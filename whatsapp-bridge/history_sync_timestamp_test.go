package main

import (
	"database/sql"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// History sync delivers each conversation newest-first, and that first entry
// is frequently a protocol message (revoke, ephemeral-setting change) that the
// store loop skips. chats.last_message_time must follow the newest message that
// actually lands in messages, exactly as it does for live traffic.

func historyTextMsg(id, text string, ts time.Time) *waProto.HistorySyncMsg {
	return &waProto.HistorySyncMsg{Message: &waProto.WebMessageInfo{
		Key:              &waCommon.MessageKey{ID: proto.String(id), FromMe: proto.Bool(false)},
		MessageTimestamp: proto.Uint64(uint64(ts.Unix())),
		Message:          &waProto.Message{Conversation: proto.String(text)},
	}}
}

func historyRevokeMsg(id, targetID string, ts time.Time) *waProto.HistorySyncMsg {
	return &waProto.HistorySyncMsg{Message: &waProto.WebMessageInfo{
		Key:              &waCommon.MessageKey{ID: proto.String(id), FromMe: proto.Bool(false)},
		MessageTimestamp: proto.Uint64(uint64(ts.Unix())),
		Message: &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
			Type: waProto.ProtocolMessage_REVOKE.Enum(),
			Key:  &waCommon.MessageKey{ID: proto.String(targetID), FromMe: proto.Bool(false)},
		}},
	}}
}

func historySyncFor(chatJID string, msgs ...*waProto.HistorySyncMsg) *events.HistorySync {
	return &events.HistorySync{Data: &waProto.HistorySync{
		SyncType:      waProto.HistorySync_RECENT.Enum(),
		Conversations: []*waProto.Conversation{{ID: proto.String(chatJID), Messages: msgs}},
	}}
}

func historyChatLastMessageTime(t *testing.T, ms *MessageStore, chatJID string) (time.Time, bool, bool) {
	t.Helper()
	var got sql.NullTime
	err := ms.db.QueryRow("SELECT last_message_time FROM chats WHERE jid = ?", chatJID).Scan(&got)
	if err == sql.ErrNoRows {
		return time.Time{}, false, false
	}
	if err != nil {
		t.Fatalf("read last_message_time: %v", err)
	}
	return got.Time, got.Valid, true
}

func TestHandleHistorySync_ProtocolLatestDoesNotSetLastMessageTime(t *testing.T) {
	client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
	ms := newTestMessageStore(t)
	chatJID := phonePN.String()
	t0 := time.Unix(1710000000, 0)
	t1 := t0.Add(time.Minute)

	handleHistorySync(client, ms, historySyncFor(chatJID,
		historyRevokeMsg("hist-revoke", "hist-text", t1), // newest entry, never stored
		historyTextMsg("hist-text", "hello", t0),
	), testLogger())

	got, valid, exists := historyChatLastMessageTime(t, ms, chatJID)
	if !exists || !valid || !got.Equal(t0) {
		t.Fatalf("last_message_time must follow the newest stored message %v; got %v (valid=%v exists=%v)", t0, got, valid, exists)
	}
	if n := queryMessageCount(ms, chatJID); n != 1 {
		t.Fatalf("expected the text message stored once, got %d rows", n)
	}
}

func TestHandleHistorySync_NewestStoredMessageWins(t *testing.T) {
	client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
	ms := newTestMessageStore(t)
	chatJID := phonePN.String()
	t0 := time.Unix(1710000000, 0)
	t1 := t0.Add(time.Minute)

	// Out-of-order delivery must not matter: the newest stored message wins.
	handleHistorySync(client, ms, historySyncFor(chatJID,
		historyTextMsg("hist-a", "first", t0),
		historyTextMsg("hist-b", "second", t1),
	), testLogger())

	got, valid, exists := historyChatLastMessageTime(t, ms, chatJID)
	if !exists || !valid || !got.Equal(t1) {
		t.Fatalf("expected last_message_time %v, got %v (valid=%v exists=%v)", t1, got, valid, exists)
	}
}

func TestHandleHistorySync_ProtocolOnlyConversationCreatesNoChat(t *testing.T) {
	client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
	ms := newTestMessageStore(t)
	chatJID := phonePN.String()

	handleHistorySync(client, ms, historySyncFor(chatJID,
		historyRevokeMsg("hist-revoke", "never-seen", time.Unix(1710000000, 0)),
	), testLogger())

	if _, _, exists := historyChatLastMessageTime(t, ms, chatJID); exists {
		t.Fatalf("a conversation with nothing storable must not create a chats row")
	}
}
