package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	waProto "go.mau.fi/whatsmeow/binary/proto"
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
	`)
	if err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}
	t.Cleanup(func() { db.Close() })
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

// queryMessageCount returns the number of messages stored under a chat JID.
func queryMessageCount(ms *MessageStore, chatJID string) int {
	var count int
	ms.db.QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid = ?", chatJID).Scan(&count)
	return count
}

// --- Test fixtures ---

var (
	phoneLID = types.JID{User: "185366493536339", Server: types.HiddenUserServer}
	phonePN  = types.JID{User: "11234567890", Server: types.DefaultUserServer}
	groupJID = types.JID{User: "120363123456789", Server: types.GroupServer}
)

// --- Unit tests: resolveLIDChat ---

func TestResolveLIDChat_NonLIDPassthrough(t *testing.T) {
	client := newTestClient(&mockLIDStore{})

	got := resolveLIDChat(client, phonePN, types.EmptyJID, types.EmptyJID, false)
	if got != phonePN {
		t.Errorf("expected %s, got %s", phonePN, got)
	}

	got = resolveLIDChat(client, groupJID, types.EmptyJID, types.EmptyJID, false)
	if got != groupJID {
		t.Errorf("expected %s, got %s", groupJID, got)
	}
}

func TestResolveLIDChat_IncomingWithSenderAlt(t *testing.T) {
	client := newTestClient(&mockLIDStore{})

	got := resolveLIDChat(client, phoneLID, phonePN, types.EmptyJID, false)
	if got != phonePN {
		t.Errorf("expected %s, got %s", phonePN, got)
	}
}

func TestResolveLIDChat_OutgoingWithRecipientAlt(t *testing.T) {
	client := newTestClient(&mockLIDStore{})

	got := resolveLIDChat(client, phoneLID, types.EmptyJID, phonePN, true)
	if got != phonePN {
		t.Errorf("expected %s, got %s", phonePN, got)
	}
}

func TestResolveLIDChat_FallbackToLIDStore(t *testing.T) {
	lidStore := &mockLIDStore{
		pnByLID: map[types.JID]types.JID{phoneLID: phonePN},
	}
	client := newTestClient(lidStore)

	got := resolveLIDChat(client, phoneLID, types.EmptyJID, types.EmptyJID, false)
	if got != phonePN {
		t.Errorf("expected %s, got %s", phonePN, got)
	}
}

func TestResolveLIDChat_UnresolvableLIDReturnsOriginal(t *testing.T) {
	client := newTestClient(&mockLIDStore{})

	got := resolveLIDChat(client, phoneLID, types.EmptyJID, types.EmptyJID, false)
	if got != phoneLID {
		t.Errorf("expected %s (original LID), got %s", phoneLID, got)
	}
}

// --- Integration tests: handleMessage stores under correct JID ---

func TestHandleMessage_IncomingLIDMessage_StoredUnderPhoneJID(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	logger := testLogger()

	msg := buildTextMessage(
		phoneLID,        // chat: arrives as LID
		phoneLID,        // sender: LID
		phonePN,         // senderAlt: phone JID (provided by whatsmeow)
		types.EmptyJID,  // recipientAlt: not set for incoming
		false,           // isFromMe: incoming
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
		phoneLID,        // chat: LID
		phoneLID,        // sender: self (LID)
		types.EmptyJID,  // senderAlt: not set for outgoing
		phonePN,         // recipientAlt: phone JID
		true,            // isFromMe: outgoing
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
		phoneLID,        // chat: LID
		phoneLID,        // sender: LID
		types.EmptyJID,  // senderAlt: empty (simulates missing alt)
		types.EmptyJID,  // recipientAlt: empty
		false,           // isFromMe: incoming
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
		phonePN,         // chat: already phone-based
		phonePN,         // sender: phone-based
		types.EmptyJID,  // senderAlt: empty
		types.EmptyJID,  // recipientAlt: empty
		false,           // isFromMe: incoming
		"Normal message",
	)

	handleMessage(client, ms, msg, logger)

	if count := queryMessageCount(ms, phonePN.String()); count != 1 {
		t.Errorf("expected 1 message under phone JID %s, got %d", phonePN, count)
	}
}
