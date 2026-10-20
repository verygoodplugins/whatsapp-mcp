package main

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func TestHistoryContactCardAdvancesActivity(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, archived := range []bool{false, true} {
			client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
			ms := newTestMessageStore(t)
			ts := time.Unix(1710000000, 0)
			card := historyTextMsg("card", "", ts)
			card.Message.Message = &waProto.Message{ContactMessage: &waProto.ContactMessage{
				DisplayName: proto.String("Shared Contact"), Vcard: proto.String("TEL:+12025550123"),
			}}
			if wrapped {
				card.Message.Message = &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: card.Message.Message}}
			}
			sync := historySyncFor(phonePN.String(), historyRevokeMsg("control", "other", ts.Add(time.Minute)), card)
			sync.Data.Conversations[0].Archived = proto.Bool(archived)
			handleHistorySync(client, ms, sync, testLogger())
			got, valid, exists := historyChatLastMessageTime(t, ms, phonePN.String())
			if !exists || !valid || !got.Equal(ts) || queryMessageCount(ms, phonePN.String()) != 1 {
				t.Fatalf("wrapped=%v archived=%v: contact card must set chat time to %v, got %v (valid=%v exists=%v)", wrapped, archived, ts, got, valid, exists)
			}
		}
	}
}
