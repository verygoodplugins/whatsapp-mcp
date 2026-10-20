package main

import (
	"strings"
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestHistorySyncUnwrapsContent(t *testing.T) {
	contact := &waProto.Message{ContactMessage: &waProto.ContactMessage{
		DisplayName: proto.String("Ada"), Vcard: proto.String("TEL:+12025550123"),
	}}
	contacts := &waProto.Message{ContactsArrayMessage: &waProto.ContactsArrayMessage{
		Contacts: []*waProto.ContactMessage{contact.ContactMessage},
	}}
	for name, tc := range map[string]struct {
		raw         *waProto.Message
		text, media string
	}{
		"single card":          {raw: &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: contact}}, text: "Ada (+12025550123)"},
		"multiple cards":       {raw: &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: contacts}}, text: "Ada (+12025550123)"},
		"device and ephemeral": {raw: &waProto.Message{DeviceSentMessage: &waProto.DeviceSentMessage{Message: &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: contact}}}}, text: "Ada (+12025550123)"},
		"text":                 {raw: &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{Conversation: proto.String("wrapped text")}}}, text: "wrapped text"},
		"image":                {raw: &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{ImageMessage: &waProto.ImageMessage{Caption: proto.String("wrapped caption")}}}}, text: "wrapped caption", media: "image"},
	} {
		t.Run(name, func(t *testing.T) {
			client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
			ms := newTestMessageStore(t)
			row := &waProto.HistorySyncMsg{Message: &waProto.WebMessageInfo{
				Key:              &waCommon.MessageKey{ID: proto.String("wrapped"), FromMe: proto.Bool(false)},
				MessageTimestamp: proto.Uint64(1710000000), Message: tc.raw,
			}}
			sync := &events.HistorySync{Data: &waProto.HistorySync{
				SyncType:      waProto.HistorySync_RECENT.Enum(),
				Conversations: []*waProto.Conversation{{ID: proto.String(phonePN.String()), Messages: []*waProto.HistorySyncMsg{row}}},
			}}
			handleHistorySync(client, ms, sync, testLogger())
			var content, media string
			if err := ms.db.QueryRow("SELECT content, COALESCE(media_type, '') FROM messages WHERE id='wrapped'").Scan(&content, &media); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(content, tc.text) || media != tc.media {
				t.Fatalf("stored content=%q media=%q", content, media)
			}
		})
	}
}
