package main

import (
	"testing"
	"time"
)

func TestTypingStore_UpdateAndGet(t *testing.T) {
	store := &TypingStore{
		entries: make(map[string]*TypingState),
		expiry:  30 * time.Second,
	}

	// Initially, no typing state.
	if got := store.Get("chat1", "sender1"); got != nil {
		t.Errorf("expected nil for non-existent state, got %+v", got)
	}

	// Update with composing state.
	prev, changed := store.Update("chat1", "sender1", true, "")
	if prev != nil {
		t.Errorf("expected nil prev for first update, got %+v", prev)
	}
	if !changed {
		t.Error("expected changed=true for first composing update")
	}

	// Get should return the state.
	state := store.Get("chat1", "sender1")
	if state == nil {
		t.Fatal("expected state after update, got nil")
	}
	if !state.IsTyping {
		t.Error("expected IsTyping=true")
	}
	if state.ChatJID != "chat1" {
		t.Errorf("expected ChatJID=chat1, got %s", state.ChatJID)
	}
	if state.SenderJID != "sender1" {
		t.Errorf("expected SenderJID=sender1, got %s", state.SenderJID)
	}

	// Update again with composing - not a change since already typing.
	_, changed = store.Update("chat1", "sender1", true, "")
	if changed {
		t.Error("expected changed=false for duplicate composing update")
	}

	// Update with paused state.
	prev, changed = store.Update("chat1", "sender1", false, "")
	if prev == nil {
		t.Error("expected non-nil prev for paused update")
	}
	if !prev.IsTyping {
		t.Error("expected prev.IsTyping=true")
	}
	if !changed {
		t.Error("expected changed=true for paused update")
	}

	// Get should return nil after pause.
	if got := store.Get("chat1", "sender1"); got != nil {
		t.Errorf("expected nil after pause, got %+v", got)
	}
}

func TestTypingStore_UpdateWithMedia(t *testing.T) {
	store := &TypingStore{
		entries: make(map[string]*TypingState),
		expiry:  30 * time.Second,
	}

	store.Update("chat1", "sender1", true, "audio")

	state := store.Get("chat1", "sender1")
	if state == nil {
		t.Fatal("expected state, got nil")
	}
	if state.Media != "audio" {
		t.Errorf("expected Media=audio, got %s", state.Media)
	}
}

func TestTypingStore_GetForChat(t *testing.T) {
	store := &TypingStore{
		entries: make(map[string]*TypingState),
		expiry:  30 * time.Second,
	}

	// Add multiple senders to same chat.
	store.Update("chat1", "sender1", true, "")
	store.Update("chat1", "sender2", true, "audio")
	store.Update("chat2", "sender3", true, "")

	states := store.GetForChat("chat1")
	if len(states) != 2 {
		t.Errorf("expected 2 states for chat1, got %d", len(states))
	}

	// Check that we got the right senders.
	senders := make(map[string]bool)
	for _, s := range states {
		senders[s.SenderJID] = true
	}
	if !senders["sender1"] || !senders["sender2"] {
		t.Errorf("expected sender1 and sender2, got %v", senders)
	}
}

func TestTypingStore_ListActive(t *testing.T) {
	store := &TypingStore{
		entries: make(map[string]*TypingState),
		expiry:  30 * time.Second,
	}

	store.Update("chat1", "sender1", true, "")
	store.Update("chat2", "sender2", true, "audio")

	states := store.ListActive()
	if len(states) != 2 {
		t.Errorf("expected 2 active states, got %d", len(states))
	}
}

func TestTypingStore_Expiry(t *testing.T) {
	store := &TypingStore{
		entries: make(map[string]*TypingState),
		expiry:  50 * time.Millisecond,
	}

	store.Update("chat1", "sender1", true, "")

	// Should exist immediately.
	if got := store.Get("chat1", "sender1"); got == nil {
		t.Error("expected state immediately after update")
	}

	// Wait for expiry.
	time.Sleep(60 * time.Millisecond)

	// Should be expired.
	if got := store.Get("chat1", "sender1"); got != nil {
		t.Errorf("expected nil after expiry, got %+v", got)
	}
}

func TestTypingStore_CleanupRemovesExpired(t *testing.T) {
	store := &TypingStore{
		entries: make(map[string]*TypingState),
		expiry:  50 * time.Millisecond,
	}

	store.Update("chat1", "sender1", true, "")

	// Entry should exist.
	if len(store.entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(store.entries))
	}

	// Wait for expiry.
	time.Sleep(60 * time.Millisecond)

	// Run cleanup.
	store.cleanup()

	// Entry should be removed.
	if len(store.entries) != 0 {
		t.Errorf("expected 0 entries after cleanup, got %d", len(store.entries))
	}
}

func TestTypingStore_PausedNotChanged(t *testing.T) {
	store := &TypingStore{
		entries: make(map[string]*TypingState),
		expiry:  30 * time.Second,
	}

	// Paused on non-existent entry is not a change.
	_, changed := store.Update("chat1", "sender1", false, "")
	if changed {
		t.Error("expected changed=false for paused on non-existent entry")
	}
}
