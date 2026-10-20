package main

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestContactCardsPreserveContext(t *testing.T) {
	ctx := &waProto.ContextInfo{
		StanzaID:                  proto.String("original"),
		Participant:               proto.String(phonePN.String()),
		QuotedMessage:             &waProto.Message{Conversation: proto.String("quoted text")},
		MentionedJID:              []string{phonePN.String()},
		Expiration:                proto.Uint32(86400),
		EphemeralSettingTimestamp: proto.Int64(1710000000),
	}
	for name, msg := range map[string]*waProto.Message{
		"single": {ContactMessage: &waProto.ContactMessage{DisplayName: proto.String("Ada"), ContextInfo: ctx}},
		"multiple": {ContactsArrayMessage: &waProto.ContactsArrayMessage{
			Contacts: []*waProto.ContactMessage{{DisplayName: proto.String("Ada")}}, ContextInfo: ctx,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			id, sender, content := extractQuotedMessageInfo(msg)
			if id != "original" || sender != phonePN.String() || content != "quoted text" {
				t.Fatalf("quoted context lost: %q %q %q", id, sender, content)
			}
			if mentions := extractMentionedJIDs(msg); len(mentions) != 1 || mentions[0] != phonePN.String() {
				t.Fatalf("mentions lost: %v", mentions)
			}
			client := newTestClient(&mockLIDStore{})
			ms := newTestMessageStore(t)
			handleMessage(client, ms, &events.Message{
				Info: types.MessageInfo{
					MessageSource: types.MessageSource{Chat: phonePN, Sender: phonePN},
					ID:            "card", Timestamp: time.Unix(1710000001, 0),
				}, Message: msg,
			}, testLogger())
			if got, valid := queryQuotedMessageID(ms, phonePN.String(), "card"); !valid || got != "original" {
				t.Fatalf("stored quoted ID = %q, valid=%v", got, valid)
			}
			var expiration uint32
			var setting int64
			if err := ms.db.QueryRow("SELECT ephemeral_expiration, ephemeral_setting_timestamp FROM chats WHERE jid = ?", phonePN.String()).Scan(&expiration, &setting); err != nil {
				t.Fatal(err)
			}
			if expiration != 86400 || setting != 1710000000 {
				t.Fatalf("ephemeral settings lost: %d %d", expiration, setting)
			}
		})
	}
}
