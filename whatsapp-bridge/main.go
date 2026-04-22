package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal"

	"bytes"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Whether to forward messages sent by self via webhook.
// Defaults to true. Override with env FORWARD_SELF=false.
var forwardSelfMessages = getEnvBool("FORWARD_SELF", true)

// getEnvBool reads a boolean env var with a default.
// Accepts: 1/true/yes/on and 0/false/no/off (case-insensitive)
func getEnvBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// Message represents a chat message for our client
type Message struct {
	Time      time.Time
	Sender    string
	Content   string
	IsFromMe  bool
	MediaType string
	Filename  string
}

// Database handler for storing message history
type MessageStore struct {
	db *sql.DB
}

// Initialize message store
func NewMessageStore() (*MessageStore, error) {
	// Create directory for database if it doesn't exist
	if err := os.MkdirAll("store", 0755); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %v", err)
	}

	// Open SQLite database for messages
	db, err := sql.Open("sqlite3", "file:store/messages.db?_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("failed to open message database: %v", err)
	}

	// Create tables if they don't exist
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS chats (
			jid TEXT PRIMARY KEY,
			name TEXT,
			last_message_time TIMESTAMP
		);
		
		CREATE TABLE IF NOT EXISTS messages (
			id TEXT,
			chat_jid TEXT,
			sender TEXT,
			content TEXT,
			timestamp TIMESTAMP,
			is_from_me BOOLEAN,
			media_type TEXT,
			filename TEXT,
			url TEXT,
			media_key BLOB,
			file_sha256 BLOB,
			file_enc_sha256 BLOB,
			file_length INTEGER,
			PRIMARY KEY (id, chat_jid),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
		);
	`)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to create tables: %v", err)
	}

	return &MessageStore{db: db}, nil
}

// MigrateLegacyLIDChatsToPhoneJIDs rewrites message/chat rows stored under
// legacy @lid chat JIDs into phone-based @s.whatsapp.net chat JIDs using the
// whatsmeow LID map in whatsapp.db.
func (store *MessageStore) MigrateLegacyLIDChatsToPhoneJIDs(whatsappDBPath string, logger waLog.Logger) error {
	if _, err := os.Stat(whatsappDBPath); err != nil {
		if os.IsNotExist(err) {
			logger.Infof("Skipping LID chat migration: %s not found", whatsappDBPath)
			return nil
		}
		return fmt.Errorf("failed to stat WhatsApp DB %s: %w", whatsappDBPath, err)
	}

	tx, err := store.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to start LID chat migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	alias := fmt.Sprintf("wa_mig_%d", time.Now().UnixNano())
	escapedPath := strings.ReplaceAll(whatsappDBPath, "'", "''")
	if _, err := tx.Exec(fmt.Sprintf("ATTACH DATABASE '%s' AS %s;", escapedPath, alias)); err != nil {
		return fmt.Errorf("failed to attach WhatsApp DB for LID chat migration: %w", err)
	}

	var lidMapTableExists int
	if err := tx.QueryRow(fmt.Sprintf(
		"SELECT COUNT(1) FROM %s.sqlite_master WHERE type='table' AND name='whatsmeow_lid_map';",
		alias,
	)).Scan(&lidMapTableExists); err != nil {
		return fmt.Errorf("failed to inspect WhatsApp DB schema for LID migration: %w", err)
	}
	if lidMapTableExists == 0 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit no-op LID chat migration: %w", err)
		}
		logger.Infof("Skipping LID chat migration: whatsmeow_lid_map table not found")
		return nil
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		CREATE TEMP TABLE tmp_lid_to_phone AS
		SELECT DISTINCT
			lm.lid || '@lid' AS lid_jid,
			lm.pn || '@s.whatsapp.net' AS phone_jid
		FROM %s.whatsmeow_lid_map lm
		WHERE lm.lid != '' AND lm.pn != ''
		  AND (
		  	EXISTS (SELECT 1 FROM chats c WHERE c.jid = lm.lid || '@lid')
		  	OR EXISTS (SELECT 1 FROM messages m WHERE m.chat_jid = lm.lid || '@lid')
		  );
	`, alias)); err != nil {
		return fmt.Errorf("failed to build temporary LID mapping table: %w", err)
	}

	var mappedChats int
	if err := tx.QueryRow("SELECT COUNT(*) FROM tmp_lid_to_phone;").Scan(&mappedChats); err != nil {
		return fmt.Errorf("failed to count mapped LID chats: %w", err)
	}

	if mappedChats == 0 {
		if _, err := tx.Exec("DROP TABLE IF EXISTS tmp_lid_to_phone;"); err != nil {
			return fmt.Errorf("failed to clean temporary LID mapping table: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit no-op LID chat migration: %w", err)
		}
		logger.Infof("LID chat migration: nothing to migrate")
		return nil
	}

	if _, err := tx.Exec(`
		CREATE TEMP TABLE tmp_lid_chat_candidates AS
		SELECT
			m.phone_jid AS phone_jid,
			m.lid_jid AS lid_jid,
			NULLIF(TRIM(c.name), '') AS source_name,
			COALESCE(
				c.last_message_time,
				(
					SELECT MAX(msg.timestamp)
					FROM messages msg
					WHERE msg.chat_jid = m.lid_jid
				)
			) AS source_last_message_time
		FROM tmp_lid_to_phone m
		LEFT JOIN chats c ON c.jid = m.lid_jid;
	`); err != nil {
		return fmt.Errorf("failed to build temporary chat candidate table: %w", err)
	}

	if _, err := tx.Exec(`
		CREATE TEMP TABLE tmp_lid_chat_meta AS
		SELECT
			c.phone_jid AS phone_jid,
			COALESCE(
				(
					SELECT c2.source_name
					FROM tmp_lid_chat_candidates c2
					WHERE c2.phone_jid = c.phone_jid
						AND c2.source_name IS NOT NULL
					ORDER BY
						CASE WHEN c2.source_last_message_time IS NULL THEN 1 ELSE 0 END,
						c2.source_last_message_time DESC,
						c2.lid_jid ASC
					LIMIT 1
				),
				substr(c.phone_jid, 1, instr(c.phone_jid, '@') - 1)
			) AS source_name,
			MAX(c.source_last_message_time) AS source_last_message_time
		FROM tmp_lid_chat_candidates c
		GROUP BY c.phone_jid;
	`); err != nil {
		return fmt.Errorf("failed to build temporary chat metadata table: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO chats (jid, name, last_message_time)
		SELECT phone_jid, source_name, source_last_message_time
		FROM tmp_lid_chat_meta;
	`); err != nil {
		return fmt.Errorf("failed to upsert destination chat rows: %w", err)
	}

	if _, err := tx.Exec(`
		UPDATE chats
		SET
			name = CASE
				WHEN (name IS NULL OR TRIM(name) = '') THEN (
					SELECT m.source_name
					FROM tmp_lid_chat_meta m
					WHERE m.phone_jid = chats.jid
				)
				ELSE name
			END,
			last_message_time = CASE
				WHEN (
					SELECT m.source_last_message_time
					FROM tmp_lid_chat_meta m
					WHERE m.phone_jid = chats.jid
				) IS NULL THEN last_message_time
				WHEN last_message_time IS NULL THEN (
					SELECT m.source_last_message_time
					FROM tmp_lid_chat_meta m
					WHERE m.phone_jid = chats.jid
				)
				WHEN (
					SELECT m.source_last_message_time
					FROM tmp_lid_chat_meta m
					WHERE m.phone_jid = chats.jid
				) > last_message_time THEN (
					SELECT m.source_last_message_time
					FROM tmp_lid_chat_meta m
					WHERE m.phone_jid = chats.jid
				)
				ELSE last_message_time
			END
		WHERE jid IN (SELECT phone_jid FROM tmp_lid_chat_meta);
	`); err != nil {
		return fmt.Errorf("failed to merge destination chat metadata: %w", err)
	}

	insertResult, err := tx.Exec(`
		INSERT OR IGNORE INTO messages (
			id, chat_jid, sender, content, timestamp, is_from_me,
			media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length
		)
		SELECT
			msg.id,
			m.phone_jid,
			msg.sender,
			msg.content,
			msg.timestamp,
			msg.is_from_me,
			msg.media_type,
			msg.filename,
			msg.url,
			msg.media_key,
			msg.file_sha256,
			msg.file_enc_sha256,
			msg.file_length
		FROM messages msg
		JOIN tmp_lid_to_phone m ON m.lid_jid = msg.chat_jid;
	`)
	if err != nil {
		return fmt.Errorf("failed to copy legacy LID messages into phone chats: %w", err)
	}

	insertedMessages, _ := insertResult.RowsAffected()

	deleteMessagesResult, err := tx.Exec(`
		DELETE FROM messages
		WHERE chat_jid IN (SELECT lid_jid FROM tmp_lid_to_phone);
	`)
	if err != nil {
		return fmt.Errorf("failed to delete migrated LID messages: %w", err)
	}
	deletedMessages, _ := deleteMessagesResult.RowsAffected()

	deleteChatsResult, err := tx.Exec(`
		DELETE FROM chats
		WHERE jid IN (SELECT lid_jid FROM tmp_lid_to_phone);
	`)
	if err != nil {
		return fmt.Errorf("failed to delete migrated LID chats: %w", err)
	}
	deletedChats, _ := deleteChatsResult.RowsAffected()

	if _, err := tx.Exec("DROP TABLE IF EXISTS tmp_lid_to_phone;"); err != nil {
		return fmt.Errorf("failed to clean temporary LID mapping table: %w", err)
	}
	if _, err := tx.Exec("DROP TABLE IF EXISTS tmp_lid_chat_meta;"); err != nil {
		return fmt.Errorf("failed to clean temporary chat metadata table: %w", err)
	}
	if _, err := tx.Exec("DROP TABLE IF EXISTS tmp_lid_chat_candidates;"); err != nil {
		return fmt.Errorf("failed to clean temporary chat candidate table: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit LID chat migration: %w", err)
	}

	logger.Infof(
		"LID chat migration complete: mapped_chats=%d inserted_messages=%d deleted_lid_messages=%d deleted_lid_chats=%d",
		mappedChats,
		insertedMessages,
		deletedMessages,
		deletedChats,
	)
	return nil
}

// Close the database connection
func (store *MessageStore) Close() error {
	return store.db.Close()
}

// Store a chat in the database
func (store *MessageStore) StoreChat(jid, name string, lastMessageTime time.Time) error {
	_, err := store.db.Exec(
		"INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, lastMessageTime,
	)
	return err
}

// Store a message in the database
func (store *MessageStore) StoreMessage(id, chatJID, sender, content string, timestamp time.Time, isFromMe bool,
	mediaType, filename, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	// Only store if there's actual content or media
	if content == "" && mediaType == "" {
		return nil
	}

	_, err := store.db.Exec(
		`INSERT OR REPLACE INTO messages 
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, chatJID, sender, content, timestamp, isFromMe, mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength,
	)
	return err
}

