package main

import (
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func TestResolveLIDChat_NilClientFallsBackToLID(t *testing.T) {
	lid := types.JID{User: "184125298348272", Server: types.HiddenUserServer}

	// No alt hint and no client: must not panic, must return the LID unchanged.
	got := resolveLIDChat(nil, lid, types.EmptyJID, types.EmptyJID, false)
	if got != lid {
		t.Fatalf("expected unresolved LID %s, got %s", lid, got)
	}

	// A client whose store has no LID map behaves the same way.
	got = resolveLIDChat(&whatsmeow.Client{}, lid, types.EmptyJID, types.EmptyJID, false)
	if got != lid {
		t.Fatalf("expected unresolved LID %s with empty client, got %s", lid, got)
	}
}
