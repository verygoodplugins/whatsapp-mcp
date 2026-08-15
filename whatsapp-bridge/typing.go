package main

import (
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// TypingState represents the composing state of a user in a chat.
type TypingState struct {
	ChatJID   string    `json:"chat_jid"`
	SenderJID string    `json:"sender_jid"`
	IsTyping  bool      `json:"is_typing"`
	Media     string    `json:"media,omitempty"` // "" for text, "audio" for voice recording
	UpdatedAt time.Time `json:"updated_at"`
}

// typingKey creates a unique key for a chat+sender combination.
func typingKey(chatJID, senderJID string) string {
	return chatJID + "|" + senderJID
}

// TypingStore tracks inbound typing/composing presence per chat and sender.
// WhatsApp sends "composing" when a contact starts typing and "paused" when
// they stop. If no "paused" arrives (e.g. network hiccup), stale entries are
// expired after a timeout.
type TypingStore struct {
	mu      sync.RWMutex
	entries map[string]*TypingState
	expiry  time.Duration
}

// DefaultTypingExpiry is how long a "composing" state lives before auto-expiry.
// WhatsApp's UI typically shows typing for ~10-15 seconds; we use 30 seconds
// to be conservative while still cleaning up stale entries.
const DefaultTypingExpiry = 30 * time.Second

// NewTypingStore creates a new typing state store with the given expiry.
func NewTypingStore(expiry time.Duration) *TypingStore {
	ts := &TypingStore{
		entries: make(map[string]*TypingState),
		expiry:  expiry,
	}
	// Start background cleanup goroutine.
	go ts.cleanupLoop()
	return ts
}

// cleanupLoop periodically removes expired entries.
func (ts *TypingStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		ts.cleanup()
	}
}

// cleanup removes all expired entries.
func (ts *TypingStore) cleanup() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := time.Now()
	for key, state := range ts.entries {
		if state.IsTyping && now.Sub(state.UpdatedAt) > ts.expiry {
			delete(ts.entries, key)
		}
	}
}

// Update records a typing state change for a chat/sender. Returns the previous
// state (nil if none) and whether this is a state change worth notifying about.
func (ts *TypingStore) Update(chatJID, senderJID string, isTyping bool, media string) (prev *TypingState, changed bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	key := typingKey(chatJID, senderJID)
	now := time.Now()

	existing := ts.entries[key]
	if existing != nil {
		// Copy previous state for return.
		prevCopy := *existing
		prev = &prevCopy
	}

	newState := &TypingState{
		ChatJID:   chatJID,
		SenderJID: senderJID,
		IsTyping:  isTyping,
		Media:     media,
		UpdatedAt: now,
	}

	if isTyping {
		ts.entries[key] = newState
		// Changed if no previous state or was not typing.
		changed = prev == nil || !prev.IsTyping
	} else {
		// "Paused" clears the entry.
		delete(ts.entries, key)
		// Changed if there was a previous typing state.
		changed = prev != nil && prev.IsTyping
	}

	return prev, changed
}

// Get returns the current typing state for a chat/sender, or nil if not typing.
// Expired entries are treated as non-existent.
func (ts *TypingStore) Get(chatJID, senderJID string) *TypingState {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	key := typingKey(chatJID, senderJID)
	state := ts.entries[key]
	if state == nil {
		return nil
	}
	// Check expiry.
	if time.Since(state.UpdatedAt) > ts.expiry {
		return nil
	}
	// Return a copy.
	stateCopy := *state
	return &stateCopy
}

// GetForChat returns all currently-typing senders in a chat.
func (ts *TypingStore) GetForChat(chatJID string) []*TypingState {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	now := time.Now()
	var result []*TypingState
	for _, state := range ts.entries {
		if state.ChatJID == chatJID && state.IsTyping && now.Sub(state.UpdatedAt) <= ts.expiry {
			stateCopy := *state
			result = append(result, &stateCopy)
		}
	}
	return result
}

// ListActive returns all currently-typing states across all chats.
func (ts *TypingStore) ListActive() []*TypingState {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	now := time.Now()
	var result []*TypingState
	for _, state := range ts.entries {
		if state.IsTyping && now.Sub(state.UpdatedAt) <= ts.expiry {
			stateCopy := *state
			result = append(result, &stateCopy)
		}
	}
	return result
}

// TypingStateFromJIDs converts whatsmeow JIDs to a TypingState.
func TypingStateFromJIDs(chat, sender types.JID, isTyping bool, media string) *TypingState {
	return &TypingState{
		ChatJID:   chat.String(),
		SenderJID: sender.String(),
		IsTyping:  isTyping,
		Media:     media,
		UpdatedAt: time.Now(),
	}
}
