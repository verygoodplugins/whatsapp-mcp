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

// SQLite's date functions round to milliseconds, so julianday() reads these
// as one instant. Each pair also puts the later instant on the smaller string.
var subMillisecondPairs = []struct {
	name           string
	earlier, later time.Time
}{
	{
		// stored "14:00:00.1233+02:00" vs "12:00:00.1234+00:00"
		name:    "same millisecond across offsets",
		earlier: time.Date(2024, 1, 15, 14, 0, 0, 123300000, time.FixedZone("EET", 2*3600)),
		later:   time.Date(2024, 1, 15, 12, 0, 0, 123400000, time.UTC),
	},
	{
		// Older SQLite releases round .9995 and up into the next second.
		name:    "fraction that rounds into the next second",
		earlier: time.Date(2024, 1, 15, 13, 0, 0, 999900000, time.FixedZone("CET", 3600)),
		later:   time.Date(2024, 1, 15, 12, 0, 1, 0, time.UTC),
	},
	{
		name:    "nanoseconds",
		earlier: time.Date(2024, 1, 15, 12, 0, 0, 123456788, time.UTC),
		later:   time.Date(2024, 1, 15, 12, 0, 0, 123456789, time.UTC),
	},
}

func TestInstantSQL_NormalisesOffsetAndKeepsFraction(t *testing.T) {
	ms := newTestMessageStore(t)
	for _, tc := range []struct {
		in   string
		want sql.NullString
	}{
		{"2024-01-15 14:00:00.12335+02:00", sql.NullString{String: "2024-01-15 12:00:00.123350000", Valid: true}},
		{"2024-01-15 12:00:00.9999+00:00", sql.NullString{String: "2024-01-15 12:00:00.999900000", Valid: true}},
		{"2024-01-15 07:00:00.5-05:00", sql.NullString{String: "2024-01-15 12:00:00.500000000", Valid: true}},
		{"2024-01-15 12:00:00+00:00", sql.NullString{String: "2024-01-15 12:00:00.000000000", Valid: true}},
		{"2024-01-15T12:00:00.123456789Z", sql.NullString{String: "2024-01-15 12:00:00.123456789", Valid: true}},
		{"2024-01-15 12:00:00.25", sql.NullString{String: "2024-01-15 12:00:00.250000000", Valid: true}},
		{"not a time", sql.NullString{}},
	} {
		var got sql.NullString
		if err := ms.db.QueryRow("SELECT "+instantSQL("?1"), tc.in).Scan(&got); err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("instantSQL(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestStoreChat_AdvancesWithinMillisecond(t *testing.T) {
	for _, tc := range subMillisecondPairs {
		t.Run(tc.name, func(t *testing.T) {
			ms := newTestMessageStore(t)
			jid := phonePN.String()
			for _, ts := range []time.Time{tc.earlier, tc.later, tc.earlier} {
				if err := ms.StoreChat(jid, "Alice", ts); err != nil {
					t.Fatal(err)
				}
			}
			if got := chatTimeColumn(t, ms, jid, "last_message_time"); !got.Equal(tc.later) {
				t.Fatalf("got %v want %v", got, tc.later)
			}
		})
	}
}

func TestMarkChatRead_AdvancesWithinMillisecond(t *testing.T) {
	for _, tc := range subMillisecondPairs {
		t.Run(tc.name, func(t *testing.T) {
			ms := newTestMessageStore(t)
			jid := phonePN.String()
			for _, ts := range []time.Time{tc.earlier, tc.later, tc.earlier} {
				if err := ms.MarkChatRead(jid, ts); err != nil {
					t.Fatal(err)
				}
			}
			if got := chatTimeColumn(t, ms, jid, "last_read_time"); !got.Equal(tc.later) {
				t.Fatalf("got %v want %v", got, tc.later)
			}
		})
	}
}

func TestMaxMessageTimestamp_PicksLatestWithinMillisecond(t *testing.T) {
	for _, tc := range subMillisecondPairs {
		t.Run(tc.name, func(t *testing.T) {
			ms := newTestMessageStore(t)
			jid := phonePN.String()
			if err := ms.StoreChat(jid, "Alice", tc.earlier); err != nil {
				t.Fatal(err)
			}
			// Insert the earlier message first: on a tied sort key SQLite keeps
			// insertion order, so a key that loses precision picks the wrong row.
			for _, m := range []struct {
				id string
				ts time.Time
			}{{"earlier", tc.earlier}, {"later", tc.later}} {
				if err := ms.StoreMessage(m.id, jid, phonePN.User, "x", m.ts, false, "", "", "", nil, nil, nil, 0, ""); err != nil {
					t.Fatal(err)
				}
			}
			got, ok, err := ms.MaxMessageTimestamp(jid, []string{"earlier", "later"})
			if err != nil || !ok {
				t.Fatalf("expected a timestamp, got ok=%v err=%v", ok, err)
			}
			if !got.Equal(tc.later) {
				t.Fatalf("got %v want %v", got, tc.later)
			}
		})
	}
}
