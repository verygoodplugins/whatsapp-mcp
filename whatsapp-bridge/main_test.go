package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// --- Test helpers ---

// mockLIDStore implements store.LIDStore with a simple in-memory map.
type mockLIDStore struct {
	store.NoopStore
	pnByLID map[types.JID]types.JID
}

func (m *mockLIDStore) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	if pn, ok := m.pnByLID[lid]; ok {
		return pn, nil
	}
	return types.EmptyJID, nil
}

func newTestClient(lidStore store.LIDStore) *whatsmeow.Client {
	noop := &store.NoopStore{}
	return &whatsmeow.Client{
		Store: &store.Device{
			LIDs:     lidStore,
			Contacts: noop,
		},
	}
}

// newTestClientWithSelf builds a test client with the user's own phone JID set
// on Store.ID, which the production code uses as the sender-alt hint for
// outgoing messages. Tests that exercise sender resolution for outgoing
// messages must use this constructor.
func newTestClientWithSelf(lidStore store.LIDStore, selfPhone types.JID) *whatsmeow.Client {
	c := newTestClient(lidStore)
	pn := selfPhone.ToNonAD()
	c.Store.ID = &pn
	return c
}

// querySender returns the sender column for the first message stored under a
// chat JID, or empty string if none.
func querySender(ms *MessageStore, chatJID string) string {
	var s string
	_ = ms.db.QueryRow("SELECT sender FROM messages WHERE chat_jid = ? LIMIT 1", chatJID).Scan(&s)
	return s
}

