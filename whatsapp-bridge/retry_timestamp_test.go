package main

import (
	"testing"
	"time"
)

// resetOrigMsgTime clears the package-level cache between tests.
func resetOrigMsgTime() {
	origMsgTimeMu.Lock()
	defer origMsgTimeMu.Unlock()
	origMsgTime = make(map[string]time.Time)
}

func TestRememberAndTakeOriginalTimestamp(t *testing.T) {
	resetOrigMsgTime()

	orig := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	rememberOriginalTimestamp("MSG_A", orig)

	got, ok := takeOriginalTimestamp("MSG_A")
	if !ok {
		t.Fatal("expected cached timestamp for MSG_A")
	}
	if !got.Equal(orig) {
		t.Fatalf("got %s, want %s", got, orig)
	}

	// A message ID is consumed on take: a second take must miss.
	if _, ok := takeOriginalTimestamp("MSG_A"); ok {
		t.Fatal("expected MSG_A to be consumed after take")
	}
}

func TestRememberKeepsEarliest(t *testing.T) {
	resetOrigMsgTime()

	earliest := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	later := earliest.Add(12 * time.Hour) // e.g. the retry-resend time

	// Order of observation must not matter: the earliest (original) always wins.
	rememberOriginalTimestamp("MSG_B", later)
	rememberOriginalTimestamp("MSG_B", earliest)
	rememberOriginalTimestamp("MSG_B", later)

	got, ok := takeOriginalTimestamp("MSG_B")
	if !ok {
		t.Fatal("expected cached timestamp for MSG_B")
	}
	if !got.Equal(earliest) {
		t.Fatalf("got %s, want earliest %s", got, earliest)
	}
}

func TestRememberIgnoresEmptyInput(t *testing.T) {
	resetOrigMsgTime()

	rememberOriginalTimestamp("", time.Now())       // empty ID
	rememberOriginalTimestamp("MSG_C", time.Time{}) // zero time

	if _, ok := takeOriginalTimestamp(""); ok {
		t.Fatal("empty ID should never be cached")
	}
	if _, ok := takeOriginalTimestamp("MSG_C"); ok {
		t.Fatal("zero timestamp should never be cached")
	}
}

func TestTakeMissingReturnsFalse(t *testing.T) {
	resetOrigMsgTime()
	if _, ok := takeOriginalTimestamp("does-not-exist"); ok {
		t.Fatal("expected miss for unknown ID")
	}
}
