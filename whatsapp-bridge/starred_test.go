package main

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func readStarred(t *testing.T, ms *MessageStore, chatJID, messageID string) sql.NullInt64 {
	t.Helper()
	var starred sql.NullInt64
	err := ms.db.QueryRow(
		"SELECT starred FROM starred_messages WHERE chat_jid = ? AND message_id = ?", chatJID, messageID,
	).Scan(&starred)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read starred: %v", err)
	}
	return starred
}

func starEvent(chat types.JID, messageID string, starred bool, at time.Time) *events.Star {
	return &events.Star{
		ChatJID:   chat,
		MessageID: messageID,
		Timestamp: at,
		Action:    &waSyncAction.StarAction{Starred: proto.Bool(starred)},
	}
}

func TestStarStateOnlyMovesForward(t *testing.T) {
	ms := newChatStateStore(t)
	chat := "123@s.whatsapp.net"
	t1 := time.Unix(1_700_000_000, 0)
	t2 := t1.Add(time.Minute)

	if err := ms.SetMessageStarred(chat, "m1", true, t2); err != nil {
		t.Fatal(err)
	}
	// A late, older unstar (or a full-sync replay of it) must not win.
	if err := ms.SetMessageStarred(chat, "m1", false, t1); err != nil {
		t.Fatal(err)
	}
	if got := readStarred(t, ms, chat, "m1"); got.Int64 != 1 {
		t.Fatalf("starred = %+v, want 1", got)
	}

	if err := ms.SetMessageStarred(chat, "m1", false, t2.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// The unstar is kept as a row so an older star can't resurrect the message.
	if err := ms.SetMessageStarred(chat, "m1", true, t2); err != nil {
		t.Fatal(err)
	}
	if got := readStarred(t, ms, chat, "m1"); !got.Valid || got.Int64 != 0 {
		t.Fatalf("starred = %+v, want 0", got)
	}
}

func TestHandleStarEvent(t *testing.T) {
	ms := newChatStateStore(t)
	logger := waLog.Noop
	group := types.NewJID("120363000000000000", types.GroupServer)
	at := time.Unix(1_700_000_000, 0)

	// The message isn't stored: the star must still be kept for when it is.
	if !handleChatStateEvent(nil, ms, starEvent(group, "unstored", true, at), logger) {
		t.Fatal("*events.Star not handled")
	}
	if got := readStarred(t, ms, group.String(), "unstored"); got.Int64 != 1 {
		t.Fatalf("starred = %+v, want 1", got)
	}

	handleChatStateEvent(nil, ms, starEvent(group, "unstored", false, at.Add(time.Hour)), logger)
	if got := readStarred(t, ms, group.String(), "unstored"); got.Int64 != 0 {
		t.Fatalf("starred = %+v after unstar, want 0", got)
	}
}

func TestStoredMessageAuthor(t *testing.T) {
	ms := newChatStateStore(t)
	chat := "120363000000000000@g.us"
	at := time.Unix(1_700_000_000, 0)
	if err := ms.StoreChat(chat, "g", at); err != nil {
		t.Fatal(err)
	}
	if err := ms.StoreMessage("in", chat, "972500000001", "hi", at, false, "", "", "", nil, nil, nil, 0, ""); err != nil {
		t.Fatal(err)
	}

	sender, isFromMe, err := ms.StoredMessageAuthor(chat, "in")
	if err != nil || sender != "972500000001" || isFromMe {
		t.Fatalf("got (%q, %v, %v)", sender, isFromMe, err)
	}
	if _, _, err := ms.StoredMessageAuthor(chat, "missing"); !errors.Is(err, errStarMessageNotStored) {
		t.Fatalf("err = %v, want errStarMessageNotStored", err)
	}
}

func TestStarParticipant(t *testing.T) {
	group := types.NewJID("120363000000000000", types.GroupServer)
	dm := types.NewJID("972500000002", types.DefaultUserServer)
	cases := []struct {
		name     string
		chat     types.JID
		sender   string
		isFromMe bool
		want     string
	}{
		{"group message from someone else names them", group, "972500000001", false, "972500000001"},
		{"group sender stored as a full JID", group, "972500000001@s.whatsapp.net", false, "972500000001"},
		{"own group message uses 0", group, "972500000009", true, ""},
		{"DM inbound uses 0", dm, "972500000002", false, ""},
		{"DM outbound uses 0", dm, "972500000009", true, ""},
	}
	for _, c := range cases {
		if got := starParticipant(c.chat, c.sender, c.isFromMe); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParticipantJID(t *testing.T) {
	lidUser := types.NewJID("123456789012345", types.HiddenUserServer)
	pnUser := types.NewJID("972500000001", types.DefaultUserServer)
	participants := []types.GroupParticipant{
		{JID: lidUser, LID: lidUser, PhoneNumber: pnUser},
		{JID: types.NewJID("999", types.HiddenUserServer), LID: types.NewJID("999", types.HiddenUserServer)}, // phone hidden
	}
	cases := []struct {
		user string
		want types.JID
		ok   bool
	}{
		{"123456789012345", lidUser, true},                         // stored as LID digits
		{"972500000001", lidUser, true},                            // stored as phone digits
		{"999", types.NewJID("999", types.HiddenUserServer), true}, // hidden phone: stays a LID
		{"555", types.JID{}, false},                                // left the group
	}
	for _, c := range cases {
		got, ok := participantJID(participants, c.user)
		if ok != c.ok || got != c.want {
			t.Errorf("participantJID(%q) = %v, %v; want %v, %v", c.user, got, ok, c.want, c.ok)
		}
	}
}
