package main

import (
	"database/sql"
	"testing"
	"time"
)

// go-sqlite3 stores time.Time as local wall clock plus offset. Across a DST
// switch (or a host timezone change) the later instant can have the smaller
// string, so every "newer than" decision must compare instants, not text.
// 2026-10-25 is the UK autumn switch: 01:30 BST (00:30Z) precedes 01:15 GMT (01:15Z).

var (
	dstEarlier = time.Date(2026, 10, 25, 1, 30, 0, 0, time.FixedZone("BST", 3600)) // stored "01:30:00+01:00"
	dstLater   = time.Date(2026, 10, 25, 1, 15, 0, 0, time.UTC)                    // stored "01:15:00+00:00"
)

func chatTimeColumn(t *testing.T, ms *MessageStore, jid, column string) time.Time {
	t.Helper()
	var got sql.NullTime
	if err := ms.db.QueryRow("SELECT "+column+" FROM chats WHERE jid = ?", jid).Scan(&got); err != nil {
		t.Fatalf("read %s: %v", column, err)
	}
	if !got.Valid {
		t.Fatalf("%s is NULL", column)
	}
	return got.Time
}

func TestDSTFixtureIsOrderedByInstantNotString(t *testing.T) {
	if !dstLater.After(dstEarlier) {
		t.Fatal("fixture: dstLater must be the later instant")
	}
	if dstLater.Format("2006-01-02 15:04:05-07:00") > dstEarlier.Format("2006-01-02 15:04:05-07:00") {
		t.Fatal("fixture: dstLater must have the lexicographically smaller stored form")
	}
}

func TestStoreChat_AdvancesAcrossOffsetChange(t *testing.T) {
	ms := newTestMessageStore(t)
	jid := phonePN.String()

	if err := ms.StoreChat(jid, "Alice", dstEarlier); err != nil {
		t.Fatal(err)
	}
	if err := ms.StoreChat(jid, "Alice", dstLater); err != nil {
		t.Fatal(err)
	}
	if got := chatTimeColumn(t, ms, jid, "last_message_time"); !got.Equal(dstLater) {
		t.Fatalf("later instant with smaller string must win: got %v want %v", got, dstLater)
	}
	// Still monotonic the other way round.
	if err := ms.StoreChat(jid, "Alice", dstEarlier); err != nil {
		t.Fatal(err)
	}
	if got := chatTimeColumn(t, ms, jid, "last_message_time"); !got.Equal(dstLater) {
		t.Fatalf("older instant must not regress the marker: got %v want %v", got, dstLater)
	}
}

func TestMarkChatRead_AdvancesAcrossOffsetChange(t *testing.T) {
	ms := newTestMessageStore(t)
	jid := phonePN.String()

	if err := ms.MarkChatRead(jid, dstEarlier); err != nil {
		t.Fatal(err)
	}
	if err := ms.MarkChatRead(jid, dstLater); err != nil {
		t.Fatal(err)
	}
	if got := chatTimeColumn(t, ms, jid, "last_read_time"); !got.Equal(dstLater) {
		t.Fatalf("later read receipt with smaller string must win: got %v want %v", got, dstLater)
	}
	if err := ms.MarkChatRead(jid, dstEarlier); err != nil {
		t.Fatal(err)
	}
	if got := chatTimeColumn(t, ms, jid, "last_read_time"); !got.Equal(dstLater) {
		t.Fatalf("older receipt must not regress the marker: got %v want %v", got, dstLater)
	}
}

func TestMaxMessageTimestamp_PicksLatestInstant(t *testing.T) {
	ms := newTestMessageStore(t)
	jid := phonePN.String()
	if err := ms.StoreChat(jid, "Alice", dstEarlier); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		id string
		ts time.Time
	}{{"a", dstEarlier}, {"b", dstLater}} {
		if err := ms.StoreMessage(m.id, jid, phonePN.User, "x", m.ts, false, "", "", "", nil, nil, nil, 0, ""); err != nil {
			t.Fatal(err)
		}
	}

	got, ok, err := ms.MaxMessageTimestamp(jid, []string{"a", "b"})
	if err != nil || !ok {
		t.Fatalf("expected a timestamp, got ok=%v err=%v", ok, err)
	}
	if !got.Equal(dstLater) {
		t.Fatalf("MAX(text) would pick %v; want the later instant %v, got %v", dstEarlier, dstLater, got)
	}

	if _, ok, err := ms.MaxMessageTimestamp(jid, []string{"nope"}); err != nil || ok {
		t.Fatalf("unknown ids must report ok=false without error, got ok=%v err=%v", ok, err)
	}
}
