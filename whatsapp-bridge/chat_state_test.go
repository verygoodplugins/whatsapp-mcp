package main

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func newChatStateStore(t *testing.T) *MessageStore {
	t.Helper()
	ms := newTestMessageStore(t)
	if err := ensureMessageStoreSchema(ms.db); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return ms
}

type chatStateRow struct {
	archived, mutedUntil, markedUnread sql.NullInt64
	lastReadTime                       sql.NullTime
	name                               sql.NullString
}

func readChatState(t *testing.T, ms *MessageStore, jid string) chatStateRow {
	t.Helper()
	var r chatStateRow
	err := ms.db.QueryRow(
		"SELECT archived, muted_until, marked_unread, last_read_time, name FROM chats WHERE jid = ?", jid,
	).Scan(&r.archived, &r.mutedUntil, &r.markedUnread, &r.lastReadTime, &r.name)
	if err != nil {
		t.Fatalf("read chat state: %v", err)
	}
	return r
}

func TestChatFlagsOnlyMoveForward(t *testing.T) {
	ms := newChatStateStore(t)
	jid := "123@s.whatsapp.net"
	t1 := time.Unix(1_700_000_000, 0)
	t2 := t1.Add(time.Minute)

	if err := ms.SetChatArchived(jid, true, t2); err != nil {
		t.Fatal(err)
	}
	// An older mutation delivered late must not unarchive.
	if err := ms.SetChatArchived(jid, false, t1); err != nil {
		t.Fatal(err)
	}
	// Nor may history sync, which has no mutation time.
	if err := ms.SetChatArchived(jid, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r := readChatState(t, ms, jid)
	if !r.archived.Valid || r.archived.Int64 != 1 {
		t.Fatalf("archived = %+v, want 1", r.archived)
	}
	// The app-state row must not invent chat metadata.
	if r.name.Valid {
		t.Fatalf("name = %q, want NULL", r.name.String)
	}

	if err := ms.SetChatArchived(jid, false, t2.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if r := readChatState(t, ms, jid); r.archived.Int64 != 0 {
		t.Fatalf("archived = %d after newer unarchive, want 0", r.archived.Int64)
	}
}

func TestHistoryChatStateOnlyWritesPresentFields(t *testing.T) {
	ms := newChatStateStore(t)
	logger := waLog.Noop
	jid := "123@s.whatsapp.net"

	storeHistoryChatState(ms, jid, &waHistorySync.Conversation{Archived: proto.Bool(true)}, logger)
	// A sparse chunk omitting the fields must not clear them.
	storeHistoryChatState(ms, jid, &waHistorySync.Conversation{}, logger)

	r := readChatState(t, ms, jid)
	if r.archived.Int64 != 1 {
		t.Fatalf("archived = %+v, want 1", r.archived)
	}
	if r.markedUnread.Valid {
		t.Fatalf("marked_unread = %+v, want NULL", r.markedUnread)
	}
}

func TestMuteEndMillis(t *testing.T) {
	cases := []struct {
		muted bool
		end   int64
		want  int64
	}{
		{false, 0, 0},
		{false, 1_800_000_000_000, 0},
		{true, 0, 0}, // whatsmeow reads an explicit 0 as the epoch: expired
		{true, -1, -1},
		{true, 1_800_000_000_000, 1_800_000_000_000},
	}
	for _, c := range cases {
		if got := muteEndMillis(c.muted, c.end); got != c.want {
			t.Errorf("muteEndMillis(%v, %d) = %d, want %d", c.muted, c.end, got, c.want)
		}
	}
}

func TestHandleChatStateEvents(t *testing.T) {
	ms := newChatStateStore(t)
	logger := waLog.Noop
	jid := types.NewJID("123", types.DefaultUserServer)
	at := time.Unix(1_700_000_000, 0)

	stateEvents := []interface{}{
		&events.Archive{JID: jid, Timestamp: at, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}},
		&events.Mute{JID: jid, Timestamp: at, Action: &waSyncAction.MuteAction{Muted: proto.Bool(true), MuteEndTimestamp: proto.Int64(-1)}},
		&events.MarkChatAsRead{JID: jid, Timestamp: at, Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)}},
	}
	for _, evt := range stateEvents {
		if !handleChatStateEvent(nil, ms, evt, logger) {
			t.Fatalf("%T not handled", evt)
		}
	}
	r := readChatState(t, ms, jid.String())
	if r.archived.Int64 != 1 || r.mutedUntil.Int64 != -1 || r.markedUnread.Int64 != 1 {
		t.Fatalf("state = %+v", r)
	}
	if r.lastReadTime.Valid {
		t.Fatalf("mark-as-unread advanced the read marker to %s", r.lastReadTime.Time)
	}

	// Marking read clears the flag and reads the chat up to the range's last message.
	read := &events.MarkChatAsRead{JID: jid, Timestamp: at.Add(time.Hour), Action: &waSyncAction.MarkChatAsReadAction{
		Read:         proto.Bool(true),
		MessageRange: &waSyncAction.SyncActionMessageRange{LastMessageTimestamp: proto.Int64(at.Add(time.Minute).Unix())},
	}}
	handleChatStateEvent(nil, ms, read, logger)
	r = readChatState(t, ms, jid.String())
	if r.markedUnread.Int64 != 0 {
		t.Fatalf("marked_unread = %d, want 0", r.markedUnread.Int64)
	}
	if !r.lastReadTime.Valid || !r.lastReadTime.Time.Equal(at.Add(time.Minute)) {
		t.Fatalf("last_read_time = %+v, want %s", r.lastReadTime, at.Add(time.Minute))
	}

	if handleChatStateEvent(nil, ms, &events.Connected{}, logger) {
		t.Fatal("unrelated event reported as handled")
	}
}

