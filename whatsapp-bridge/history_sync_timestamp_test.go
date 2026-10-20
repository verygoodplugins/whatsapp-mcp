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

func TestHandleHistorySync_ProtocolMetadataIsRetainedWithoutActivity(t *testing.T) {
	client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
	ms := newTestMessageStore(t)
	ts := time.Unix(1710000000, 0)
	sync := historySyncFor(phonePN.String(), historyRevokeMsg("control", "other", ts))
	conversation := sync.Data.Conversations[0]
	conversation.UnreadCount = proto.Uint32(0)
	conversation.EphemeralExpiration = proto.Uint32(86400)
	conversation.EphemeralSettingTimestamp = proto.Int64(ts.Unix())
	handleHistorySync(client, ms, sync, testLogger())
	_, valid, exists := historyChatLastMessageTime(t, ms, phonePN.String())
	if !exists || valid || queryMessageCount(ms, phonePN.String()) != 0 {
		t.Fatalf("metadata must be retained without inventing message activity (exists=%v valid=%v)", exists, valid)
	}
	settings, err := ms.GetChatEphemeralSettings(phonePN.String())
	if err != nil || settings.Expiration != 86400 {
		t.Fatalf("disappearing-message settings lost: %+v, %v", settings, err)
	}
	var readAt sql.NullTime
	if err := ms.db.QueryRow("SELECT last_read_time FROM chats WHERE jid = ?", phonePN.String()).Scan(&readAt); err != nil || !readAt.Valid || !readAt.Time.Equal(ts) {
		t.Fatalf("read metadata lost: %v, %v", readAt, err)
	}
}

func TestHandleHistorySync_SupportedBusinessTextSetsChatTime(t *testing.T) {
	for _, archived := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			msg  *waProto.Message
		}{
			{"template", &waProto.Message{TemplateMessage: &waProto.TemplateMessage{
				HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{HydratedContentText: proto.String("business text")},
			}}},
			{"buttons", &waProto.Message{ButtonsMessage: &waProto.ButtonsMessage{ContentText: proto.String("button text")}}},
		} {
			t.Run(tc.name+map[bool]string{false: "/active", true: "/archived"}[archived], func(t *testing.T) {
				client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
				ms := newTestMessageStore(t)
				ts := time.Unix(1710000000, 0)
				text := historyTextMsg("business", "", ts)
				text.Message.Message = tc.msg
				sync := historySyncFor(phonePN.String(), historyRevokeMsg("control", "other", ts.Add(time.Minute)), text)
				sync.Data.Conversations[0].Archived = proto.Bool(archived)
				handleHistorySync(client, ms, sync, testLogger())
				got, valid, exists := historyChatLastMessageTime(t, ms, phonePN.String())
				if !exists || !valid || !got.Equal(ts) {
					t.Fatalf("last message time = %v (valid=%v, exists=%v), want %v", got, valid, exists, ts)
				}
				if n := queryMessageCount(ms, phonePN.String()); n != 1 {
					t.Fatalf("stored %d messages, want 1", n)
				}
			})
		}
	}
}