// Get messages from a chat
func (store *MessageStore) GetMessages(chatJID string, limit int) ([]Message, error) {
	rows, err := store.db.Query(
		"SELECT sender, content, timestamp, is_from_me, media_type, filename FROM messages WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT ?",
		chatJID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var messages []Message
	for rows.Next() {
		var msg Message
		var timestamp time.Time
		err := rows.Scan(&msg.Sender, &msg.Content, &timestamp, &msg.IsFromMe, &msg.MediaType, &msg.Filename)
		if err != nil {
			return nil, err
		}
		msg.Time = timestamp
		messages = append(messages, msg)
	}

	return messages, nil
}

// Get all chats
func (store *MessageStore) GetChats() (map[string]time.Time, error) {
	rows, err := store.db.Query("SELECT jid, last_message_time FROM chats ORDER BY last_message_time DESC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	chats := make(map[string]time.Time)
	for rows.Next() {
		var jid string
		var lastMessageTime time.Time
		err := rows.Scan(&jid, &lastMessageTime)
		if err != nil {
			return nil, err
		}
		chats[jid] = lastMessageTime
	}

	return chats, nil
}

// ---- REST API query methods ----

// ChatResponse represents a chat in JSON API responses.
type ChatResponse struct {
	JID             string      `json:"jid"`
	Name            *string     `json:"name"`
	IsGroup         bool        `json:"is_group"`
	LastMessageTime *string     `json:"last_message_time"`
	LastMessage     *string     `json:"last_message"`
	LastSender      *string     `json:"last_sender"`
	LastIsFromMe    *bool       `json:"last_is_from_me"`
}

// MessageResponse represents a message in JSON API responses.
type MessageResponse struct {
	ID        string  `json:"id"`
	Timestamp string  `json:"timestamp"`
	Sender    string  `json:"sender"`
	ChatName  *string `json:"chat_name"`
	Content   string  `json:"content"`
	IsFromMe  bool    `json:"is_from_me"`
	ChatJID   string  `json:"chat_jid"`
	MediaType *string `json:"media_type"`
}

// ContactResponse represents a contact in JSON API responses.
type ContactResponse struct {
	PhoneNumber string  `json:"phone_number"`
	Name        *string `json:"name"`
	JID         string  `json:"jid"`
}

// MessageContextResponse represents message context in JSON API responses.
type MessageContextResponse struct {
	Message *MessageResponse  `json:"message"`
	Before  []MessageResponse `json:"before"`
	After   []MessageResponse `json:"after"`
}

func nullableString(s sql.NullString) *string {
	if s.Valid {
		return &s.String
	}
	return nil
}

func nullableBool(b sql.NullBool) *bool {
	if b.Valid {
		return &b.Bool
	}
	return nil
}

// ListChats returns chats matching the given filters.
func (store *MessageStore) ListChats(query string, limit, offset int, includeLastMessage bool, sortBy string) ([]ChatResponse, error) {
	queryParts := []string{`SELECT chats.jid, chats.name, chats.last_message_time`}
	if includeLastMessage {
		queryParts = append(queryParts, `, messages.content, messages.sender, messages.is_from_me`)
	}
	queryParts = append(queryParts, ` FROM chats`)
	if includeLastMessage {
		queryParts = append(queryParts, ` LEFT JOIN messages ON chats.jid = messages.chat_jid AND chats.last_message_time = messages.timestamp`)
	}

	var whereClauses []string
	var params []interface{}

	if query != "" {
		whereClauses = append(whereClauses, `(LOWER(chats.name) LIKE LOWER(?) OR chats.jid LIKE ?)`)
		pattern := "%" + query + "%"
		params = append(params, pattern, pattern)
	}

	if len(whereClauses) > 0 {
		queryParts = append(queryParts, " WHERE "+strings.Join(whereClauses, " AND "))
	}

	if sortBy == "name" {
		queryParts = append(queryParts, ` ORDER BY chats.name`)
	} else {
		queryParts = append(queryParts, ` ORDER BY chats.last_message_time DESC`)
	}

	queryParts = append(queryParts, ` LIMIT ? OFFSET ?`)
	params = append(params, limit, offset)

	rows, err := store.db.Query(strings.Join(queryParts, ""), params...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	chats := make([]ChatResponse, 0)
	for rows.Next() {
		var c ChatResponse
		var jid string
		var name sql.NullString
		var lastTime sql.NullString
		var lastMsg sql.NullString
		var lastSender sql.NullString
		var lastFromMe sql.NullBool

		if includeLastMessage {
			err = rows.Scan(&jid, &name, &lastTime, &lastMsg, &lastSender, &lastFromMe)
		} else {
			err = rows.Scan(&jid, &name, &lastTime)
		}
		if err != nil {
			return nil, err
		}

		c.JID = jid
		c.Name = nullableString(name)
		c.IsGroup = strings.HasSuffix(jid, "@g.us")
		c.LastMessageTime = nullableString(lastTime)
		if includeLastMessage {
			c.LastMessage = nullableString(lastMsg)
			c.LastSender = nullableString(lastSender)
			c.LastIsFromMe = nullableBool(lastFromMe)
		}
		chats = append(chats, c)
	}
	return chats, nil
}

// GetChatByJID returns a single chat by its JID.
func (store *MessageStore) GetChatByJID(jid string, includeLastMessage bool) (*ChatResponse, error) {
	queryParts := []string{`SELECT c.jid, c.name, c.last_message_time`}
	if includeLastMessage {
		queryParts = append(queryParts, `, m.content, m.sender, m.is_from_me`)
	}
	queryParts = append(queryParts, ` FROM chats c`)
	if includeLastMessage {
		queryParts = append(queryParts, ` LEFT JOIN messages m ON c.jid = m.chat_jid AND c.last_message_time = m.timestamp`)
	}
	queryParts = append(queryParts, ` WHERE c.jid = ?`)

	var c ChatResponse
	var name sql.NullString
	var lastTime sql.NullString
	var lastMsg sql.NullString
	var lastSender sql.NullString
	var lastFromMe sql.NullBool

	var err error
	row := store.db.QueryRow(strings.Join(queryParts, ""), jid)
	if includeLastMessage {
		err = row.Scan(&c.JID, &name, &lastTime, &lastMsg, &lastSender, &lastFromMe)
	} else {
		err = row.Scan(&c.JID, &name, &lastTime)
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	c.Name = nullableString(name)
	c.IsGroup = strings.HasSuffix(c.JID, "@g.us")
	c.LastMessageTime = nullableString(lastTime)
	if includeLastMessage {
		c.LastMessage = nullableString(lastMsg)
		c.LastSender = nullableString(lastSender)
		c.LastIsFromMe = nullableBool(lastFromMe)
	}
	return &c, nil
}

// GetDirectChatByPhone returns the direct (non-group) chat matching a phone number.
func (store *MessageStore) GetDirectChatByPhone(phone string) (*ChatResponse, error) {
	row := store.db.QueryRow(`
		SELECT c.jid, c.name, c.last_message_time,
		       m.content, m.sender, m.is_from_me
		FROM chats c
		LEFT JOIN messages m ON c.jid = m.chat_jid AND c.last_message_time = m.timestamp
		WHERE c.jid LIKE ? AND c.jid NOT LIKE '%@g.us'
		LIMIT 1
	`, "%"+phone+"%")

	var c ChatResponse
	var name sql.NullString
	var lastTime sql.NullString
	var lastMsg sql.NullString
	var lastSender sql.NullString
	var lastFromMe sql.NullBool

	err := row.Scan(&c.JID, &name, &lastTime, &lastMsg, &lastSender, &lastFromMe)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	c.Name = nullableString(name)
	c.IsGroup = false
	c.LastMessageTime = nullableString(lastTime)
	c.LastMessage = nullableString(lastMsg)
	c.LastSender = nullableString(lastSender)
	c.LastIsFromMe = nullableBool(lastFromMe)
	return &c, nil
}

// GetContactChats returns all chats involving a contact.
func (store *MessageStore) GetContactChats(jid string, limit, offset int) ([]ChatResponse, error) {
	rows, err := store.db.Query(`
		SELECT DISTINCT c.jid, c.name, c.last_message_time,
		       m.content, m.sender, m.is_from_me
		FROM chats c
		JOIN messages m ON c.jid = m.chat_jid
		WHERE m.sender = ? OR c.jid = ?
		ORDER BY c.last_message_time DESC
		LIMIT ? OFFSET ?
	`, jid, jid, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	chats := make([]ChatResponse, 0)
	for rows.Next() {
		var c ChatResponse
		var name sql.NullString
		var lastTime sql.NullString
		var lastMsg sql.NullString
		var lastSender sql.NullString
		var lastFromMe sql.NullBool

		if err := rows.Scan(&c.JID, &name, &lastTime, &lastMsg, &lastSender, &lastFromMe); err != nil {
			return nil, err
		}
		c.Name = nullableString(name)
		c.IsGroup = strings.HasSuffix(c.JID, "@g.us")
		c.LastMessageTime = nullableString(lastTime)
		c.LastMessage = nullableString(lastMsg)
		c.LastSender = nullableString(lastSender)
		c.LastIsFromMe = nullableBool(lastFromMe)
		chats = append(chats, c)
	}
	return chats, nil
}

// ListMessages returns messages matching the given filters.
func (store *MessageStore) ListMessages(chatJID, sender, search, after, before, sortBy string, limit, offset int) ([]MessageResponse, error) {
	queryParts := []string{`SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages JOIN chats ON messages.chat_jid = chats.jid`}

	var whereClauses []string
	var params []interface{}

	if chatJID != "" {
		whereClauses = append(whereClauses, `messages.chat_jid = ?`)
		params = append(params, chatJID)
	}
	if sender != "" {
		whereClauses = append(whereClauses, `messages.sender = ?`)
		params = append(params, sender)
	}
	if search != "" {
		whereClauses = append(whereClauses, `LOWER(messages.content) LIKE LOWER(?)`)
		params = append(params, "%"+search+"%")
	}
	if after != "" {
		whereClauses = append(whereClauses, `messages.timestamp > ?`)
		params = append(params, after)
	}
	if before != "" {
		whereClauses = append(whereClauses, `messages.timestamp < ?`)
		params = append(params, before)
	}

	if len(whereClauses) > 0 {
		queryParts = append(queryParts, " WHERE "+strings.Join(whereClauses, " AND "))
	}

	order := "DESC"
	if sortBy == "oldest" {
		order = "ASC"
	}
	queryParts = append(queryParts, fmt.Sprintf(` ORDER BY messages.timestamp %s`, order))
	queryParts = append(queryParts, ` LIMIT ? OFFSET ?`)
	params = append(params, limit, offset)

	rows, err := store.db.Query(strings.Join(queryParts, ""), params...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	messages := make([]MessageResponse, 0)
	for rows.Next() {
		var m MessageResponse
		var chatName sql.NullString
		var mediaType sql.NullString
		if err := rows.Scan(&m.Timestamp, &m.Sender, &chatName, &m.Content, &m.IsFromMe, &m.ChatJID, &m.ID, &mediaType); err != nil {
			return nil, err
		}
		m.ChatName = nullableString(chatName)
		m.MediaType = nullableString(mediaType)
		messages = append(messages, m)
	}
	return messages, nil
}

// GetMessageContext returns a message and surrounding context.
func (store *MessageStore) GetMessageContext(messageID string, beforeCount, afterCount int) (*MessageContextResponse, error) {
	// Get target message
	row := store.db.QueryRow(`
		SELECT messages.timestamp, messages.sender, chats.name, messages.content,
		       messages.is_from_me, chats.jid, messages.id, messages.chat_jid, messages.media_type
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.id = ?
	`, messageID)

	var target MessageResponse
	var chatName sql.NullString
	var mediaType sql.NullString
	var chatJID string
	var timestamp string

	err := row.Scan(&timestamp, &target.Sender, &chatName, &target.Content, &target.IsFromMe, &target.ChatJID, &target.ID, &chatJID, &mediaType)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	target.Timestamp = timestamp
	target.ChatName = nullableString(chatName)
	target.MediaType = nullableString(mediaType)

	// Get messages before
	beforeRows, err := store.db.Query(`
		SELECT messages.timestamp, messages.sender, chats.name, messages.content,
		       messages.is_from_me, chats.jid, messages.id, messages.media_type
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.chat_jid = ? AND messages.timestamp < ?
		ORDER BY messages.timestamp DESC
		LIMIT ?
	`, chatJID, timestamp, beforeCount)
	if err != nil {
		return nil, err
	}
	defer func() { _ = beforeRows.Close() }()

	beforeMsgs := make([]MessageResponse, 0)
	for beforeRows.Next() {
		var m MessageResponse
		var cn sql.NullString
		var mt sql.NullString
		if err := beforeRows.Scan(&m.Timestamp, &m.Sender, &cn, &m.Content, &m.IsFromMe, &m.ChatJID, &m.ID, &mt); err != nil {
			return nil, err
		}
		m.ChatName = nullableString(cn)
		m.MediaType = nullableString(mt)
		beforeMsgs = append(beforeMsgs, m)
	}
	// Reverse before messages to chronological order
	for i, j := 0, len(beforeMsgs)-1; i < j; i, j = i+1, j-1 {
		beforeMsgs[i], beforeMsgs[j] = beforeMsgs[j], beforeMsgs[i]
	}

	// Get messages after
	afterRows, err := store.db.Query(`
		SELECT messages.timestamp, messages.sender, chats.name, messages.content,
		       messages.is_from_me, chats.jid, messages.id, messages.media_type
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.chat_jid = ? AND messages.timestamp > ?
		ORDER BY messages.timestamp ASC
		LIMIT ?
	`, chatJID, timestamp, afterCount)
	if err != nil {
		return nil, err
	}
	defer func() { _ = afterRows.Close() }()

	afterMsgs := make([]MessageResponse, 0)
	for afterRows.Next() {
		var m MessageResponse
		var cn sql.NullString
		var mt sql.NullString
		if err := afterRows.Scan(&m.Timestamp, &m.Sender, &cn, &m.Content, &m.IsFromMe, &m.ChatJID, &m.ID, &mt); err != nil {
			return nil, err
		}
		m.ChatName = nullableString(cn)
		m.MediaType = nullableString(mt)
		afterMsgs = append(afterMsgs, m)
	}

	return &MessageContextResponse{
		Message: &target,
		Before:  beforeMsgs,
		After:   afterMsgs,
	}, nil
}

// GetLastInteraction returns the most recent message involving a contact.
func (store *MessageStore) GetLastInteraction(jid string) (*MessageResponse, error) {
	row := store.db.QueryRow(`
		SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me,
		       c.jid, m.id, m.media_type
		FROM messages m
		JOIN chats c ON m.chat_jid = c.jid
		WHERE m.sender = ? OR c.jid = ?
		ORDER BY m.timestamp DESC
		LIMIT 1
	`, jid, jid)

	var m MessageResponse
	var chatName sql.NullString
	var mediaType sql.NullString

	err := row.Scan(&m.Timestamp, &m.Sender, &chatName, &m.Content, &m.IsFromMe, &m.ChatJID, &m.ID, &mediaType)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.ChatName = nullableString(chatName)
	m.MediaType = nullableString(mediaType)
	return &m, nil
}

// SearchContacts searches contacts by name or phone number.
func (store *MessageStore) SearchContacts(query string) ([]ContactResponse, error) {
	pattern := "%" + query + "%"
	rows, err := store.db.Query(`
		SELECT DISTINCT jid, name
		FROM chats
		WHERE (LOWER(name) LIKE LOWER(?) OR LOWER(jid) LIKE LOWER(?))
		  AND jid NOT LIKE '%@g.us'
		ORDER BY name, jid
		LIMIT 50
	`, pattern, pattern)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	contacts := make([]ContactResponse, 0)
	for rows.Next() {
		var jid string
		var name sql.NullString
		if err := rows.Scan(&jid, &name); err != nil {
			return nil, err
		}
		phone := jid
		if idx := strings.Index(jid, "@"); idx >= 0 {
			phone = jid[:idx]
		}
		contacts = append(contacts, ContactResponse{
			PhoneNumber: phone,
			Name:        nullableString(name),
			JID:         jid,
		})
	}
	return contacts, nil
}

// GetSenderName resolves a sender JID to a display name.
func (store *MessageStore) GetSenderName(senderJID string) (string, error) {
	// First try exact JID match
	var name sql.NullString
	err := store.db.QueryRow(`SELECT name FROM chats WHERE jid = ? LIMIT 1`, senderJID).Scan(&name)
	if err == nil && name.Valid && name.String != "" {
		return name.String, nil
	}

	// Try partial match by phone number
	phonePart := senderJID
	if idx := strings.Index(senderJID, "@"); idx >= 0 {
		phonePart = senderJID[:idx]
	}
	err = store.db.QueryRow(`SELECT name FROM chats WHERE jid LIKE ? LIMIT 1`, "%"+phonePart+"%").Scan(&name)
	if err == nil && name.Valid && name.String != "" {
		return name.String, nil
	}

	return senderJID, nil
}

// Extract text content from a message
func extractTextContent(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}

	// Try to get text content
	if text := msg.GetConversation(); text != "" {
		return text
	} else if extendedText := msg.GetExtendedTextMessage(); extendedText != nil {
		return extendedText.GetText()
	}

	// For now, we're ignoring non-text messages
	return ""
}

// SendMessageResponse represents the response for the send message API
type SendMessageResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// SendMessageRequest represents the request body for the send message API
type SendMessageRequest struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
	MediaPath string `json:"media_path,omitempty"`
}

// Function to send a WhatsApp message
func sendWhatsAppMessage(client *whatsmeow.Client, recipient string, message string, mediaPath string) (bool, string) {
	if !client.IsConnected() {
		return false, "Not connected to WhatsApp"
	}

	// Create JID for recipient
	var recipientJID types.JID
	var err error

	// Check if recipient is a JID
	isJID := strings.Contains(recipient, "@")

	if isJID {
		// Parse the JID string
		recipientJID, err = types.ParseJID(recipient)
		if err != nil {
			return false, fmt.Sprintf("Error parsing JID: %v", err)
		}
	} else {
		// Create JID from phone number
		recipientJID = types.JID{
			User:   recipient,
			Server: "s.whatsapp.net", // For personal chats
		}
	}

	// For personal chats, resolve phone number JID to LID (Linked Identity).
	// WhatsApp is migrating to LID-based addressing; messages sent to the
	// phone JID silently fail for migrated contacts.
	if recipientJID.Server == types.DefaultUserServer {
		ctx := context.Background()
		lid, lidErr := client.Store.LIDs.GetLIDForPN(ctx, recipientJID)
		if lidErr == nil && !lid.IsEmpty() {
			fmt.Printf("Resolved %s -> %s (LID)\n", recipientJID, lid)
			recipientJID = lid
		} else {
			// Cache miss or cache error — ask the WhatsApp server.
			if lidErr != nil {
				fmt.Printf("Warning: LID cache lookup failed for %s: %v, falling back to server\n", recipientJID, lidErr)
			}
			info, infoErr := client.GetUserInfo(ctx, []types.JID{recipientJID})
			if infoErr != nil {
				fmt.Printf("Warning: server LID lookup failed for %s: %v\n", recipientJID, infoErr)
			} else if userInfo, ok := info[recipientJID]; ok && !userInfo.LID.IsEmpty() {
				fmt.Printf("Resolved %s -> %s (LID via server)\n", recipientJID, userInfo.LID)
				recipientJID = userInfo.LID
			}
		}
	}

	msg := &waProto.Message{}

	// Check if we have media to send
	if mediaPath != "" {
		// Read media file
		mediaData, err := os.ReadFile(mediaPath)
		if err != nil {
			return false, fmt.Sprintf("Error reading media file: %v", err)
		}

		// Determine media type and mime type based on file extension
		fileExt := strings.ToLower(mediaPath[strings.LastIndex(mediaPath, ".")+1:])
		var mediaType whatsmeow.MediaType
		var mimeType string

		// Handle different media types
		switch fileExt {
		// Image types
		case "jpg", "jpeg":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/jpeg"
		case "png":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/png"
		case "gif":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/gif"
		case "webp":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/webp"

		// Audio types
		case "ogg":
			mediaType = whatsmeow.MediaAudio
			mimeType = "audio/ogg; codecs=opus"

		// Video types
		case "mp4":
			mediaType = whatsmeow.MediaVideo
			mimeType = "video/mp4"
		case "avi":
			mediaType = whatsmeow.MediaVideo
			mimeType = "video/avi"
		case "mov":
			mediaType = whatsmeow.MediaVideo
			mimeType = "video/quicktime"

		// Document types (for any other file type)
		default:
			mediaType = whatsmeow.MediaDocument
			mimeType = "application/octet-stream"
		}

		// Upload media to WhatsApp servers
		resp, err := client.Upload(context.Background(), mediaData, mediaType)
		if err != nil {
			return false, fmt.Sprintf("Error uploading media: %v", err)
		}

		fmt.Println("Media uploaded", resp)

		// Create the appropriate message type based on media type
		switch mediaType {
		case whatsmeow.MediaImage:
			msg.ImageMessage = &waProto.ImageMessage{
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		case whatsmeow.MediaAudio:
			// Handle ogg audio files
			var seconds uint32 = 30 // Default fallback
			var waveform []byte = nil

			// Try to analyze the ogg file
			if strings.Contains(mimeType, "ogg") {
				analyzedSeconds, analyzedWaveform, err := analyzeOggOpus(mediaData)
				if err == nil {
					seconds = analyzedSeconds
					waveform = analyzedWaveform
				} else {
					return false, fmt.Sprintf("Failed to analyze Ogg Opus file: %v", err)
				}
			} else {
				fmt.Printf("Not an Ogg Opus file: %s\n", mimeType)
			}

			msg.AudioMessage = &waProto.AudioMessage{
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
				Seconds:       proto.Uint32(seconds),
				PTT:           proto.Bool(true),
				Waveform:      waveform,
			}
		case whatsmeow.MediaVideo:
			msg.VideoMessage = &waProto.VideoMessage{
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		case whatsmeow.MediaDocument:
			msg.DocumentMessage = &waProto.DocumentMessage{
				Title:         proto.String(mediaPath[strings.LastIndex(mediaPath, "/")+1:]),
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		}
	} else {
		msg.Conversation = proto.String(message)
	}

	// Send message
	_, err = client.SendMessage(context.Background(), recipientJID, msg)

	if err != nil {
		return false, fmt.Sprintf("Error sending message: %v", err)
	}

	return true, fmt.Sprintf("Message sent to %s", recipient)
}

// Extract quoted message info from ContextInfo
func extractQuotedMessageInfo(msg *waProto.Message) (quotedMessageId string, quotedSender string, quotedContent string) {
	if msg == nil {
		return "", "", ""
	}

	var contextInfo *waProto.ContextInfo

	// Check all message types that can have ContextInfo
	if extText := msg.GetExtendedTextMessage(); extText != nil {
		contextInfo = extText.GetContextInfo()
	} else if img := msg.GetImageMessage(); img != nil {
		contextInfo = img.GetContextInfo()
	} else if vid := msg.GetVideoMessage(); vid != nil {
		contextInfo = vid.GetContextInfo()
	} else if doc := msg.GetDocumentMessage(); doc != nil {
		contextInfo = doc.GetContextInfo()
	} else if aud := msg.GetAudioMessage(); aud != nil {
		contextInfo = aud.GetContextInfo()
	}

	if contextInfo == nil {
		return "", "", ""
	}

	// Extract quoted message ID (StanzaID)
	if contextInfo.StanzaID != nil {
		quotedMessageId = *contextInfo.StanzaID
	}

	// Extract quoted sender (Participant)
	if contextInfo.Participant != nil {
		quotedSender = *contextInfo.Participant
	}

	// Extract quoted message content
	if quotedMsg := contextInfo.QuotedMessage; quotedMsg != nil {
		quotedContent = extractTextContent(quotedMsg)
	}

	return quotedMessageId, quotedSender, quotedContent
}

// Extract media info from a message - uses provided timestamp for consistent filenames
func extractMediaInfo(msg *waProto.Message, msgTimestamp time.Time) (mediaType string, filename string, url string, mediaKey []byte, fileSHA256 []byte, fileEncSHA256 []byte, fileLength uint64) {
	if msg == nil {
		return "", "", "", nil, nil, nil, 0
	}

	// Use message timestamp for filename, fallback to current time if zero
	ts := msgTimestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	tsStr := ts.Format("20060102_150405")

	// Check for image message
	if img := msg.GetImageMessage(); img != nil {
		return "image", "image_" + tsStr + ".jpg",
			img.GetURL(), img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()
	}

	// Check for video message
	if vid := msg.GetVideoMessage(); vid != nil {
		return "video", "video_" + tsStr + ".mp4",
			vid.GetURL(), vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()
	}

	// Check for audio message
	if aud := msg.GetAudioMessage(); aud != nil {
		return "audio", "audio_" + tsStr + ".ogg",
			aud.GetURL(), aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()
	}

	// Check for document message
	if doc := msg.GetDocumentMessage(); doc != nil {
		filename := doc.GetFileName()
		if filename == "" {
			filename = "document_" + tsStr
		}
		return "document", filename,
			doc.GetURL(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}

	return "", "", "", nil, nil, nil, 0
}

// resolveLIDChat resolves a LID-based chat JID to its phone-based equivalent
// so that incoming and outgoing messages are stored under the same chat entry.
// The senderAlt/recipientAlt fields carry the phone JID on live messages;
// for history sync these will be empty and the function falls back to the
// whatsmeow LID store (populated during live message handling).
func resolveLIDChat(client *whatsmeow.Client, chat, senderAlt, recipientAlt types.JID, isFromMe bool) types.JID {
	if chat.Server != types.HiddenUserServer {
		return chat
	}

	// For incoming DMs the phone JID is in SenderAlt;
	// for outgoing DMs it is in RecipientAlt.
	var alt types.JID
	if !isFromMe && !senderAlt.IsEmpty() && senderAlt.Server == types.DefaultUserServer {
		alt = senderAlt.ToNonAD()
	} else if isFromMe && !recipientAlt.IsEmpty() && recipientAlt.Server == types.DefaultUserServer {
		alt = recipientAlt.ToNonAD()
	}

	if !alt.IsEmpty() {
		fmt.Printf("Resolved LID chat %s -> %s (from message alt)\n", chat, alt)
		return alt
	}

	// Fallback: query the whatsmeow LID-PN mapping store.
	pn, err := client.Store.LIDs.GetPNForLID(context.Background(), chat)
	if err == nil && !pn.IsEmpty() {
		fmt.Printf("Resolved LID chat %s -> %s (from LID store)\n", chat, pn.ToNonAD())
		return pn.ToNonAD()
	}

	fmt.Printf("Warning: could not resolve LID chat %s to phone JID\n", chat)
	return chat
}

// Handle regular incoming messages with media support
func handleMessage(client *whatsmeow.Client, messageStore *MessageStore, msg *events.Message, logger waLog.Logger) {
	// Resolve LID-based chats to phone-based JIDs so that incoming
	// and outgoing messages land in the same chat entry.
	resolvedChat := resolveLIDChat(client, msg.Info.Chat, msg.Info.SenderAlt, msg.Info.RecipientAlt, msg.Info.IsFromMe)
	chatJID := resolvedChat.String()
	sender := msg.Info.Sender.User

	// Get appropriate chat name (pass resolved JID so contact lookup works)
	name := GetChatName(client, messageStore, resolvedChat, chatJID, nil, sender, logger)

	// If contact resolution fails (common for LIDs), PushName is often the best available display name.
	// Only apply for direct messages (not groups) and only when the stored name is the numeric JID user.
	if !msg.Info.IsFromMe && msg.Info.Chat.Server != "g.us" && strings.TrimSpace(msg.Info.PushName) != "" {
		pushName := strings.TrimSpace(msg.Info.PushName)
		if name == "" || name == msg.Info.Chat.User {
			logger.Infof("Updating chat name from PushName for %s: %s -> %s", chatJID, name, pushName)
			name = pushName
		}
	}

	// Update chat in database with the message timestamp (keeps last message time updated)
	err := messageStore.StoreChat(chatJID, name, msg.Info.Timestamp)
	if err != nil {
		logger.Warnf("Failed to store chat: %v", err)
	}

	// Extract text content
	content := extractTextContent(msg.Message)

	// Extract media info - pass message timestamp for consistent filenames
	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength := extractMediaInfo(msg.Message, msg.Info.Timestamp)

	// Extract quoted message info
	quotedMessageId, quotedSender, quotedContent := extractQuotedMessageInfo(msg.Message)

	// Skip if there's no content and no media
	if content == "" && mediaType == "" {
		return
	}

	// Auto-download media if present
	if mediaType != "" && url != "" && len(mediaKey) > 0 {
		logger.Infof("Auto-downloading %s media for message %s", mediaType, msg.Info.ID)
		go func() {
			success, _, _, downloadPath, err := downloadMedia(client, messageStore, msg.Info.ID, chatJID)
			if success && err == nil {
				logger.Infof("✅ Auto-downloaded media: %s", downloadPath)
			} else {
				logger.Warnf("❌ Auto-download failed: %v", err)
			}
		}()
	}

	// Store message in database
	err = messageStore.StoreMessage(
		msg.Info.ID,
		chatJID,
		sender,
		content,
		msg.Info.Timestamp,
		msg.Info.IsFromMe,
		mediaType,
		filename,
		url,
		mediaKey,
		fileSHA256,
		fileEncSHA256,
		fileLength,
	)

	// Send webhook for incoming messages
	// Forward self-messages when FORWARD_SELF=true
	if content != "" && (forwardSelfMessages || !msg.Info.IsFromMe) {
		SendWebhook(sender, content, chatJID, msg.Info.IsFromMe, quotedMessageId, quotedSender, quotedContent)
	}

	if err != nil {
		logger.Warnf("Failed to store message: %v", err)
	} else {
		// Log message reception
		timestamp := msg.Info.Timestamp.Format("2006-01-02 15:04:05")
		direction := "←"
		if msg.Info.IsFromMe {
			direction = "→"
		}

		// Log based on message type
		if mediaType != "" {
			fmt.Printf("[%s] %s %s: [%s: %s] %s\n", timestamp, direction, sender, mediaType, filename, content)
		} else if content != "" {
			fmt.Printf("[%s] %s %s: %s\n", timestamp, direction, sender, content)
		}
	}
}

// DownloadMediaRequest represents the request body for the download media API
type DownloadMediaRequest struct {
	MessageID string `json:"message_id"`
	ChatJID   string `json:"chat_jid"`
}

// DownloadMediaResponse represents the response for the download media API
type DownloadMediaResponse struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Filename string `json:"filename,omitempty"`
	Path     string `json:"path,omitempty"`
}

// Store additional media info in the database
func (store *MessageStore) StoreMediaInfo(id, chatJID, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	_, err := store.db.Exec(
		"UPDATE messages SET url = ?, media_key = ?, file_sha256 = ?, file_enc_sha256 = ?, file_length = ? WHERE id = ? AND chat_jid = ?",
		url, mediaKey, fileSHA256, fileEncSHA256, fileLength, id, chatJID,
	)
	return err
}

// Get media info from the database
func (store *MessageStore) GetMediaInfo(id, chatJID string) (string, string, string, []byte, []byte, []byte, uint64, error) {
	var mediaType, filename, url string
	var mediaKey, fileSHA256, fileEncSHA256 []byte
	var fileLength uint64

	err := store.db.QueryRow(
		"SELECT media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length FROM messages WHERE id = ? AND chat_jid = ?",
		id, chatJID,
	).Scan(&mediaType, &filename, &url, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength)

	return mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength, err
}

// MediaDownloader implements the whatsmeow.DownloadableMessage interface
type MediaDownloader struct {
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileLength    uint64
	FileSHA256    []byte
	FileEncSHA256 []byte
	MediaType     whatsmeow.MediaType
}

// GetDirectPath implements the DownloadableMessage interface
func (d *MediaDownloader) GetDirectPath() string {
	return d.DirectPath
}

// GetURL implements the DownloadableMessage interface
func (d *MediaDownloader) GetURL() string {
	return d.URL
}

// GetMediaKey implements the DownloadableMessage interface
func (d *MediaDownloader) GetMediaKey() []byte {
	return d.MediaKey
}

// GetFileLength implements the DownloadableMessage interface
func (d *MediaDownloader) GetFileLength() uint64 {
	return d.FileLength
}

// GetFileSHA256 implements the DownloadableMessage interface
func (d *MediaDownloader) GetFileSHA256() []byte {
	return d.FileSHA256
}

// GetFileEncSHA256 implements the DownloadableMessage interface
func (d *MediaDownloader) GetFileEncSHA256() []byte {
	return d.FileEncSHA256
}

// GetMediaType implements the DownloadableMessage interface
func (d *MediaDownloader) GetMediaType() whatsmeow.MediaType {
	return d.MediaType
}

// Function to download media from a message
func downloadMedia(client *whatsmeow.Client, messageStore *MessageStore, messageID, chatJID string) (bool, string, string, string, error) {
	// Query the database for the message including timestamp
	var mediaType, url string
	var mediaKey, fileSHA256, fileEncSHA256 []byte
	var fileLength uint64
	var timestamp time.Time
	var err error

	// Get media info AND timestamp from the database
	err = messageStore.db.QueryRow(
		"SELECT media_type, url, media_key, file_sha256, file_enc_sha256, file_length, timestamp FROM messages WHERE id = ? AND chat_jid = ?",
		messageID, chatJID,
	).Scan(&mediaType, &url, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength, &timestamp)

	if err != nil {
		return false, "", "", "", fmt.Errorf("failed to find message: %v", err)
	}

	// Check if this is a media message
	if mediaType == "" {
		return false, "", "", "", fmt.Errorf("not a media message")
	}

	// Generate filename from message timestamp (not stored filename which may be wrong)
	var ext string
	switch mediaType {
	case "image":
		ext = ".jpg"
	case "video":
		ext = ".mp4"
	case "audio":
		ext = ".ogg"
	case "document":
		ext = ""
	default:
		ext = ""
	}
	filename := fmt.Sprintf("%s_%s%s", mediaType, timestamp.Format("20060102_150405"), ext)

	// First, check if we already have this file
	chatDir := fmt.Sprintf("store/%s", strings.ReplaceAll(chatJID, ":", "_"))

	// Create directory for the chat if it doesn't exist
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		return false, "", "", "", fmt.Errorf("failed to create chat directory: %v", err)
	}

	// Generate a local path for the file
	localPath := fmt.Sprintf("%s/%s", chatDir, filename)

	// Get absolute path
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return false, "", "", "", fmt.Errorf("failed to get absolute path: %v", err)
	}

	// Check if file already exists
	if _, err := os.Stat(localPath); err == nil {
		// File exists, return it
		fmt.Printf("📁 File already exists: %s\n", absPath)
		return true, mediaType, filename, absPath, nil
	}

	// If we don't have all the media info we need, we can't download
	if url == "" || len(mediaKey) == 0 || len(fileSHA256) == 0 || len(fileEncSHA256) == 0 || fileLength == 0 {
		return false, "", "", "", fmt.Errorf("incomplete media information for download")
	}

	fmt.Printf("Attempting to download media for message %s in chat %s...\n", messageID, chatJID)

	// Extract direct path from URL
	directPath := extractDirectPathFromURL(url)

	// Create a downloader that implements DownloadableMessage
	var waMediaType whatsmeow.MediaType
	switch mediaType {
	case "image":
		waMediaType = whatsmeow.MediaImage
	case "video":
		waMediaType = whatsmeow.MediaVideo
	case "audio":
		waMediaType = whatsmeow.MediaAudio
	case "document":
		waMediaType = whatsmeow.MediaDocument
	default:
		return false, "", "", "", fmt.Errorf("unsupported media type: %s", mediaType)
	}

	downloader := &MediaDownloader{
		URL:           url,
		DirectPath:    directPath,
		MediaKey:      mediaKey,
		FileLength:    fileLength,
		FileSHA256:    fileSHA256,
		FileEncSHA256: fileEncSHA256,
		MediaType:     waMediaType,
	}

	// Download the media using whatsmeow client
	mediaData, err := client.Download(context.Background(), downloader)
	if err != nil {
		return false, "", "", "", fmt.Errorf("failed to download media: %v", err)
	}

	// Save the downloaded media to file
	if err := os.WriteFile(localPath, mediaData, 0644); err != nil {
		return false, "", "", "", fmt.Errorf("failed to save media file: %v", err)
	}

	fmt.Printf("Successfully downloaded %s media to %s (%d bytes)\n", mediaType, absPath, len(mediaData))
	return true, mediaType, filename, absPath, nil
}

// Extract direct path from a WhatsApp media URL
func extractDirectPathFromURL(url string) string {
	// The direct path is typically in the URL, we need to extract it
	// Example URL: https://mmg.whatsapp.net/v/t62.7118-24/13812002_698058036224062_3424455886509161511_n.enc?ccb=11-4&oh=...

	// Find the path part after the domain
	parts := strings.SplitN(url, ".net/", 2)
	if len(parts) < 2 {
		return url // Return original URL if parsing fails
	}

	pathPart := parts[1]

	// Remove query parameters
	pathPart = strings.SplitN(pathPart, "?", 2)[0]

	// Create proper direct path format
	return "/" + pathPart
}

// Start a REST API server to expose the WhatsApp client functionality
func startRESTServer(client *whatsmeow.Client, messageStore *MessageStore, port int) {
	// Health check endpoint
	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := map[string]interface{}{
			"status":    "ok",
			"connected": client.IsConnected(),
			"timestamp": time.Now().Unix(),
		}
		if !client.IsConnected() {
			status["status"] = "disconnected"
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(status)
	})

	// Handler for sending messages
	http.HandleFunc("/api/send", func(w http.ResponseWriter, r *http.Request) {
		// Only allow POST requests
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse the request body
		var req SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}

		// Validate request
		if req.Recipient == "" {
			http.Error(w, "Recipient is required", http.StatusBadRequest)
			return
		}

		if req.Message == "" && req.MediaPath == "" {
			http.Error(w, "Message or media path is required", http.StatusBadRequest)
			return
		}

		fmt.Println("Received request to send message", req.Message, req.MediaPath)

		// Send the message
		success, message := sendWhatsAppMessage(client, req.Recipient, req.Message, req.MediaPath)
		fmt.Println("Message sent", success, message)
		// Set response headers
		w.Header().Set("Content-Type", "application/json")

		// Set appropriate status code
		if !success {
			w.WriteHeader(http.StatusInternalServerError)
		}

		// Send response
		_ = json.NewEncoder(w).Encode(SendMessageResponse{
			Success: success,
			Message: message,
		})
	})

	// Handler for downloading media
	http.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		// Only allow POST requests
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Check if connected
		if !client.IsConnected() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(DownloadMediaResponse{
				Success: false,
				Message: "WhatsApp client is not connected. Please wait for reconnection.",
			})
			return
		}

		// Parse the request body
		var req DownloadMediaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}

		// Validate request
		if req.MessageID == "" || req.ChatJID == "" {
			http.Error(w, "Message ID and Chat JID are required", http.StatusBadRequest)
			return
		}

		// Log download request for debugging
		fmt.Printf("📥 Download request: message_id=%s chat_jid=%s\n", req.MessageID, req.ChatJID)

		// Download the media
		success, mediaType, filename, path, err := downloadMedia(client, messageStore, req.MessageID, req.ChatJID)

		// Set response headers
		w.Header().Set("Content-Type", "application/json")

		// Handle download result
		if !success || err != nil {
			errMsg := "Unknown error"
			if err != nil {
				errMsg = err.Error()
			}

			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(DownloadMediaResponse{
				Success: false,
				Message: fmt.Sprintf("Failed to download media: %s", errMsg),
			})
			return
		}

		// Send successful response
		_ = json.NewEncoder(w).Encode(DownloadMediaResponse{
			Success:  true,
			Message:  fmt.Sprintf("Successfully downloaded %s media", mediaType),
			Filename: filename,
			Path:     path,
		})
	})

	// Handler for sending typing indicator
	http.HandleFunc("/api/typing", func(w http.ResponseWriter, r *http.Request) {
		// Only allow POST requests
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse the request body
		var req struct {
			Recipient string `json:"recipient"`
			IsTyping  bool   `json:"is_typing"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}

		// Validate request
		if req.Recipient == "" {
			http.Error(w, "Recipient is required", http.StatusBadRequest)
			return
		}

		// Create JID for recipient
		var recipientJID types.JID
		var err error

		// Check if recipient is a JID
		if strings.Contains(req.Recipient, "@") {
			recipientJID, err = types.ParseJID(req.Recipient)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"success": false,
					"message": fmt.Sprintf("Error parsing JID: %v", err),
				})
				return
			}
		} else {
			// Create JID from phone number
			recipientJID = types.JID{
				User:   req.Recipient,
				Server: "s.whatsapp.net",
			}
		}

		// Determine the chat presence state
		var state types.ChatPresence
		if req.IsTyping {
			state = types.ChatPresenceComposing
		} else {
			state = types.ChatPresencePaused
		}

		// Send the chat presence update
		err = client.SendChatPresence(context.Background(), recipientJID, state, types.ChatPresenceMediaText)

		// Set response headers
		w.Header().Set("Content-Type", "application/json")

		// Send response
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": fmt.Sprintf("Failed to send typing indicator: %v", err),
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"message": fmt.Sprintf("Typing indicator set to %v", req.IsTyping),
			})
		}
	})

	// GET /api/chats - List chats from SQLite
	http.HandleFunc("/api/chats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		q := r.URL.Query()
		query := q.Get("query")
		limitStr := q.Get("limit")
		pageStr := q.Get("page")
		includeLast := q.Get("include_last_message")
		sortBy := q.Get("sort_by")
		jid := q.Get("jid")
		contactJID := q.Get("contact_jid")
		phone := q.Get("phone")

		limit := 20
		if limitStr != "" {
			if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
				limit = v
			}
		}
		page := 0
		if pageStr != "" {
			if v, err := strconv.Atoi(pageStr); err == nil && v >= 0 {
				page = v
			}
		}
		includeLastMessage := true
		if includeLast == "false" || includeLast == "0" {
			includeLastMessage = false
		}
		if sortBy == "" {
			sortBy = "last_active"
		}
		offset := page * limit

		// Single chat lookup by JID
		if jid != "" {
			chat, err := messageStore.GetChatByJID(jid, includeLastMessage)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
				return
			}
			if chat == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "chat not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(chat)
			return
		}

		// Direct chat lookup by phone number
		if phone != "" {
			chat, err := messageStore.GetDirectChatByPhone(phone)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
				return
			}
			if chat == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "chat not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(chat)
			return
		}

		// Chats involving a specific contact
		if contactJID != "" {
			chats, err := messageStore.GetContactChats(contactJID, limit, offset)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(chats)
			return
		}

		// General chat listing
		chats, err := messageStore.ListChats(query, limit, offset, includeLastMessage, sortBy)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(chats)
	})

	// GET /api/messages - List messages with filters
	http.HandleFunc("/api/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		q := r.URL.Query()
		chatJID := q.Get("chat_jid")
		limitStr := q.Get("limit")
		pageStr := q.Get("page")
		after := q.Get("after")
		before := q.Get("before")
		sender := q.Get("sender")
		query := q.Get("query")
		sortBy := q.Get("sort_by")
		messageID := q.Get("message_id")
		jid := q.Get("jid")

		limit := 20
		if limitStr != "" {
			if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
				limit = v
			}
		}
		page := 0
		if pageStr != "" {
			if v, err := strconv.Atoi(pageStr); err == nil && v >= 0 {
				page = v
			}
		}
		if sortBy == "" {
			sortBy = "newest"
		}
		offset := page * limit

		// Message context lookup
		if messageID != "" {
			beforeCount := 5
			afterCount := 5
			if v := q.Get("before_count"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 {
					beforeCount = n
				}
			}
			if v := q.Get("after_count"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 {
					afterCount = n
				}
			}
			ctx, err := messageStore.GetMessageContext(messageID, beforeCount, afterCount)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
				return
			}
			if ctx == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "message not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(ctx)
			return
		}

		// Last interaction with a contact
		if jid != "" {
			msg, err := messageStore.GetLastInteraction(jid)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
				return
			}
			if msg == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "no messages found"})
				return
			}
			_ = json.NewEncoder(w).Encode(msg)
			return
		}

		// General message listing
		messages, err := messageStore.ListMessages(chatJID, sender, query, after, before, sortBy, limit, offset)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(messages)
	})

	// GET /api/contacts - Search contacts by name or phone
	http.HandleFunc("/api/contacts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		query := r.URL.Query().Get("query")
		if query == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "query parameter is required"})
			return
		}

		contacts, err := messageStore.SearchContacts(query)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(contacts)
	})

	// GET /api/sender-name - Resolve sender JID to display name
	http.HandleFunc("/api/sender-name", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		jid := r.URL.Query().Get("jid")
		if jid == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "jid parameter is required"})
			return
		}

		name, err := messageStore.GetSenderName(jid)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": name})
	})

	// Start the server with proper timeouts
	serverAddr := fmt.Sprintf(":%d", port)
	fmt.Printf("Starting REST API server on %s...\n", serverAddr)

	// Create server with timeouts for stability
	server := &http.Server{
		Addr:         serverAddr,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second, // Longer for media downloads
		IdleTimeout:  120 * time.Second,
	}

	// Run server in a goroutine so it doesn't block
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("REST API server error: %v\n", err)
		}
	}()
}

func main() {
	// Set up logger with DEBUG level for more detailed logging
	logger := waLog.Stdout("Client", "DEBUG", true)
	logger.Infof("Starting WhatsApp client...")

	if forwardSelfMessages {
		logger.Infof("FORWARD_SELF enabled: forwarding self messages to webhook")
	} else {
		logger.Infof("FORWARD_SELF disabled: self messages will NOT be forwarded")
	}

	// Create database connection for storing session data
	dbLog := waLog.Stdout("Database", "INFO", true)

	// Create directory for database if it doesn't exist
	if err := os.MkdirAll("store", 0755); err != nil {
		logger.Errorf("Failed to create store directory: %v", err)
		return
	}

	container, err := sqlstore.New(context.Background(), "sqlite3", "file:store/whatsapp.db?_foreign_keys=on", dbLog)
	if err != nil {
		logger.Errorf("Failed to connect to database: %v", err)
		return
	}

	// Get device store - This contains session information
	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		if err == sql.ErrNoRows {
			// No device exists, create one
			deviceStore = container.NewDevice()
			logger.Infof("Created new device")
		} else {
			logger.Errorf("Failed to get device: %v", err)
			return
		}
	}

	// Create client instance
	client := whatsmeow.NewClient(deviceStore, logger)
	if client == nil {
		logger.Errorf("Failed to create WhatsApp client")
		return
	}

	// Initialize message store
	messageStore, err := NewMessageStore()
	if err != nil {
		logger.Errorf("Failed to initialize message store: %v", err)
		return
	}
	defer func() { _ = messageStore.Close() }()

	if err := messageStore.MigrateLegacyLIDChatsToPhoneJIDs("store/whatsapp.db", logger); err != nil {
		logger.Errorf("Failed to migrate legacy LID chat rows: %v", err)
		return
	}

	// Channel to signal reconnection needs
	reconnectChan := make(chan bool, 1)

	// Setup event handling for messages and history sync
	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			// Process regular messages
			handleMessage(client, messageStore, v, logger)

		case *events.HistorySync:
			// Process history sync events
			handleHistorySync(client, messageStore, v, logger)

		case *events.Connected:
			logger.Infof("✓ Successfully connected to WhatsApp servers")

		case *events.LoggedOut:
			logger.Warnf("⚠️  Device logged out, please scan QR code to log in again")

		case *events.Disconnected:
			logger.Warnf("⚠️  Disconnected from WhatsApp servers, will attempt reconnection...")
			// Signal reconnection needed
			select {
			case reconnectChan <- true:
			default:
				// Channel already has a reconnect signal
			}

		case *events.ConnectFailure:
			logger.Errorf("❌ Connection failure: %v", v.Reason)
			// Signal reconnection needed
			select {
			case reconnectChan <- true:
			default:
			}

		case *events.StreamError:
			logger.Errorf("❌ Stream error: %v", v.Code)
			// Signal reconnection needed
			select {
			case reconnectChan <- true:
			default:
			}

		case *events.ClientOutdated:
			logger.Errorf("❌ Client outdated - please update whatsmeow library")
		}
	})

	// Create channel to track connection success
	connected := make(chan bool, 1)

	// Add connection retry logic
	maxRetries := 3
	var connErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		logger.Infof("Connection attempt %d/%d...", attempt, maxRetries)

		// Connect to WhatsApp
		if client.Store.ID == nil {
			// No ID stored, this is a new client, need to pair with phone
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			qrChan, connErr := client.GetQRChannel(ctx)
			if connErr != nil {
				logger.Errorf("Failed to get QR channel: %v", connErr)
				if attempt == maxRetries {
					return
				}
				time.Sleep(5 * time.Second)
				continue
			}

			connErr = client.Connect()
			if connErr != nil {
				logger.Errorf("Failed to connect (attempt %d): %v", attempt, connErr)
				if attempt == maxRetries {
					return
				}
				time.Sleep(5 * time.Second)
				continue
			}

			// Print QR code for pairing with phone
			qrCodeShown := false
			for evt := range qrChan {
				if evt.Event == "code" {
					if !qrCodeShown {
						fmt.Println("\nScan this QR code with your WhatsApp app:")
						qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
						fmt.Println("\nWaiting for QR code scan...")
						qrCodeShown = true
					}
				} else if evt.Event == "success" {
					connected <- true
					break
				} else if evt.Event == "timeout" {
					logger.Warnf("QR code timed out")
					break
				}
			}

			// Wait for connection with timeout
			select {
			case <-connected:
				fmt.Println("\nSuccessfully connected and authenticated!")
				goto connectionSuccess
			case <-ctx.Done():
				logger.Errorf("Timeout waiting for QR code scan (attempt %d)", attempt)
				client.Disconnect()
				if attempt == maxRetries {
					return
				}
				time.Sleep(10 * time.Second)
				continue
			}
		} else {
			// Already logged in, just connect
			connErr = client.Connect()
			if connErr != nil {
				logger.Errorf("Failed to connect (attempt %d): %v", attempt, connErr)
				if attempt == maxRetries {
					return
				}
				time.Sleep(5 * time.Second)
				continue
			}
			connected <- true
			break
		}
	}

connectionSuccess:

	// Wait a moment for connection to stabilize
	time.Sleep(2 * time.Second)

	if !client.IsConnected() {
		logger.Errorf("Failed to establish stable connection")
		return
	}

	fmt.Println("\n✓ Connected to WhatsApp! Type 'help' for commands.")

	// Start REST API server
	port := 8080
	if p := os.Getenv("WHATSAPP_BRIDGE_PORT"); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil || v < 1 || v > 65535 {
			logger.Errorf("Invalid WHATSAPP_BRIDGE_PORT=%q, must be 1-65535", p)
			return
		}
		port = v
	}
	startRESTServer(client, messageStore, port)

	// Create a channel to keep the main goroutine alive
	exitChan := make(chan os.Signal, 1)
	signal.Notify(exitChan, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("REST server is running. Press Ctrl+C to disconnect and exit.")

	// Start reconnection handler goroutine
	go func() {
		reconnectBackoff := time.Second * 5
		maxBackoff := time.Minute * 5

		for {
			select {
			case <-reconnectChan:
				logger.Infof("🔄 Attempting to reconnect...")

				// Wait before reconnecting
				time.Sleep(reconnectBackoff)

				// Try to reconnect
				if !client.IsConnected() {
					err := client.Connect()
					if err != nil {
						logger.Errorf("❌ Reconnection failed: %v", err)
						// Increase backoff for next attempt
						reconnectBackoff = reconnectBackoff * 2
						if reconnectBackoff > maxBackoff {
							reconnectBackoff = maxBackoff
						}
						// Signal another reconnection attempt
						select {
						case reconnectChan <- true:
						default:
						}
					} else {
						logger.Infof("✓ Reconnected successfully")
						// Reset backoff on successful connection
						reconnectBackoff = time.Second * 5
					}
				} else {
					logger.Infof("Already connected, skipping reconnection")
					reconnectBackoff = time.Second * 5
				}

			case <-exitChan:
				return
			}
		}
	}()

	// Wait for termination signal
	<-exitChan

	fmt.Println("Disconnecting...")
	// Disconnect client
	client.Disconnect()
}

// GetChatName determines the appropriate name for a chat based on JID and other info
func GetChatName(client *whatsmeow.Client, messageStore *MessageStore, jid types.JID, chatJID string, conversation interface{}, sender string, logger waLog.Logger) string {
	// First, check if chat already exists in database with a name
	var existingName string
	err := messageStore.db.QueryRow("SELECT name FROM chats WHERE jid = ?", chatJID).Scan(&existingName)
	if err == nil && existingName != "" {
		// Chat exists with a name, use that
		logger.Infof("Using existing chat name for %s: %s", chatJID, existingName)
		return existingName
	}

	// Need to determine chat name
	var name string

	if jid.Server == "g.us" {
		// This is a group chat
		logger.Infof("Getting name for group: %s", chatJID)

		// Use conversation data if provided (from history sync)
		if conversation != nil {
			// Extract name from conversation if available
			// This uses type assertions to handle different possible types
			var displayName, convName *string
			// Try to extract the fields we care about regardless of the exact type
			v := reflect.ValueOf(conversation)
			if v.Kind() == reflect.Ptr && !v.IsNil() {
				v = v.Elem()

				// Try to find DisplayName field
				if displayNameField := v.FieldByName("DisplayName"); displayNameField.IsValid() && displayNameField.Kind() == reflect.Ptr && !displayNameField.IsNil() {
					dn := displayNameField.Elem().String()
					displayName = &dn
				}

				// Try to find Name field
				if nameField := v.FieldByName("Name"); nameField.IsValid() && nameField.Kind() == reflect.Ptr && !nameField.IsNil() {
					n := nameField.Elem().String()
					convName = &n
				}
			}

			// Use the name we found
			if displayName != nil && *displayName != "" {
				name = *displayName
			} else if convName != nil && *convName != "" {
				name = *convName
			}
		}

		// If we didn't get a name, try group info
		if name == "" {
			groupInfo, err := client.GetGroupInfo(context.Background(), jid)
			if err == nil && groupInfo.Name != "" {
				name = groupInfo.Name
			} else {
				// Fallback name for groups
				name = fmt.Sprintf("Group %s", jid.User)
			}
		}

		logger.Infof("Using group name: %s", name)
	} else {
		// This is an individual contact
		logger.Infof("Getting name for contact: %s", chatJID)

		// Just use contact info (full name)
		contact, err := client.Store.Contacts.GetContact(context.Background(), jid)
		if err == nil && contact.FullName != "" {
			name = contact.FullName
		} else if sender != "" {
			// Fallback to sender
			name = sender
		} else {
			// Last fallback to JID
			name = jid.User
		}

		logger.Infof("Using contact name: %s", name)
	}

	return name
}

// Handle history sync events
func handleHistorySync(client *whatsmeow.Client, messageStore *MessageStore, historySync *events.HistorySync, logger waLog.Logger) {
	fmt.Printf("Received history sync event with %d conversations\n", len(historySync.Data.Conversations))

	syncedCount := 0
	for _, conversation := range historySync.Data.Conversations {
		// Parse JID from the conversation
		if conversation.ID == nil {
			continue
		}

		rawChatJID := *conversation.ID

		// Try to parse the JID
		jid, err := types.ParseJID(rawChatJID)
		if err != nil {
			logger.Warnf("Failed to parse JID %s: %v", rawChatJID, err)
			continue
		}

		// Resolve LID-based chats to phone-based JIDs.
		// History sync doesn't carry SenderAlt, so rely on the
		// LID store mapping populated during live message handling.
		resolved := resolveLIDChat(client, jid, types.EmptyJID, types.EmptyJID, false)
		chatJID := resolved.String()

		// Get appropriate chat name by passing the history sync conversation directly
		name := GetChatName(client, messageStore, resolved, chatJID, conversation, "", logger)

		// Process messages
		messages := conversation.Messages
		if len(messages) > 0 {
			// Update chat with latest message timestamp
			latestMsg := messages[0]
			if latestMsg == nil || latestMsg.Message == nil {
				continue
			}

			// Get timestamp from message info
			ts := latestMsg.Message.GetMessageTimestamp()
			if ts == 0 {
				continue
			}
			timestamp := time.Unix(int64(ts), 0)

			_ = messageStore.StoreChat(chatJID, name, timestamp)

			// Store messages
			for _, msg := range messages {
				if msg == nil || msg.Message == nil {
					continue
				}

				// Extract text content
				var content string
				if msg.Message.Message != nil {
					if conv := msg.Message.Message.GetConversation(); conv != "" {
						content = conv
					} else if ext := msg.Message.Message.GetExtendedTextMessage(); ext != nil {
						content = ext.GetText()
					}
				}

				// Extract media info - use message timestamp for consistent filenames
				var mediaType, filename, url string
				var mediaKey, fileSHA256, fileEncSHA256 []byte
				var fileLength uint64

				if msg.Message.Message != nil {
					mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength = extractMediaInfo(msg.Message.Message, timestamp)
				}

				// Log the message content for debugging
				logger.Infof("Message content: %v, Media Type: %v", content, mediaType)

				// Skip messages with no content and no media
				if content == "" && mediaType == "" {
					continue
				}

				// Determine sender
				var sender string
				isFromMe := false
				if msg.Message.Key != nil {
					if msg.Message.Key.FromMe != nil {
						isFromMe = *msg.Message.Key.FromMe
					}
					if !isFromMe && msg.Message.Key.Participant != nil && *msg.Message.Key.Participant != "" {
						sender = *msg.Message.Key.Participant
					} else if isFromMe {
						sender = client.Store.ID.User
					} else {
						sender = jid.User
					}
				} else {
					sender = jid.User
				}

				// Store message
				msgID := ""
				if msg.Message.Key != nil && msg.Message.Key.ID != nil {
					msgID = *msg.Message.Key.ID
				}

				// Get message timestamp
				ts := msg.Message.GetMessageTimestamp()
				if ts == 0 {
					continue
				}
				msgTimestamp := time.Unix(int64(ts), 0)

				err = messageStore.StoreMessage(
					msgID,
					chatJID,
					sender,
					content,
					msgTimestamp,
					isFromMe,
					mediaType,
					filename,
					url,
					mediaKey,
					fileSHA256,
					fileEncSHA256,
					fileLength,
				)
				if err != nil {
					logger.Warnf("Failed to store history message: %v", err)
				} else {
					syncedCount++
					// Log successful message storage
					if mediaType != "" {
						logger.Infof("Stored message: [%s] %s -> %s: [%s: %s] %s",
							msgTimestamp.Format("2006-01-02 15:04:05"), sender, chatJID, mediaType, filename, content)
					} else {
						logger.Infof("Stored message: [%s] %s -> %s: %s",
							msgTimestamp.Format("2006-01-02 15:04:05"), sender, chatJID, content)
					}
				}
			}
		}
	}

	fmt.Printf("History sync complete. Stored %d messages.\n", syncedCount)
}

// analyzeOggOpus tries to extract duration and generate a simple waveform from an Ogg Opus file
func analyzeOggOpus(data []byte) (duration uint32, waveform []byte, err error) {
	// Try to detect if this is a valid Ogg file by checking for the "OggS" signature
	// at the beginning of the file
	if len(data) < 4 || string(data[0:4]) != "OggS" {
		return 0, nil, fmt.Errorf("not a valid Ogg file (missing OggS signature)")
	}

	// Parse Ogg pages to find the last page with a valid granule position
	var lastGranule uint64
	var sampleRate uint32 = 48000 // Default Opus sample rate
	var preSkip uint16 = 0
	var foundOpusHead bool

	// Scan through the file looking for Ogg pages
	for i := 0; i < len(data); {
		// Check if we have enough data to read Ogg page header
		if i+27 >= len(data) {
			break
		}

		// Verify Ogg page signature
		if string(data[i:i+4]) != "OggS" {
			// Skip until next potential page
			i++
			continue
		}

		// Extract header fields
		granulePos := binary.LittleEndian.Uint64(data[i+6 : i+14])
		pageSeqNum := binary.LittleEndian.Uint32(data[i+18 : i+22])
		numSegments := int(data[i+26])

		// Extract segment table
		if i+27+numSegments >= len(data) {
			break
		}
		segmentTable := data[i+27 : i+27+numSegments]

		// Calculate page size
		pageSize := 27 + numSegments
		for _, segLen := range segmentTable {
			pageSize += int(segLen)
		}

		// Check if we're looking at an OpusHead packet (should be in first few pages)
		if !foundOpusHead && pageSeqNum <= 1 {
			// Look for "OpusHead" marker in this page
			pageData := data[i : i+pageSize]
			headPos := bytes.Index(pageData, []byte("OpusHead"))
			if headPos >= 0 && headPos+12 < len(pageData) {
				// Found OpusHead, extract sample rate and pre-skip
				// OpusHead format: Magic(8) + Version(1) + Channels(1) + PreSkip(2) + SampleRate(4) + ...
				headPos += 8 // Skip "OpusHead" marker
				// PreSkip is 2 bytes at offset 10
				if headPos+12 <= len(pageData) {
					preSkip = binary.LittleEndian.Uint16(pageData[headPos+10 : headPos+12])
					sampleRate = binary.LittleEndian.Uint32(pageData[headPos+12 : headPos+16])
					foundOpusHead = true
					fmt.Printf("Found OpusHead: sampleRate=%d, preSkip=%d\n", sampleRate, preSkip)
				}
			}
		}

		// Keep track of last valid granule position
		if granulePos != 0 {
			lastGranule = granulePos
		}

		// Move to next page
		i += pageSize
	}

	if !foundOpusHead {
		fmt.Println("Warning: OpusHead not found, using default values")
	}

	// Calculate duration based on granule position
	if lastGranule > 0 {
		// Formula for duration: (lastGranule - preSkip) / sampleRate
		durationSeconds := float64(lastGranule-uint64(preSkip)) / float64(sampleRate)
		duration = uint32(math.Ceil(durationSeconds))
		fmt.Printf("Calculated Opus duration from granule: %f seconds (lastGranule=%d)\n",
			durationSeconds, lastGranule)
	} else {
		// Fallback to rough estimation if granule position not found
		fmt.Println("Warning: No valid granule position found, using estimation")
		durationEstimate := float64(len(data)) / 2000.0 // Very rough approximation
		duration = uint32(durationEstimate)
	}

	// Make sure we have a reasonable duration (at least 1 second, at most 300 seconds)
	if duration < 1 {
		duration = 1
	} else if duration > 300 {
		duration = 300
	}

	// Generate waveform
	waveform = placeholderWaveform(duration)

	fmt.Printf("Ogg Opus analysis: size=%d bytes, calculated duration=%d sec, waveform=%d bytes\n",
		len(data), duration, len(waveform))

	return duration, waveform, nil
}

// min returns the smaller of x or y
func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

// placeholderWaveform generates a synthetic waveform for WhatsApp voice messages
// that appears natural with some variability based on the duration
func placeholderWaveform(duration uint32) []byte {
	// WhatsApp expects a 64-byte waveform for voice messages
	const waveformLength = 64
	waveform := make([]byte, waveformLength)

	// Seed the random number generator for consistent results with the same duration
	rand.Seed(int64(duration))

	// Create a more natural looking waveform with some patterns and variability
	// rather than completely random values

	// Base amplitude and frequency - longer messages get faster frequency
	baseAmplitude := 35.0
	frequencyFactor := float64(min(int(duration), 120)) / 30.0

	for i := range waveform {
		// Position in the waveform (normalized 0-1)
		pos := float64(i) / float64(waveformLength)

		// Create a wave pattern with some randomness
		// Use multiple sine waves of different frequencies for more natural look
		val := baseAmplitude * math.Sin(pos*math.Pi*frequencyFactor*8)
		val += (baseAmplitude / 2) * math.Sin(pos*math.Pi*frequencyFactor*16)

		// Add some randomness to make it look more natural
		val += (rand.Float64() - 0.5) * 15

		// Add some fade-in and fade-out effects
		fadeInOut := math.Sin(pos * math.Pi)
		val = val * (0.7 + 0.3*fadeInOut)

		// Center around 50 (typical voice baseline)
		val = val + 50

		// Ensure values stay within WhatsApp's expected range (0-100)
		if val < 0 {
			val = 0
		} else if val > 100 {
			val = 100
		}

		waveform[i] = byte(val)
	}

	return waveform
}