func TestBackfillReadMarkerFromUnreadCount(t *testing.T) {
	ms := newChatStateStore(t)
	jid := "123@s.whatsapp.net"
	base := time.Unix(1_700_000_000, 0).UTC()
	if _, err := ms.db.Exec("INSERT INTO chats (jid, name) VALUES (?, 'x')", jid); err != nil {
		t.Fatal(err)
	}
	// Inbound at +1..+5 min, a reaction at +6 and an outbound at +7.
	for i := 1; i <= 5; i++ {
		if err := ms.StoreMessage(fmt.Sprintf("in%d", i), jid, jid, "hi", base.Add(time.Duration(i)*time.Minute), false, "", "", "", nil, nil, nil, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := ms.StoreMessage("react", jid, jid, "", base.Add(6*time.Minute), false, "reaction", "in1", "", nil, nil, nil, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := ms.StoreMessage("out", jid, "me", "yo", base.Add(7*time.Minute), true, "", "", "", nil, nil, nil, 0, ""); err != nil {
		t.Fatal(err)
	}

	// Phone says 2 unread: the marker lands on the 3rd-newest inbound (+3).
	if err := ms.BackfillReadMarkerFromUnreadCount(jid, 2); err != nil {
		t.Fatal(err)
	}
	r := readChatState(t, ms, jid)
	if !r.lastReadTime.Valid || !r.lastReadTime.Time.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("last_read_time = %+v, want %s", r.lastReadTime, base.Add(3*time.Minute))
	}

	// An existing marker is never moved.
	if err := ms.BackfillReadMarkerFromUnreadCount(jid, 4); err != nil {
		t.Fatal(err)
	}
	if r := readChatState(t, ms, jid); !r.lastReadTime.Time.Equal(base.Add(3 * time.Minute)) {
		t.Fatalf("existing marker moved to %s", r.lastReadTime.Time)
	}

	// More unread than stored: nothing to anchor on, marker stays NULL.
	other := "456@s.whatsapp.net"
	if _, err := ms.db.Exec("INSERT INTO chats (jid, name) VALUES (?, 'y')", other); err != nil {
		t.Fatal(err)
	}
	if err := ms.StoreMessage("o1", other, other, "hi", base, false, "", "", "", nil, nil, nil, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := ms.BackfillReadMarkerFromUnreadCount(other, 3); err != nil {
		t.Fatal(err)
	}
	if r := readChatState(t, ms, other); r.lastReadTime.Valid {
		t.Fatalf("marker set to %s with too few messages", r.lastReadTime.Time)
	}
}
