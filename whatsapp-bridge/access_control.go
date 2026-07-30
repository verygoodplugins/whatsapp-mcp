package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrMCPChatNotFound       = errors.New("MCP access chat not found")
	ErrMCPDuplicateChatJID   = errors.New("duplicate chat JID in MCP access update")
	ErrMCPPermissionConflict = errors.New("MCP access permissions changed since they were loaded")
	ErrMCPSettingsConflict   = errors.New("MCP access settings changed since they were loaded")
)

// MCPChatPermissionUpdate is the desired MCP access state for one chat.
//
// Read windows are maintained by the server:
//   - false -> true derives a conservative endpoint from server time;
//   - true -> true preserves the existing endpoint;
//   - true -> false clears the endpoint.
//
// ReadNew and ReadHistory are intentionally independent. This lets an operator
// expose only messages arriving after access was granted, only messages dated
// before the historical cutoff, both, or neither.
type MCPChatPermissionUpdate struct {
	ChatJID          string `json:"chat_jid"`
	ReadNew          bool   `json:"read_new"`
	ReadHistory      bool   `json:"read_history"`
	CanSend          bool   `json:"can_send"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// MCPAdminChat contains only the metadata needed by the local access-control
// UI. It intentionally has no message-content or last-message-preview fields.
type MCPAdminChat struct {
	JID                      string  `json:"jid"`
	Name                     string  `json:"name"`
	LastMessageTime          *string `json:"last_message_time"`
	IsGroup                  bool    `json:"is_group"`
	ReadNew                  bool    `json:"read_new"`
	ReadHistory              bool    `json:"read_history"`
	CanSend                  bool    `json:"can_send"`
	ReadNewSinceUnix         *int64  `json:"read_new_since_unix"`
	ReadHistoryThroughUnix   *int64  `json:"read_history_through_unix"`
	PermissionsUpdatedAtUnix *int64  `json:"permissions_updated_at_unix"`
	PermissionsRevision      int64   `json:"permissions_revision"`
}

// MCPAccessSettings contains global MCP capabilities that cannot be expressed
// as permissions on a chat that already exists.
type MCPAccessSettings struct {
	AllowStartNewConversations bool  `json:"allow_start_new_conversations"`
	UpdatedAtUnix              int64 `json:"updated_at_unix"`
	Revision                   int64 `json:"revision"`
}

// ensureMCPAccessSchema creates the opt-in MCP access-control table.
//
// The table's existence means enforcement is active. A missing permission row
// is therefore an explicit deny for both reading and sending.
func ensureMCPAccessSchema(db *sql.DB) error {
	if db == nil {
		return errors.New("cannot initialize MCP access schema with nil database")
	}

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS mcp_chat_permissions (
			chat_jid TEXT PRIMARY KEY,
			read_new_since_unix INTEGER,
			read_history_through_unix INTEGER,
			can_send INTEGER NOT NULL DEFAULT 0 CHECK (can_send IN (0, 1)),
			updated_at_unix INTEGER NOT NULL,
			revision INTEGER NOT NULL DEFAULT 1,
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
				ON UPDATE CASCADE
				ON DELETE CASCADE
		);
	`)
	if err != nil {
		return fmt.Errorf("create MCP chat permissions table: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS mcp_access_settings (
			singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
			allow_start_new_conversations INTEGER NOT NULL DEFAULT 0
				CHECK (allow_start_new_conversations IN (0, 1)),
			updated_at_unix INTEGER NOT NULL DEFAULT 0,
			revision INTEGER NOT NULL DEFAULT 1
		);
		INSERT OR IGNORE INTO mcp_access_settings (
			singleton_id,
			allow_start_new_conversations,
			updated_at_unix,
			revision
		) VALUES (1, 0, 0, 1);
	`); err != nil {
		return fmt.Errorf("create MCP access settings table: %w", err)
	}
	if err := ensureColumn(
		db,
		"mcp_chat_permissions",
		"revision",
		"INTEGER NOT NULL DEFAULT 1",
	); err != nil {
		return fmt.Errorf("ensure MCP chat permission revision: %w", err)
	}
	return nil
}

// GetMCPAccessSettings returns the singleton global MCP access settings.
func (store *MessageStore) GetMCPAccessSettings() (MCPAccessSettings, error) {
	var settings MCPAccessSettings
	err := store.db.QueryRow(`
		SELECT allow_start_new_conversations, updated_at_unix, revision
		FROM mcp_access_settings
		WHERE singleton_id = 1
	`).Scan(
		&settings.AllowStartNewConversations,
		&settings.UpdatedAtUnix,
		&settings.Revision,
	)
	if err != nil {
		return MCPAccessSettings{}, fmt.Errorf("load MCP access settings: %w", err)
	}
	return settings, nil
}

// UpdateMCPAccessSettings applies global settings with optimistic concurrency.
func (store *MessageStore) UpdateMCPAccessSettings(
	allowStartNewConversations bool,
	expectedRevision int64,
	now time.Time,
) error {
	result, err := store.db.Exec(`
		UPDATE mcp_access_settings
		SET
			allow_start_new_conversations = ?,
			updated_at_unix = ?,
			revision = revision + 1
		WHERE singleton_id = 1 AND revision = ?
	`, allowStartNewConversations, now.Unix(), expectedRevision)
	if err != nil {
		return fmt.Errorf("update MCP access settings: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect MCP access settings update: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf(
			"%w: expected revision %d",
			ErrMCPSettingsConflict,
			expectedRevision,
		)
	}
	return nil
}

// CanMCPStartNewConversations reports whether the MCP may initiate a direct
// chat that does not exist in the local chat store.
func (store *MessageStore) CanMCPStartNewConversations() (bool, error) {
	settings, err := store.GetMCPAccessSettings()
	if err != nil {
		return false, err
	}
	return settings.AllowStartNewConversations, nil
}

// MCPChatExists checks the exact canonical JID without exposing chat metadata.
func (store *MessageStore) MCPChatExists(chatJID string) (bool, error) {
	var exists bool
	err := store.db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM chats WHERE jid = ?)",
		chatJID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check whether MCP chat %s exists: %w", chatJID, err)
	}
	return exists, nil
}

// GrantMCPNewConversationFullAccess records a successfully initiated
// conversation with all three permissions. It uses the same conservative
// one-second read boundaries as an access grant made in the admin panel.
func (store *MessageStore) GrantMCPNewConversationFullAccess(chatJID string, now time.Time) error {
	tx, err := store.db.Begin()
	if err != nil {
		return fmt.Errorf("begin MCP new conversation grant: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	nowUnix := now.Unix()

	if _, err := tx.Exec(`
		INSERT INTO chats (jid, name, last_message_time)
		VALUES (?, '', ?)
		ON CONFLICT(jid) DO NOTHING
	`, chatJID, now.UTC()); err != nil {
		return fmt.Errorf("ensure new MCP conversation chat %s: %w", chatJID, err)
	}
	if _, err := tx.Exec(`
		INSERT INTO mcp_chat_permissions (
			chat_jid,
			read_new_since_unix,
			read_history_through_unix,
			can_send,
			updated_at_unix,
			revision
		)
		VALUES (?, ?, ?, 1, ?, 1)
		ON CONFLICT(chat_jid) DO NOTHING
	`, chatJID, nowUnix+1, nowUnix-1, nowUnix); err != nil {
		return fmt.Errorf("grant full access to new MCP conversation %s: %w", chatJID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit MCP new conversation grant: %w", err)
	}
	return nil
}

// ListMCPAccessChats returns chat metadata and effective MCP permissions for
// the local administration UI. Message content is deliberately never joined.
func (store *MessageStore) ListMCPAccessChats() ([]MCPAdminChat, error) {
	rows, err := store.db.Query(`
		SELECT
			chats.jid,
			COALESCE(chats.name, ''),
			CAST(chats.last_message_time AS TEXT),
			permissions.read_new_since_unix,
			permissions.read_history_through_unix,
			COALESCE(permissions.can_send, 0),
			permissions.updated_at_unix,
			COALESCE(permissions.revision, 0)
		FROM chats
		LEFT JOIN mcp_chat_permissions AS permissions
			ON permissions.chat_jid = chats.jid
		ORDER BY
			chats.last_message_time IS NULL,
			chats.last_message_time DESC,
			COALESCE(chats.name, '') COLLATE NOCASE,
			chats.jid
	`)
	if err != nil {
		return nil, fmt.Errorf("list chats for MCP access administration: %w", err)
	}
	defer func() { _ = rows.Close() }()

	chats := make([]MCPAdminChat, 0)
	for rows.Next() {
		var (
			chat                 MCPAdminChat
			lastMessageTime      sql.NullString
			readNewSince         sql.NullInt64
			readHistoryThrough   sql.NullInt64
			canSend              bool
			permissionsUpdatedAt sql.NullInt64
		)
		if err := rows.Scan(
			&chat.JID,
			&chat.Name,
			&lastMessageTime,
			&readNewSince,
			&readHistoryThrough,
			&canSend,
			&permissionsUpdatedAt,
			&chat.PermissionsRevision,
		); err != nil {
			return nil, fmt.Errorf("scan chat for MCP access administration: %w", err)
		}

		chat.LastMessageTime = nullStringPointer(lastMessageTime)
		chat.IsGroup = strings.HasSuffix(chat.JID, "@g.us")
		chat.ReadNew = readNewSince.Valid
		chat.ReadHistory = readHistoryThrough.Valid
		chat.CanSend = canSend
		chat.ReadNewSinceUnix = nullInt64Pointer(readNewSince)
		chat.ReadHistoryThroughUnix = nullInt64Pointer(readHistoryThrough)
		chat.PermissionsUpdatedAtUnix = nullInt64Pointer(permissionsUpdatedAt)
		chats = append(chats, chat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chats for MCP access administration: %w", err)
	}

	return chats, nil
}

// UpdateMCPChatPermissions atomically applies a batch of desired permission
// states. Every JID must already exist exactly in chats; otherwise the entire
// batch is rolled back.
func (store *MessageStore) UpdateMCPChatPermissions(updates []MCPChatPermissionUpdate, now time.Time) error {
	if len(updates) == 0 {
		return nil
	}

	tx, err := store.db.Begin()
	if err != nil {
		return fmt.Errorf("begin MCP chat permission update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	seen := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		if update.ChatJID == "" {
			return fmt.Errorf("%w: empty JID", ErrMCPChatNotFound)
		}
		if _, duplicate := seen[update.ChatJID]; duplicate {
			return fmt.Errorf("%w: %s", ErrMCPDuplicateChatJID, update.ChatJID)
		}
		seen[update.ChatJID] = struct{}{}

		var exists int
		err := tx.QueryRow("SELECT 1 FROM chats WHERE jid = ?", update.ChatJID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrMCPChatNotFound, update.ChatJID)
		}
		if err != nil {
			return fmt.Errorf("validate MCP access chat %s: %w", update.ChatJID, err)
		}
	}

	nowUnix := now.Unix()
	for _, update := range updates {
		var (
			existingReadNew     sql.NullInt64
			existingReadHistory sql.NullInt64
			existingRevision    int64
		)
		err := tx.QueryRow(`
			SELECT read_new_since_unix, read_history_through_unix, revision
			FROM mcp_chat_permissions
			WHERE chat_jid = ?
		`, update.ChatJID).Scan(&existingReadNew, &existingReadHistory, &existingRevision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("load existing MCP permissions for %s: %w", update.ChatJID, err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			existingReadNew = sql.NullInt64{}
			existingReadHistory = sql.NullInt64{}
			existingRevision = 0
		}
		if update.ExpectedRevision != existingRevision {
			return fmt.Errorf(
				"%w: %s expected revision %d, current revision %d",
				ErrMCPPermissionConflict,
				update.ChatJID,
				update.ExpectedRevision,
				existingRevision,
			)
		}

		// WhatsApp timestamps commonly have one-second precision. Leave the
		// ambiguous grant second out of both windows so a message from just
		// before/after the click cannot cross the privacy boundary.
		readNewSince := transitionMCPReadEndpoint(update.ReadNew, existingReadNew, nowUnix+1)
		readHistoryThrough := transitionMCPReadEndpoint(update.ReadHistory, existingReadHistory, nowUnix-1)
		nextRevision := existingRevision + 1

		_, err = tx.Exec(`
			INSERT INTO mcp_chat_permissions (
				chat_jid,
				read_new_since_unix,
				read_history_through_unix,
				can_send,
				updated_at_unix,
				revision
			)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(chat_jid) DO UPDATE SET
				read_new_since_unix = excluded.read_new_since_unix,
				read_history_through_unix = excluded.read_history_through_unix,
				can_send = excluded.can_send,
				updated_at_unix = excluded.updated_at_unix,
				revision = excluded.revision
		`,
			update.ChatJID,
			nullInt64Value(readNewSince),
			nullInt64Value(readHistoryThrough),
			update.CanSend,
			nowUnix,
			nextRevision,
		)
		if err != nil {
			return fmt.Errorf("store MCP permissions for %s: %w", update.ChatJID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit MCP chat permission update: %w", err)
	}
	return nil
}

// CanMCPSend reports whether the MCP may send to an exact chat JID. Missing
// rows deny by default.
func (store *MessageStore) CanMCPSend(chatJID string) (bool, error) {
	var canSend bool
	err := store.db.QueryRow(
		"SELECT can_send FROM mcp_chat_permissions WHERE chat_jid = ?",
		chatJID,
	).Scan(&canSend)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load MCP send permission for %s: %w", chatJID, err)
	}
	return canSend, nil
}

// CanMCPReadMessage reports whether a stored message falls within either of
// the exact chat's independent read windows. Unknown messages and missing
// permission rows deny by default.
func (store *MessageStore) CanMCPReadMessage(messageID, chatJID string) (bool, error) {
	var (
		messageUnix        sql.NullInt64
		readNewSince       sql.NullInt64
		readHistoryThrough sql.NullInt64
	)
	err := store.db.QueryRow(`
		SELECT
			unixepoch(messages.timestamp),
			permissions.read_new_since_unix,
			permissions.read_history_through_unix
		FROM messages
		LEFT JOIN mcp_chat_permissions AS permissions
			ON permissions.chat_jid = messages.chat_jid
		WHERE messages.id = ? AND messages.chat_jid = ?
	`, messageID, chatJID).Scan(&messageUnix, &readNewSince, &readHistoryThrough)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load MCP read permission for message %s in %s: %w", messageID, chatJID, err)
	}
	if !messageUnix.Valid {
		return false, fmt.Errorf("message %s in %s has an invalid timestamp", messageID, chatJID)
	}

	return mcpTimestampWithinReadWindow(messageUnix.Int64, readNewSince, readHistoryThrough), nil
}

// CanMCPReadTimestamp applies a chat's read windows to an event timestamp.
// It is available for callers that already have a canonical chat JID but no
// stored message ID.
func (store *MessageStore) CanMCPReadTimestamp(chatJID string, timestamp time.Time) (bool, error) {
	var readNewSince, readHistoryThrough sql.NullInt64
	err := store.db.QueryRow(`
		SELECT read_new_since_unix, read_history_through_unix
		FROM mcp_chat_permissions
		WHERE chat_jid = ?
	`, chatJID).Scan(&readNewSince, &readHistoryThrough)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load MCP read windows for %s: %w", chatJID, err)
	}

	return mcpTimestampWithinReadWindow(timestamp.Unix(), readNewSince, readHistoryThrough), nil
}

func mcpTimestampWithinReadWindow(messageUnix int64, readNewSince, readHistoryThrough sql.NullInt64) bool {
	return (readNewSince.Valid && messageUnix >= readNewSince.Int64) ||
		(readHistoryThrough.Valid && messageUnix <= readHistoryThrough.Int64)
}

func transitionMCPReadEndpoint(enabled bool, existing sql.NullInt64, nowUnix int64) sql.NullInt64 {
	if !enabled {
		return sql.NullInt64{}
	}
	if existing.Valid {
		return existing
	}
	return sql.NullInt64{Int64: nowUnix, Valid: true}
}

func nullInt64Value(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}
