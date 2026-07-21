package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

	t.Run("go String() format is parsed", func(t *testing.T) {
		if got := anchorTime("2025-03-04 05:06:07 +0000 UTC"); !got.Equal(want) {
			t.Fatalf("anchorTime(string) = %v, want %v", got, want)
		}
	})

	t.Run("rfc3339 string is parsed", func(t *testing.T) {
		if got := anchorTime("2025-03-04T05:06:07Z"); !got.Equal(want) {
			t.Fatalf("anchorTime(rfc3339) = %v, want %v", got, want)
		}
	})

	t.Run("byte slice is parsed", func(t *testing.T) {
		if got := anchorTime([]byte("2025-03-04T05:06:07Z")); !got.Equal(want) {
			t.Fatalf("anchorTime([]byte) = %v, want %v", got, want)
		}
	})

	// These two are the cross-driver case the string fallback exists for:
	// a messages.db written by cgo go-sqlite3, read by a pure-Go build. Both
	// failed before the format list was extended.
	offsetWant := time.Date(2026, 8, 1, 20, 0, 0, 123000000, time.UTC)

	t.Run("cgo space-separated format is parsed", func(t *testing.T) {
		if got := anchorTime("2026-08-01 14:00:00.123-06:00"); !got.Equal(offsetWant) {
			t.Fatalf("anchorTime(cgo space) = %v, want %v", got, offsetWant)
		}
	})

	t.Run("cgo T-separated format is parsed", func(t *testing.T) {
		if got := anchorTime("2026-08-01T14:00:00.123-06:00"); !got.Equal(offsetWant) {
			t.Fatalf("anchorTime(cgo T) = %v, want %v", got, offsetWant)
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

// TestHistoryAnchorInfo asserts the four fields whatsmeow's
// BuildHistorySyncRequest actually reads are carried through. The request's
// wire message has no field for the anchor's sender or a group flag, so those
// are intentionally not set and not tested.
func TestHistoryAnchorInfo(t *testing.T) {
	chat := types.JID{User: "15552223333", Server: types.DefaultUserServer}
	ts := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)

	got := historyAnchorInfo(chat, "MSGID", true, ts)

	if got.Chat != chat {
		t.Errorf("Chat = %v, want %v", got.Chat, chat)
	}
	if !got.IsFromMe {
		t.Error("IsFromMe = false, want true")
	}
	if got.ID != "MSGID" {
		t.Errorf("ID = %q, want %q", got.ID, "MSGID")
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, ts)
	}
}

// TestHistoryEndpointValidation exercises the handler's own request validation.
// The real auth wrapper is covered by auth_test.go, so an identity wrapper is
// used here. A nil client is sufficient: each of these paths returns at or
// before the connection check, and Client.IsConnected is nil-safe (returns
// false), which is exactly the "not connected" response we assert.
func TestHistoryEndpointValidation(t *testing.T) {
	identity := func(h http.HandlerFunc) http.HandlerFunc { return h }
	mux := http.NewServeMux()
	registerHistoryEndpoint(mux, identity, nil, nil)

	cases := []struct {
		name       string
		method     string
		body       string
		wantStatus int
	}{
		{"wrong method", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"invalid json", http.MethodPost, "{not json", http.StatusBadRequest},
		{"missing chat_jid", http.MethodPost, `{"count":10}`, http.StatusBadRequest},
		{"not connected", http.MethodPost, `{"chat_jid":"15552223333@s.whatsapp.net"}`, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, "/api/history", body)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}
