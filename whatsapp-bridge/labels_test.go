package main

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func newLabelTestStore(t *testing.T) *MessageStore {
	t.Helper()
	ms := newTestMessageStore(t)
	if err := ensureLabelTables(ms.db); err != nil {
		t.Fatalf("ensureLabelTables: %v", err)
	}
	return ms
}

func editLabel(ms *MessageStore, id, name string, order int32, deleted bool) {
	handleLabelEdit(ms, &events.LabelEdit{
		Timestamp: time.Now(),
		LabelID:   id,
		Action: &waSyncAction.LabelEditAction{
			Name:       proto.String(name),
			Color:      proto.Int32(3),
			OrderIndex: proto.Int32(order),
			IsActive:   proto.Bool(true),
			Type:       waSyncAction.LabelEditAction_CUSTOM.Enum(),
			Deleted:    proto.Bool(deleted),
		},
	})
}

func setChatLabel(ms *MessageStore, jid types.JID, id string, labeled bool) {
	handleLabelAssociationChat(ms, &events.LabelAssociationChat{
		JID:       jid,
		Timestamp: time.Now(),
		LabelID:   id,
		Action:    &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(labeled)},
	})
}

func TestLabelEvents_ListsCarryTheirChatsWithPhoneNumbers(t *testing.T) {
	ms := newLabelTestStore(t)
	lid := types.JID{User: "999888777666555", Server: types.HiddenUserServer}
	pn := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	hidden := types.JID{User: "111222333444555", Server: types.HiddenUserServer}
	client := newTestClient(&mockLIDStore{pnByLID: map[types.JID]types.JID{
		lid: {User: "15551234567", Server: types.DefaultUserServer},
	}})

	editLabel(ms, "5", "New customer", 5, false)
	editLabel(ms, "6", "Gone", 6, true)
	setChatLabel(ms, lid, "5", true)
	setChatLabel(ms, pn, "5", true)
	setChatLabel(ms, hidden, "5", true)
	setChatLabel(ms, pn, "5", false)
	// Repeating an event (a full sync replays them) changes nothing.
	setChatLabel(ms, lid, "5", true)

	labels, err := listLabels(context.Background(), client, ms.db)
	if err != nil {
		t.Fatalf("listLabels: %v", err)
	}
	if len(labels) != 1 {
		t.Fatalf("want only the live list, got %+v", labels)
	}
	got := labels[0]
	if got.ID != "5" || got.Name != "New customer" || got.ListType != "CUSTOM" || got.Color != 3 {
		t.Fatalf("unexpected list %+v", got)
	}
	want := []labelChat{
		{JID: hidden.String(), Phone: ""},
		{JID: lid.String(), Phone: "15551234567"},
	}
	if len(got.Chats) != len(want) {
		t.Fatalf("chats = %+v, want %+v", got.Chats, want)
	}
	for i := range want {
		if got.Chats[i] != want[i] {
			t.Fatalf("chats = %+v, want %+v", got.Chats, want)
		}
	}
}

func TestStoredLabelAction_KeepsWhatThePhoneSet(t *testing.T) {
	ms := newLabelTestStore(t)
	editLabel(ms, "4", "Follow up", 4, false)

	action, err := storedLabelAction(ms.db, "4")
	if err != nil || action == nil {
		t.Fatalf("storedLabelAction: %v, %v", action, err)
	}
	if action.GetOrderIndex() != 4 || !action.GetIsActive() || action.GetType() != waSyncAction.LabelEditAction_CUSTOM {
		t.Fatalf("stored action lost fields: %v", action)
	}
	if missing, err := storedLabelAction(ms.db, "99"); err != nil || missing != nil {
		t.Fatalf("unknown list: %v, %v", missing, err)
	}
}

func TestNewLabelSlot_FollowsEveryListEvenDeletedOnes(t *testing.T) {
	ms := newLabelTestStore(t)
	if id, order, err := newLabelSlot(ms.db); err != nil || id != "1" || order != 1 {
		t.Fatalf("empty store: %q %d %v", id, order, err)
	}
	editLabel(ms, "4", "Follow up", 4, false)
	editLabel(ms, "9", "Old", 2, true)
	if id, order, err := newLabelSlot(ms.db); err != nil || id != "10" || order != 5 {
		t.Fatalf("got %q %d %v, want 10 5", id, order, err)
	}
}

func TestPhoneDigits(t *testing.T) {
	cases := map[string]string{
		"+1 555-123-4567": "15551234567",
		"0015551234567":   "15551234567",
		"15551234567":     "15551234567",
		"12":              "",
		"":                "",
	}
	for in, want := range cases {
		if got := phoneDigits(in); got != want {
			t.Errorf("phoneDigits(%q) = %q, want %q", in, got, want)
		}
	}
}
