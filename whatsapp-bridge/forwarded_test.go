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

func forwardedText(score uint32) *waProto.Message {
	return &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
		Text:        proto.String("chain letter"),
		ContextInfo: &waProto.ContextInfo{IsForwarded: proto.Bool(true), ForwardingScore: proto.Uint32(score)},
	}}
}

func TestForwardingScore(t *testing.T) {
	for name, tc := range map[string]struct {
		msg  *waProto.Message
		want int
	}{
		"nil":   {nil, 0},
		"plain": {&waProto.Message{Conversation: proto.String("hi")}, 0},
		"text":  {forwardedText(2), 2},
		"flag without score": {&waProto.Message{ImageMessage: &waProto.ImageMessage{
			ContextInfo: &waProto.ContextInfo{IsForwarded: proto.Bool(true)}}}, 1},
		"reply, not forwarded": {&waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String("hi"), ContextInfo: &waProto.ContextInfo{StanzaID: proto.String("x")}}}, 0},
		"ephemeral envelope": {&waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: forwardedText(6)}}, 6},
	} {
		if got := forwardingScore(tc.msg); got != tc.want {
			t.Errorf("%s: got %d, want %d", name, got, tc.want)
		}
	}
}

func storedScore(t *testing.T, ms *MessageStore, id string) sql.NullInt64 {
	t.Helper()
	var v sql.NullInt64
	if err := ms.db.QueryRow(`SELECT forwarding_score FROM messages WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestStoreMessageKeepsForwardingScore(t *testing.T) {
	const chat = "15551234567@s.whatsapp.net"
	ms := newTestMessageStore(t)
	store := func(score int) {
		if err := ms.StoreMessage("m1", chat, "15551234567", "hi", time.Now(), false, "", "", "", nil, nil, nil, 0, "", score); err != nil {
			t.Fatal(err)
		}
	}
	store(0)
	if v := storedScore(t, ms, "m1"); v.Valid {
		t.Fatalf("not forwarded: got %v, want NULL", v.Int64)
	}
	store(6)
	store(0) // a later store without the flag must not clear it
	if v := storedScore(t, ms, "m1"); v.Int64 != 6 {
		t.Fatalf("got %v, want 6", v)
	}
}

func TestHistorySyncRecordsForwards(t *testing.T) {
	client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
	ms := newTestMessageStore(t)
	row := &waProto.HistorySyncMsg{Message: &waProto.WebMessageInfo{
		Key:              &waCommon.MessageKey{ID: proto.String("fwd"), FromMe: proto.Bool(false)},
		MessageTimestamp: proto.Uint64(1710000000),
		Message:          &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: forwardedText(5)}},
	}}
	handleHistorySync(client, ms, &events.HistorySync{Data: &waProto.HistorySync{
		SyncType:      waProto.HistorySync_RECENT.Enum(),
		Conversations: []*waProto.Conversation{{ID: proto.String(phonePN.String()), Messages: []*waProto.HistorySyncMsg{row}}},
	}}, testLogger())
	if v := storedScore(t, ms, "fwd"); v.Int64 != 5 {
		t.Fatalf("got %v, want 5", v)
	}
}
