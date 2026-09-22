package main

import (
	"database/sql"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// --- last_message_time advances only when a message row is stored ---
//
// list_chats orders by chats.last_message_time and the MCP server derives
// `unread` from the message row at that instant. Reactions and protocol
// messages (revokes, ephemeral-setting changes, polls…) must therefore never
// move it: WhatsApp neither reorders nor badges a chat for them.

func reactionEvent(targetID, emoji string, ts time.Time, fromMe bool) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     phonePN,
				Sender:   phonePN,
				IsFromMe: fromMe,
			},
			ID:        "reaction-" + targetID,
			Timestamp: ts,
		},
		Message: &waProto.Message{
			ReactionMessage: &waProto.ReactionMessage{
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String(phonePN.String()),
					ID:        proto.String(targetID),
					FromMe:    proto.Bool(true),
				},
				Text: proto.String(emoji),
			},
		},
	}
}

func textEvent(id, text string, ts time.Time) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     phonePN,
				Sender:   phonePN,
				IsFromMe: false,
			},
			ID:        id,
			Timestamp: ts,
		},
		Message: &waProto.Message{Conversation: proto.String(text)},
	}
}

func readLastMessageTime(t *testing.T, ms *MessageStore, chatJID string) (time.Time, bool, bool) {
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

func seedChatWithMessage(t *testing.T, ms *MessageStore, chatJID, msgID string, ts time.Time) {
	t.Helper()
	if err := ms.StoreChat(chatJID, "Alice", ts); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := ms.StoreMessage(msgID, chatJID, phonePN.User, "hello", ts, false,
		"", "", "", nil, nil, nil, 0, ""); err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

func TestHandleMessage_InboundReactionDoesNotAdvanceLastMessageTime(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	chatJID := phonePN.String()
	t0 := time.Unix(1710000000, 0)
	seedChatWithMessage(t, ms, chatJID, "orig-1", t0)

	handleMessage(client, ms, reactionEvent("orig-1", "👍", t0.Add(time.Minute), false), testLogger())

	got, valid, exists := readLastMessageTime(t, ms, chatJID)
	if !exists || !valid || !got.Equal(t0) {
		t.Fatalf("reaction must not advance last_message_time: got %v (valid=%v exists=%v), want %v", got, valid, exists, t0)
	}
	var mediaType string
	if err := ms.db.QueryRow("SELECT media_type FROM messages WHERE id = ? AND chat_jid = ?",
		"reaction-orig-1", chatJID).Scan(&mediaType); err != nil || mediaType != "reaction" {
		t.Fatalf("reaction row must still be stored: media_type=%q err=%v", mediaType, err)
	}
}

func TestHandleMessage_ProtocolMessageDoesNotAdvanceLastMessageTime(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	chatJID := phonePN.String()
	t0 := time.Unix(1710000000, 0)
	seedChatWithMessage(t, ms, chatJID, "orig-1", t0)

	handleMessage(client, ms, revokeEvent("orig-1", t0.Add(time.Minute)), testLogger())

	got, valid, exists := readLastMessageTime(t, ms, chatJID)
	if !exists || !valid || !got.Equal(t0) {
		t.Fatalf("revoke must not advance last_message_time: got %v (valid=%v exists=%v), want %v", got, valid, exists, t0)
	}
}

func TestHandleMessage_ContentMessageAdvancesLastMessageTime(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	chatJID := phonePN.String()
	t0 := time.Unix(1710000000, 0)
	t1 := t0.Add(time.Minute)
	seedChatWithMessage(t, ms, chatJID, "orig-1", t0)

	handleMessage(client, ms, textEvent("new-1", "second message", t1), testLogger())

	got, valid, exists := readLastMessageTime(t, ms, chatJID)
	if !exists || !valid || !got.Equal(t1) {
		t.Fatalf("content message must advance last_message_time: got %v (valid=%v exists=%v), want %v", got, valid, exists, t1)
	}
}

func TestHandleMessage_ReactionInUnknownChatCreatesRowWithoutTimestamp(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	chatJID := phonePN.String()

	handleMessage(client, ms, reactionEvent("never-seen", "❤️", time.Unix(1710000000, 0), false), testLogger())

	// The chats row must exist (messages.chat_jid is a foreign key) but carry
	// no last_message_time, so list_chats has nothing to order or badge on.
	_, valid, exists := readLastMessageTime(t, ms, chatJID)
	if !exists {
		t.Fatalf("reaction must create the chats row so the reaction row can reference it")
	}
	if valid {
		t.Fatalf("reaction must not set last_message_time on a new chats row")
	}
}

func TestHandleMessage_ProtocolMessageInUnknownChatCreatesNoRow(t *testing.T) {
	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)

	handleMessage(client, ms, revokeEvent("never-seen", time.Unix(1710000000, 0)), testLogger())

	if _, _, exists := readLastMessageTime(t, ms, phonePN.String()); exists {
		t.Fatalf("a bare protocol message must not invent a chats row")
	}
}