func newTestMessageStore(t *testing.T) *MessageStore {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE chats (
			jid TEXT PRIMARY KEY,
			name TEXT,
			last_message_time TIMESTAMP
		);
		CREATE TABLE messages (
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
		CREATE TABLE calls (
			call_id TEXT,
			chat_jid TEXT,
			from_jid TEXT,
			timestamp TIMESTAMP,
			is_from_me BOOLEAN,
			call_type TEXT,
			is_group BOOLEAN,
			result TEXT,
			duration_sec INTEGER,
			ended_at TIMESTAMP,
			reason TEXT,
			PRIMARY KEY (call_id, chat_jid)
		);
	`)
	if err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &MessageStore{db: db}
}

func testLogger() waLog.Logger {
	return waLog.Stdout("Test", "WARN", true)
}

// buildTextMessage constructs an events.Message with the given source fields.
func buildTextMessage(chat, sender, senderAlt, recipientAlt types.JID, isFromMe bool, text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:         chat,
				Sender:       sender,
				SenderAlt:    senderAlt,
				RecipientAlt: recipientAlt,
				IsFromMe:     isFromMe,
				IsGroup:      false,
			},
			ID:        "test-msg-001",
			Timestamp: time.Now(),
		},
		Message: &waProto.Message{
			Conversation: proto.String(text),
		},
	}
}

// queryChat returns the chat JID and name, or empty strings if not found.
func queryChat(ms *MessageStore, jid string) (name string, found bool) {
	err := ms.db.QueryRow("SELECT name FROM chats WHERE jid = ?", jid).Scan(&name)
	return name, err == nil
}

// queryChatLastMessageTime returns the last_message_time for a chat JID.
func queryChatLastMessageTime(ms *MessageStore, jid string) (lastMessageTime string, found bool) {
	err := ms.db.QueryRow("SELECT last_message_time FROM chats WHERE jid = ?", jid).Scan(&lastMessageTime)
	return lastMessageTime, err == nil
}

// queryMessageCount returns the number of messages stored under a chat JID.
func queryMessageCount(ms *MessageStore, chatJID string) int {
	var count int
	_ = ms.db.QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid = ?", chatJID).Scan(&count)
	return count
}

// --- Test fixtures ---

var (
	phoneLID = types.JID{User: "185366493536339", Server: types.HiddenUserServer}
	phonePN  = types.JID{User: "11234567890", Server: types.DefaultUserServer}
)

// --- Integration tests: handleMessage stores under correct JID ---

func TestHandleMessage_IncomingLIDMessage_StoredUnderPhoneJID(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildTextMessage(
		phoneLID,       // chat: arrives as LID
		phoneLID,       // sender: LID
		phonePN,        // senderAlt: phone JID (provided by whatsmeow)
		types.EmptyJID, // recipientAlt: not set for incoming
		false,          // isFromMe: incoming
		"Hola, qué tal?",
	)

	handleMessage(client, ms, msg, logger)

	// Message MUST be stored under the phone-based JID.
	if count := queryMessageCount(ms, phonePN.String()); count != 1 {
		t.Errorf("expected 1 message under phone JID %s, got %d", phonePN, count)
	}

	// No chat entry should exist for the LID JID.
	if _, found := queryChat(ms, phoneLID.String()); found {
		t.Error("LID chat entry should not exist in database")
	}

	// No message should be stored under the LID JID.
	if count := queryMessageCount(ms, phoneLID.String()); count != 0 {
		t.Errorf("expected 0 messages under LID JID %s, got %d", phoneLID, count)
	}
}

func TestHandleMessage_OutgoingLIDMessage_StoredUnderPhoneJID(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildTextMessage(
		phoneLID,       // chat: LID
		phoneLID,       // sender: self (LID)
		types.EmptyJID, // senderAlt: not set for outgoing
		phonePN,        // recipientAlt: phone JID
		true,           // isFromMe: outgoing
		"Todo bien!",
	)

	handleMessage(client, ms, msg, logger)

	if count := queryMessageCount(ms, phonePN.String()); count != 1 {
		t.Errorf("expected 1 message under phone JID %s, got %d", phonePN, count)
	}

	if count := queryMessageCount(ms, phoneLID.String()); count != 0 {
		t.Errorf("expected 0 messages under LID JID %s, got %d", phoneLID, count)
	}
}

func TestHandleMessage_LIDWithStoreFallback_StoredUnderPhoneJID(t *testing.T) {
	lidStore := &mockLIDStore{
		pnByLID: map[types.JID]types.JID{phoneLID: phonePN},
	}
	client := newTestClient(lidStore)
	ms := newTestMessageStore(t)
	logger := testLogger()

	// No SenderAlt/RecipientAlt -- must resolve via LID store.
	msg := buildTextMessage(
		phoneLID,       // chat: LID
		phoneLID,       // sender: LID
		types.EmptyJID, // senderAlt: empty (simulates missing alt)
		types.EmptyJID, // recipientAlt: empty
		false,          // isFromMe: incoming
		"Message without alt JIDs",
	)

	handleMessage(client, ms, msg, logger)

	if count := queryMessageCount(ms, phonePN.String()); count != 1 {
		t.Errorf("expected 1 message under phone JID %s, got %d", phonePN, count)
	}

	if count := queryMessageCount(ms, phoneLID.String()); count != 0 {
		t.Errorf("expected 0 messages under LID JID %s, got %d", phoneLID, count)
	}
}

func TestHandleMessage_PhoneJID_Unaffected(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildTextMessage(
		phonePN,        // chat: already phone-based
		phonePN,        // sender: phone-based
		types.EmptyJID, // senderAlt: empty
		types.EmptyJID, // recipientAlt: empty
		false,          // isFromMe: incoming
		"Normal message",
	)

	handleMessage(client, ms, msg, logger)

	if count := queryMessageCount(ms, phonePN.String()); count != 1 {
		t.Errorf("expected 1 message under phone JID %s, got %d", phonePN, count)
	}
}

// --- Sender column resolution ---
//
// These tests guard against the regression where the bridge stored the
// LID user-part (or, for outgoing messages, the recipient's phone) in the
// sender column even after the chat-JID was resolved to a phone JID.

var (
	selfLID   = types.JID{User: "999888777666555", Server: types.HiddenUserServer}
	selfPhone = types.JID{User: "10000000000", Server: types.DefaultUserServer}
)

// TestHandleMessage_OutgoingFromSelf_SenderIsOwnPhone asserts that an
// outgoing message from a LID-typed self does not get the recipient's
// phone written into the sender column. Before the fix, resolveLIDChat
// reused recipientAlt for the sender, mis-attributing self messages.
func TestHandleMessage_OutgoingFromSelf_SenderIsOwnPhone(t *testing.T) {
	client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildTextMessage(
		phoneLID,       // chat: peer LID
		selfLID,        // sender: own LID
		types.EmptyJID, // senderAlt: empty for outgoing
		phonePN,        // recipientAlt: peer phone (NOT self phone)
		true,           // outgoing
		"hi",
	)

	handleMessage(client, ms, msg, logger)

	got := querySender(ms, phonePN.String())
	if got != selfPhone.User {
		t.Errorf("outgoing sender = %q, want own phone user %q (recipient phone is %q, must not appear)",
			got, selfPhone.User, phonePN.User)
	}
}

// TestHandleMessage_IncomingLID_SenderResolvedFromAlt asserts that an
// incoming LID-only sender with a non-empty SenderAlt is rewritten to the
// peer's phone user-part, not stored as the raw LID number.
func TestHandleMessage_IncomingLID_SenderResolvedFromAlt(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildTextMessage(
		phoneLID,       // chat: LID
		phoneLID,       // sender: peer LID
		phonePN,        // senderAlt: peer phone
		types.EmptyJID, // recipientAlt: unused for incoming
		false,          // incoming
		"hola",
	)

	handleMessage(client, ms, msg, logger)

	got := querySender(ms, phonePN.String())
	if got != phonePN.User {
		t.Errorf("incoming sender = %q, want peer phone user %q", got, phonePN.User)
	}
}

// TestHandleMessage_IncomingLID_SenderResolvedFromStore covers the
// history-sync-style case: SenderAlt is empty but the LID store has a
// PN mapping for the peer LID, so the sender column should still end up
// as the phone user-part.
func TestHandleMessage_IncomingLID_SenderResolvedFromStore(t *testing.T) {
	client := newTestClient(&mockLIDStore{
		pnByLID: map[types.JID]types.JID{phoneLID: phonePN},
	})
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildTextMessage(
		phoneLID,       // chat: LID
		phoneLID,       // sender: peer LID
		types.EmptyJID, // senderAlt: empty (post-fix, fallback to LID store)
		types.EmptyJID, // recipientAlt: empty
		false,          // incoming
		"hello",
	)

	handleMessage(client, ms, msg, logger)

	got := querySender(ms, phonePN.String())
	if got != phonePN.User {
		t.Errorf("incoming sender = %q, want peer phone user %q (LID store fallback)",
			got, phonePN.User)
	}
}

// TestHandleMessage_LIDWithoutMapping_SenderFallsBackToLID asserts the
// graceful-degradation path: with no SenderAlt and no LID store mapping,
// the bridge stores the raw LID user-part rather than failing or writing
// an unrelated value.
func TestHandleMessage_LIDWithoutMapping_SenderFallsBackToLID(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildTextMessage(
		phoneLID,       // chat: LID
		phoneLID,       // sender: peer LID
		types.EmptyJID, // senderAlt: empty
		types.EmptyJID, // recipientAlt: empty
		false,          // incoming
		"orphan",
	)

	handleMessage(client, ms, msg, logger)

	// Chat JID has no mapping either, so the message ends up under the LID chat.
	got := querySender(ms, phoneLID.String())
	if got != phoneLID.User {
		t.Errorf("orphan-LID sender = %q, want raw LID user %q (graceful fallback)",
			got, phoneLID.User)
	}
}

// --- LID sender backfill migration ---

func TestMigrateLegacyLIDSendersToPhones_RewritesAndIsIdempotent(t *testing.T) {
	ms := newTestMessageStore(t)
	logger := testLogger()

	tmpDir := t.TempDir()
	whatsappDBPath := filepath.Join(tmpDir, "whatsapp.db")

	waDB, err := sql.Open("sqlite3", whatsappDBPath)
	if err != nil {
		t.Fatalf("failed to create whatsapp db: %v", err)
	}
	defer func() { _ = waDB.Close() }()

	if _, err := waDB.Exec(`
		CREATE TABLE whatsmeow_lid_map (
			lid TEXT PRIMARY KEY,
			pn TEXT NOT NULL
		);
		INSERT INTO whatsmeow_lid_map (lid, pn) VALUES ('111', '222');
		INSERT INTO whatsmeow_lid_map (lid, pn) VALUES ('333', '444');
	`); err != nil {
		t.Fatalf("failed to prepare lid map db: %v", err)
	}

	chatPhone := "222@s.whatsapp.net"
	groupChat := "group@g.us"

	if _, err := ms.db.Exec(`
		INSERT INTO chats (jid, name, last_message_time) VALUES
			(?, 'Peer', '2026-03-01T10:00:00Z'),
			(?, 'Group', '2026-03-01T11:00:00Z');

		INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length) VALUES
			('m1', ?, '111', 'incoming dm pre-fix',  '2026-03-01T10:00:00Z', 0, '', '', '', NULL, NULL, NULL, 0),
			('m2', ?, '222', 'incoming dm post-fix', '2026-03-01T10:01:00Z', 0, '', '', '', NULL, NULL, NULL, 0),
			('g1', ?, '333', 'group msg pre-fix',    '2026-03-01T11:00:00Z', 0, '', '', '', NULL, NULL, NULL, 0),
			('g2', ?, '999', 'group msg unmapped',   '2026-03-01T11:01:00Z', 0, '', '', '', NULL, NULL, NULL, 0);
	`, chatPhone, groupChat, chatPhone, chatPhone, groupChat, groupChat); err != nil {
		t.Fatalf("failed to seed message store: %v", err)
	}

	if err := ms.MigrateLegacyLIDSendersToPhones(whatsappDBPath, logger); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	type row struct {
		id, sender string
	}
	var got []row
	rows, err := ms.db.Query("SELECT id, sender FROM messages ORDER BY id")
	if err != nil {
		t.Fatalf("failed to read messages: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.sender); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}

	want := map[string]string{
		"m1": "222", // rewritten via lid map
		"m2": "222", // already phone, untouched
		"g1": "444", // rewritten via lid map
		"g2": "999", // unmapped LID stays as-is (graceful fallback)
	}
	for _, r := range got {
		if w, ok := want[r.id]; !ok || r.sender != w {
			t.Errorf("message %s: sender = %q, want %q", r.id, r.sender, w)
		}
	}

	if err := ms.MigrateLegacyLIDSendersToPhones(whatsappDBPath, logger); err != nil {
		t.Fatalf("second run should be no-op, got error: %v", err)
	}
}

func TestMigrateLegacyLIDSendersToPhones_MissingWhatsAppDBIsNoOp(t *testing.T) {
	ms := newTestMessageStore(t)
	logger := testLogger()

	missingPath := filepath.Join(t.TempDir(), "missing-whatsapp.db")
	if err := ms.MigrateLegacyLIDSendersToPhones(missingPath, logger); err != nil {
		t.Fatalf("expected missing whatsapp db to be a no-op, got error: %v", err)
	}
}

// TestHandleMessage_GroupParticipantLID_ResolvedViaStore covers the
// highest-volume path that triggers the LID-sender bug: a group message
// where the participant JID is LID-only and the per-message SenderAlt is
// empty. Resolution must come from the LID store.
func TestHandleMessage_GroupParticipantLID_ResolvedViaStore(t *testing.T) {
	groupJID := types.JID{User: "254110094043-1619359480", Server: types.GroupServer}
	participantLID := types.JID{User: "261391827087520", Server: types.HiddenUserServer}
	participantPhone := types.JID{User: "31612345678", Server: types.DefaultUserServer}

	client := newTestClient(&mockLIDStore{
		pnByLID: map[types.JID]types.JID{participantLID: participantPhone},
	})
	ms := newTestMessageStore(t)
	logger := testLogger()

	// Pre-seed the group chat row so GetChatName short-circuits on the
	// existing-name path and doesn't try to issue a GetGroupInfo IQ
	// against the fake client.
	if err := ms.StoreChat(groupJID.String(), "Test Group", time.Now()); err != nil {
		t.Fatalf("seed group chat: %v", err)
	}

	msg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     groupJID,
				Sender:   participantLID,
				IsFromMe: false,
				IsGroup:  true,
			},
			ID:        "test-group-001",
			Timestamp: time.Now(),
		},
		Message: &waProto.Message{
			Conversation: proto.String("group hello"),
		},
	}

	handleMessage(client, ms, msg, logger)

	got := querySender(ms, groupJID.String())
	if got != participantPhone.User {
		t.Errorf("group participant sender = %q, want phone user %q", got, participantPhone.User)
	}
}

// TestHandleHistorySync_LIDParticipant_ResolvedViaStore exercises the
// history-sync code path. Because history-sync rows do not carry SenderAlt,
// resolution must succeed via the LID store fallback that
// resolveUserJID consults. The stored sender column must be the phone
// user-part, not the raw LID number copied verbatim from Key.Participant.
func TestHandleHistorySync_LIDParticipant_ResolvedViaStore(t *testing.T) {
	chatJID := phonePN.String() // history-sync conversation already keyed by phone
	participantLID := types.JID{User: "445566778899", Server: types.HiddenUserServer}
	participantPhone := types.JID{User: "11234567890", Server: types.DefaultUserServer}

	client := newTestClientWithSelf(&mockLIDStore{
		pnByLID: map[types.JID]types.JID{participantLID: participantPhone},
	}, selfPhone)
	ms := newTestMessageStore(t)
	logger := testLogger()

	historySync := &events.HistorySync{
		Data: &waProto.HistorySync{
			SyncType: waProto.HistorySync_RECENT.Enum(),
			Conversations: []*waProto.Conversation{
				{
					ID: proto.String(chatJID),
					Messages: []*waProto.HistorySyncMsg{
						{
							Message: &waProto.WebMessageInfo{
								Key: &waCommon.MessageKey{
									ID:          proto.String("hist-msg-001"),
									FromMe:      proto.Bool(false),
									Participant: proto.String(participantLID.String()),
								},
								MessageTimestamp: proto.Uint64(uint64(time.Now().Unix())),
								Message: &waProto.Message{
									Conversation: proto.String("history payload"),
								},
							},
						},
					},
				},
			},
		},
	}

	handleHistorySync(client, ms, historySync, logger)

	got := querySender(ms, chatJID)
	if got != participantPhone.User {
		t.Errorf("history-sync sender = %q, want resolved phone user %q (raw LID was %q)",
			got, participantPhone.User, participantLID.User)
	}
}

func TestMigrateLegacyLIDChatsToPhoneJIDs_MigratesAndIsIdempotent(t *testing.T) {
	ms := newTestMessageStore(t)
	logger := testLogger()

	tmpDir := t.TempDir()
	whatsappDBPath := filepath.Join(tmpDir, "whatsapp.db")

	waDB, err := sql.Open("sqlite3", whatsappDBPath)
	if err != nil {
		t.Fatalf("failed to create whatsapp db: %v", err)
	}
	defer func() { _ = waDB.Close() }()

	if _, err := waDB.Exec(`
		CREATE TABLE whatsmeow_lid_map (
			lid TEXT PRIMARY KEY,
			pn TEXT NOT NULL
		);
		INSERT INTO whatsmeow_lid_map (lid, pn) VALUES ('111', '222');
	`); err != nil {
		t.Fatalf("failed to prepare lid map db: %v", err)
	}

	lidJID := "111@lid"
	phoneJID := "222@s.whatsapp.net"

	_, err = ms.db.Exec(`
		INSERT INTO chats (jid, name, last_message_time) VALUES
			(?, 'Legacy LID Name', '2026-03-01T10:00:00Z'),
			(?, '', '2026-03-01T09:00:00Z');

		INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length) VALUES
			('dup', ?, 'alice', 'lid duplicate', '2026-03-01T10:00:00Z', 0, '', '', '', NULL, NULL, NULL, 0),
			('only-lid', ?, 'alice', 'lid only', '2026-03-01T10:01:00Z', 0, '', '', '', NULL, NULL, NULL, 0),
			('dup', ?, 'alice', 'phone duplicate', '2026-03-01T10:00:00Z', 0, '', '', '', NULL, NULL, NULL, 0),
			('only-phone', ?, 'alice', 'phone only', '2026-03-01T10:02:00Z', 0, '', '', '', NULL, NULL, NULL, 0);
	`, lidJID, phoneJID, lidJID, lidJID, phoneJID, phoneJID)
	if err != nil {
		t.Fatalf("failed to seed message store: %v", err)
	}

	if err := ms.MigrateLegacyLIDChatsToPhoneJIDs(whatsappDBPath, logger); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	if lidCount := queryMessageCount(ms, lidJID); lidCount != 0 {
		t.Fatalf("expected 0 messages under migrated LID chat, got %d", lidCount)
	}
	if phoneCount := queryMessageCount(ms, phoneJID); phoneCount != 3 {
		t.Fatalf("expected 3 messages under phone chat after dedupe, got %d", phoneCount)
	}

	if _, found := queryChat(ms, lidJID); found {
		t.Fatalf("expected migrated LID chat row to be removed")
	}

	phoneName, found := queryChat(ms, phoneJID)
	if !found {
		t.Fatalf("expected phone chat row to exist after migration")
	}
	if phoneName != "Legacy LID Name" {
		t.Fatalf("expected phone chat name to be hydrated from LID chat, got %q", phoneName)
	}

	phoneTime, timeFound := queryChatLastMessageTime(ms, phoneJID)
	if !timeFound {
		t.Fatalf("expected phone chat to have last_message_time after migration")
	}
	if phoneTime != "2026-03-01T10:00:00Z" {
		t.Fatalf("expected phone chat last_message_time to be the latest (from LID chat), got %q", phoneTime)
	}

	if err := ms.MigrateLegacyLIDChatsToPhoneJIDs(whatsappDBPath, logger); err != nil {
		t.Fatalf("second migration run should be a no-op, got error: %v", err)
	}
	if phoneCount := queryMessageCount(ms, phoneJID); phoneCount != 3 {
		t.Fatalf("expected idempotent result with 3 phone messages, got %d", phoneCount)
	}
}

func TestMigrateLegacyLIDChatsToPhoneJIDs_MissingWhatsAppDBIsNoOp(t *testing.T) {
	ms := newTestMessageStore(t)
	logger := testLogger()

	missingPath := filepath.Join(t.TempDir(), "missing-whatsapp.db")
	if err := ms.MigrateLegacyLIDChatsToPhoneJIDs(missingPath, logger); err != nil {
		t.Fatalf("expected missing whatsapp db to be treated as no-op, got error: %v", err)
	}
}

func TestExtractTextContent_SurfacesMediaCaptions(t *testing.T) {
	cases := []struct {
		name string
		msg  *waProto.Message
		want string
	}{
		{
			name: "Conversation",
			msg:  &waProto.Message{Conversation: proto.String("hola")},
			want: "hola",
		},
		{
			name: "ExtendedTextMessage",
			msg: &waProto.Message{
				ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("quoted reply")},
			},
			want: "quoted reply",
		},
		{
			name: "ImageMessage with caption",
			msg: &waProto.Message{
				ImageMessage: &waProto.ImageMessage{Caption: proto.String("sunset on the beach")},
			},
			want: "sunset on the beach",
		},
		{
			name: "VideoMessage with caption",
			msg: &waProto.Message{
				VideoMessage: &waProto.VideoMessage{Caption: proto.String("the kids playing")},
			},
			want: "the kids playing",
		},
		{
			name: "DocumentMessage with caption",
			msg: &waProto.Message{
				DocumentMessage: &waProto.DocumentMessage{Caption: proto.String("invoice attached")},
			},
			want: "invoice attached",
		},
		{
			name: "ImageMessage without caption returns empty",
			msg:  &waProto.Message{ImageMessage: &waProto.ImageMessage{}},
			want: "",
		},
		{
			name: "Nil message returns empty",
			msg:  nil,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractTextContent(tc.msg)
			if got != tc.want {
				t.Errorf("extractTextContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMigrateLegacyLIDChatsToPhoneJIDs_AggregatesByPhoneJIDDeterministically(t *testing.T) {
	ms := newTestMessageStore(t)
	logger := testLogger()

	tmpDir := t.TempDir()
	whatsappDBPath := filepath.Join(tmpDir, "whatsapp.db")

	waDB, err := sql.Open("sqlite3", whatsappDBPath)
	if err != nil {
		t.Fatalf("failed to create whatsapp db: %v", err)
	}
	defer func() { _ = waDB.Close() }()

	if _, err := waDB.Exec(`
		CREATE TABLE whatsmeow_lid_map (
			lid TEXT PRIMARY KEY,
			pn TEXT NOT NULL
		);
		INSERT INTO whatsmeow_lid_map (lid, pn) VALUES ('111', '222');
		INSERT INTO whatsmeow_lid_map (lid, pn) VALUES ('333', '222');
	`); err != nil {
		t.Fatalf("failed to prepare lid map db: %v", err)
	}

	lidA := "111@lid"
	lidB := "333@lid"
	phoneJID := "222@s.whatsapp.net"

	_, err = ms.db.Exec(`
		INSERT INTO chats (jid, name, last_message_time) VALUES
			(?, 'Older Name', '2026-03-01T10:00:00Z'),
			(?, 'Newest Name', '2026-03-01T11:00:00Z');

		INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length) VALUES
			('a1', ?, 'alice', 'from lid A', '2026-03-01T10:00:00Z', 0, '', '', '', NULL, NULL, NULL, 0),
			('b1', ?, 'bob', 'from lid B', '2026-03-01T11:00:00Z', 0, '', '', '', NULL, NULL, NULL, 0);
	`, lidA, lidB, lidA, lidB)
	if err != nil {
		t.Fatalf("failed to seed message store: %v", err)
	}

	if err := ms.MigrateLegacyLIDChatsToPhoneJIDs(whatsappDBPath, logger); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	if count := queryMessageCount(ms, lidA); count != 0 {
		t.Fatalf("expected no messages under first LID after migration, got %d", count)
	}
	if count := queryMessageCount(ms, lidB); count != 0 {
		t.Fatalf("expected no messages under second LID after migration, got %d", count)
	}
	if count := queryMessageCount(ms, phoneJID); count != 2 {
		t.Fatalf("expected 2 messages under phone JID after migration, got %d", count)
	}

	name, found := queryChat(ms, phoneJID)
	if !found {
		t.Fatalf("expected merged phone chat row to exist")
	}
	if name != "Newest Name" {
		t.Fatalf("expected deterministic name selection from latest source chat, got %q", name)
	}

	var lastMessage string
	if err := ms.db.QueryRow("SELECT last_message_time FROM chats WHERE jid = ?", phoneJID).Scan(&lastMessage); err != nil {
		t.Fatalf("failed to read merged last_message_time: %v", err)
	}
	if lastMessage != "2026-03-01T11:00:00Z" {
		t.Fatalf("expected merged last_message_time to be max source value, got %s", lastMessage)
	}
}

// buildImageMessage constructs an events.Message that carries an ImageMessage
// with no download metadata (URL/media-key are empty), so handleMessage will
// classify it as an image but skip the synchronous download attempt.
func buildImageMessage(chat, sender types.JID, isFromMe bool, caption string) *events.Message {
	img := &waProto.ImageMessage{}
	if caption != "" {
		img.Caption = proto.String(caption)
	}
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: isFromMe,
			},
			ID:        "test-img-001",
			Timestamp: time.Now(),
		},
		Message: &waProto.Message{ImageMessage: img},
	}
}

// captureWebhook starts a local httptest server that records the first webhook
// payload it receives. It returns the server and a channel that yields the
// decoded payload.
func captureWebhook(t *testing.T) (*httptest.Server, <-chan WebhookPayload) {
	t.Helper()
	ch := make(chan WebhookPayload, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p WebhookPayload
		if err := json.Unmarshal(body, &p); err == nil {
			select {
			case ch <- p:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

// TestHandleMessage_ImageOnly_WebhookForwarded verifies that an image message
// with no text caption is forwarded to the webhook endpoint (not silently
// dropped), and that the webhook payload contains the expected media fields.
func TestHandleMessage_ImageOnly_WebhookForwarded(t *testing.T) {
	srv, webhookCh := captureWebhook(t)
	t.Setenv("WEBHOOK_URL", srv.URL)

	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildImageMessage(phonePN, phonePN, false, "") // no caption

	handleMessage(client, ms, msg, logger)

	// The image-only message must be stored.
	if count := queryMessageCount(ms, phonePN.String()); count != 1 {
		t.Errorf("expected 1 message stored, got %d", count)
	}

	// The webhook must have been called.
	select {
	case payload := <-webhookCh:
		if payload.MediaType != "image" {
			t.Errorf("expected mediaType=image, got %q", payload.MediaType)
		}
		if payload.MessageID != "test-img-001" {
			t.Errorf("expected messageId=test-img-001, got %q", payload.MessageID)
		}
		if payload.Content != "" {
			t.Errorf("expected empty content for image-only message, got %q", payload.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook call")
	}
}

// TestHandleMessage_ImageWithCaption_WebhookForwarded verifies that an image
// message WITH a text caption is forwarded and that the caption is included in
// the webhook content field (extractTextContent now surfaces image captions).
func TestHandleMessage_ImageWithCaption_WebhookForwarded(t *testing.T) {
	srv, webhookCh := captureWebhook(t)
	t.Setenv("WEBHOOK_URL", srv.URL)

	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildImageMessage(phonePN, phonePN, false, "look at this!")

	handleMessage(client, ms, msg, logger)

	select {
	case payload := <-webhookCh:
		if payload.MediaType != "image" {
			t.Errorf("expected mediaType=image, got %q", payload.MediaType)
		}
		if payload.MessageID != "test-img-001" {
			t.Errorf("expected messageId=test-img-001, got %q", payload.MessageID)
		}
		if payload.Content != "look at this!" {
			t.Errorf("expected caption in content, got %q", payload.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook call")
	}
}

// queryCallResult returns the (result, duration_sec, reason) for a call row,
// or empties if no row exists.
func queryCallResult(ms *MessageStore, callID, chatJID string) (result string, duration sql.NullInt64, reason sql.NullString, found bool) {
	err := ms.db.QueryRow(
		"SELECT result, duration_sec, reason FROM calls WHERE call_id = ? AND chat_jid = ?",
		callID, chatJID,
	).Scan(&result, &duration, &reason)
	return result, duration, reason, err == nil
}

// TestCallStateMachine_AllTransitions exercises every documented transition of
// the call lifecycle state machine and pins down the non-obvious invariants:
//
//   - Offer → Accept → Terminate          ⇒ "ended" (with computed duration)
//   - Offer → Terminate (no Accept)       ⇒ "missed"
//   - Offer → Reject → Terminate          ⇒ "rejected" is preserved
//     (Terminate's CASE branch must NOT downgrade rejected to ended/missed)
//   - Duplicate Offer events do not clobber a call already in a later state
//   - MarkCallAnswered/Rejected only fire when row is still in_progress
func TestCallStateMachine_AllTransitions(t *testing.T) {
	type step struct {
		name string
		do   func(ms *MessageStore) error
	}

	t0 := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	t30 := t0.Add(30 * time.Second)
	t90 := t0.Add(90 * time.Second)

	cases := []struct {
		name         string
		callID       string
		chatJID      string
		steps        []step
		wantResult   string
		wantDuration int64 // 0 = expect NULL
		wantReason   string
	}{
		{
			name:    "Offer→Accept→Terminate yields ended with duration",
			callID:  "call-answered",
			chatJID: "creator@s.whatsapp.net",
			steps: []step{
				{"offer", func(ms *MessageStore) error {
					return ms.StoreCallOffer("call-answered", "creator@s.whatsapp.net", "creator@s.whatsapp.net", t0, false, "voice", false)
				}},
				{"accept", func(ms *MessageStore) error {
					return ms.MarkCallAnswered("call-answered", "creator@s.whatsapp.net")
				}},
				{"terminate", func(ms *MessageStore) error {
					return ms.MarkCallTerminated("call-answered", "creator@s.whatsapp.net", "normal", t90)
				}},
			},
			wantResult:   "ended",
			wantDuration: 90,
			wantReason:   "normal",
		},
		{
			name:    "Offer→Terminate with no Accept yields missed",
			callID:  "call-missed",
			chatJID: "creator@s.whatsapp.net",
			steps: []step{
				{"offer", func(ms *MessageStore) error {
					return ms.StoreCallOffer("call-missed", "creator@s.whatsapp.net", "creator@s.whatsapp.net", t0, false, "voice", false)
				}},
				{"terminate", func(ms *MessageStore) error {
					return ms.MarkCallTerminated("call-missed", "creator@s.whatsapp.net", "timeout", t30)
				}},
			},
			wantResult:   "missed",
			wantDuration: 30,
			wantReason:   "timeout",
		},
		{
			name:    "Offer→Reject→Terminate preserves rejected",
			callID:  "call-rejected",
			chatJID: "creator@s.whatsapp.net",
			steps: []step{
				{"offer", func(ms *MessageStore) error {
					return ms.StoreCallOffer("call-rejected", "creator@s.whatsapp.net", "creator@s.whatsapp.net", t0, false, "voice", false)
				}},
				{"reject", func(ms *MessageStore) error {
					return ms.MarkCallRejected("call-rejected", "creator@s.whatsapp.net")
				}},
				{"terminate", func(ms *MessageStore) error {
					return ms.MarkCallTerminated("call-rejected", "creator@s.whatsapp.net", "rejected_by_user", t30)
				}},
			},
			wantResult:   "rejected",
			wantDuration: 30,
			wantReason:   "rejected_by_user",
		},
		{
			name:    "Duplicate Offer does not clobber later state",
			callID:  "call-dup-offer",
			chatJID: "creator@s.whatsapp.net",
			steps: []step{
				{"offer", func(ms *MessageStore) error {
					return ms.StoreCallOffer("call-dup-offer", "creator@s.whatsapp.net", "creator@s.whatsapp.net", t0, false, "voice", false)
				}},
				{"accept", func(ms *MessageStore) error {
					return ms.MarkCallAnswered("call-dup-offer", "creator@s.whatsapp.net")
				}},
				{"duplicate offer (should be ignored)", func(ms *MessageStore) error {
					return ms.StoreCallOffer("call-dup-offer", "creator@s.whatsapp.net", "creator@s.whatsapp.net", t0, false, "voice", false)
				}},
				{"terminate", func(ms *MessageStore) error {
					return ms.MarkCallTerminated("call-dup-offer", "creator@s.whatsapp.net", "normal", t90)
				}},
			},
			wantResult:   "ended",
			wantDuration: 90,
			wantReason:   "normal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := newTestMessageStore(t)
			for _, s := range tc.steps {
				if err := s.do(ms); err != nil {
					t.Fatalf("step %q failed: %v", s.name, err)
				}
			}

			result, duration, reason, found := queryCallResult(ms, tc.callID, tc.chatJID)
			if !found {
				t.Fatalf("expected row for call_id=%s chat_jid=%s, got none", tc.callID, tc.chatJID)
			}
			if result != tc.wantResult {
				t.Errorf("result: got %q, want %q", result, tc.wantResult)
			}
			if !duration.Valid || duration.Int64 != tc.wantDuration {
				t.Errorf("duration_sec: got %v, want %d", duration, tc.wantDuration)
			}
			if !reason.Valid || reason.String != tc.wantReason {
				t.Errorf("reason: got %v, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestCallStateMachine_AcceptAndRejectAreNoOpAfterTerminate verifies that
// late-arriving Accept/Reject events (post-Terminate) do not corrupt a
// finalized row. The WHERE result='in_progress' guard is what enforces this.
func TestCallStateMachine_AcceptAndRejectAreNoOpAfterTerminate(t *testing.T) {
	ms := newTestMessageStore(t)
	t0 := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)

	if err := ms.StoreCallOffer("call-late", "creator@s.whatsapp.net", "creator@s.whatsapp.net", t0, false, "voice", false); err != nil {
		t.Fatalf("offer: %v", err)
	}
	if err := ms.MarkCallTerminated("call-late", "creator@s.whatsapp.net", "timeout", t0.Add(30*time.Second)); err != nil {
		t.Fatalf("terminate: %v", err)
	}

	// These should be no-ops because the row is already 'missed', not 'in_progress'.
	_ = ms.MarkCallAnswered("call-late", "creator@s.whatsapp.net")
	_ = ms.MarkCallRejected("call-late", "creator@s.whatsapp.net")

	result, _, _, _ := queryCallResult(ms, "call-late", "creator@s.whatsapp.net")
	if result != "missed" {
		t.Errorf("expected missed to be preserved, got %q", result)
	}
}

// TestCallChatJID_Precedence pins down the precedence rules in callChatJID:
//
//  1. GroupJID wins (group calls always key on the group)
//  2. CallCreator wins over From (the bug Ed fixed: Accept events arrive
//     with From=accepter's JID, which is "us" if user picked up on phone)
//  3. From is the last-resort fallback
//
// Without rule 2, Accept UPDATEs miss the row stored at Offer time and the
// state machine falls through to "missed" when the user answered elsewhere.
func TestCallChatJID_Precedence(t *testing.T) {
	groupJID := types.JID{User: "120363012345678901", Server: types.GroupServer}
	creatorJID := types.JID{User: "11234567890", Server: types.DefaultUserServer}
	fromJID := types.JID{User: "19998887777", Server: types.DefaultUserServer}

	cases := []struct {
		name string
		meta types.BasicCallMeta
		want string
	}{
		{
			name: "group JID wins when present",
			meta: types.BasicCallMeta{
				GroupJID:    groupJID,
				CallCreator: creatorJID,
				From:        fromJID,
			},
			want: groupJID.String(),
		},
		{
			name: "creator wins over From for 1:1 (Accept-from-other-device case)",
			meta: types.BasicCallMeta{
				CallCreator: creatorJID,
				From:        fromJID,
			},
			want: creatorJID.ToNonAD().String(),
		},
		{
			name: "From is fallback when creator is empty",
			meta: types.BasicCallMeta{
				From: fromJID,
			},
			want: fromJID.ToNonAD().String(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := callChatJID(tc.meta)
			if got != tc.want {
				t.Errorf("callChatJID() = %q, want %q", got, tc.want)
			}
		})
	}
}
