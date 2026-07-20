package main

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestClampHistoryCount(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero falls back to default", 0, defaultHistoryCount},
		{"negative falls back to default", -5, defaultHistoryCount},
		{"in range is preserved", 25, 25},
		{"max is preserved", maxHistoryCount, maxHistoryCount},
		{"above max is clamped", maxHistoryCount + 1, maxHistoryCount},
		{"absurd value is clamped", 1 << 20, maxHistoryCount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampHistoryCount(tc.in); got != tc.want {
				t.Fatalf("clampHistoryCount(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestAnchorTime(t *testing.T) {
	want := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)

	t.Run("time.Time passes through", func(t *testing.T) {
		if got := anchorTime(want); !got.Equal(want) {
			t.Fatalf("anchorTime(time.Time) = %v, want %v", got, want)
		}
	})

	t.Run("go formatted string is parsed", func(t *testing.T) {
		got := anchorTime("2025-03-04 05:06:07 +0000 UTC")
		if !got.Equal(want) {
			t.Fatalf("anchorTime(string) = %v, want %v", got, want)
		}
	})

	t.Run("rfc3339 string is parsed", func(t *testing.T) {
		got := anchorTime("2025-03-04T05:06:07Z")
		if !got.Equal(want) {
			t.Fatalf("anchorTime(rfc3339) = %v, want %v", got, want)
		}
	})

	t.Run("byte slice is parsed", func(t *testing.T) {
		got := anchorTime([]byte("2025-03-04T05:06:07Z"))
		if !got.Equal(want) {
			t.Fatalf("anchorTime([]byte) = %v, want %v", got, want)
		}
	})

	t.Run("unparseable value yields zero time", func(t *testing.T) {
		if got := anchorTime("not a timestamp"); !got.IsZero() {
			t.Fatalf("anchorTime(garbage) = %v, want zero time", got)
		}
	})

	t.Run("unexpected type yields zero time", func(t *testing.T) {
		if got := anchorTime(42); !got.IsZero() {
			t.Fatalf("anchorTime(int) = %v, want zero time", got)
		}
	})
}

func TestHistoryAnchorInfo(t *testing.T) {
	var (
		own   = types.JID{User: "15550001111", Server: types.DefaultUserServer}
		dm    = types.JID{User: "15552223333", Server: types.DefaultUserServer}
		group = types.JID{User: "120363000000000000", Server: types.GroupServer}
		ts    = time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	)

	t.Run("own message is attributed to the paired account", func(t *testing.T) {
		got := historyAnchorInfo(dm, "MSGID", "15550001111", true, ts, own)
		if !got.IsFromMe {
			t.Fatal("IsFromMe = false, want true")
		}
		if got.Sender != own {
			t.Fatalf("Sender = %v, want %v", got.Sender, own)
		}
		if got.IsGroup {
			t.Fatal("IsGroup = true for a 1:1 chat, want false")
		}
	})

	t.Run("bare phone sender is expanded to a user JID", func(t *testing.T) {
		got := historyAnchorInfo(dm, "MSGID", "15552223333", false, ts, own)
		if got.Sender != dm {
			t.Fatalf("Sender = %v, want %v", got.Sender, dm)
		}
	})

	t.Run("full JID sender is parsed", func(t *testing.T) {
		got := historyAnchorInfo(group, "MSGID", "15552223333@s.whatsapp.net", false, ts, own)
		if got.Sender != dm {
			t.Fatalf("Sender = %v, want %v", got.Sender, dm)
		}
	})

	t.Run("group chat is flagged and keeps the participant as sender", func(t *testing.T) {
		got := historyAnchorInfo(group, "MSGID", "15552223333", false, ts, own)
		if !got.IsGroup {
			t.Fatal("IsGroup = false for a group chat, want true")
		}
		if got.Chat != group {
			t.Fatalf("Chat = %v, want %v", got.Chat, group)
		}
		if got.Sender != dm {
			t.Fatalf("Sender = %v, want %v", got.Sender, dm)
		}
	})

	t.Run("empty sender falls back to the chat", func(t *testing.T) {
		got := historyAnchorInfo(dm, "MSGID", "", false, ts, own)
		if got.Sender != dm {
			t.Fatalf("Sender = %v, want %v", got.Sender, dm)
		}
	})

	t.Run("id and timestamp are carried through", func(t *testing.T) {
		got := historyAnchorInfo(dm, "MSGID", "15552223333", false, ts, own)
		if got.ID != "MSGID" {
			t.Fatalf("ID = %q, want %q", got.ID, "MSGID")
		}
		if !got.Timestamp.Equal(ts) {
			t.Fatalf("Timestamp = %v, want %v", got.Timestamp, ts)
		}
	})
}
