package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func webhookHitCounter(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestWebhookEgressDefaultDeniesEveryPayloadType(t *testing.T) {
	const chatJID = "15551234567@s.whatsapp.net"
	eventTimestamp := time.Unix(1_720_000_100, 0).UTC()
	messageStore := newMCPAccessTestStore(t)
	seedMCPAccessChat(t, messageStore, chatJID, "Private", &eventTimestamp)

	server, hits := webhookHitCounter(t)
	t.Setenv("WEBHOOK_URL", server.URL)

	mediaPath := filepath.Join(t.TempDir(), "private.jpg")
	if err := os.WriteFile(mediaPath, []byte("private media"), 0o600); err != nil {
		t.Fatalf("write private media fixture: %v", err)
	}

	SendWebhook(
		messageStore,
		eventTimestamp,
		"15551234567",
		"private text",
		chatJID,
		false,
		"",
		"",
		"",
		nil,
		nil,
	)
	SendWebhookWithMedia(
		messageStore,
		eventTimestamp,
		"15551234567",
		"private caption",
		chatJID,
		false,
		"",
		"",
		"",
		nil,
		nil,
		"private-image",
		"image",
		"image/jpeg",
		"private.jpg",
		mediaPath,
	)
	SendReactionWebhook(
		messageStore,
		eventTimestamp,
		"15551234567",
		chatJID,
		false,
		"private-reaction",
		"private-target",
		"👍",
	)

	if got := hits.Load(); got != 0 {
		t.Fatalf("default-denied webhook calls = %d, want 0", got)
	}
}

func TestHandleMessageBlockedWebhooksStillPersistLocally(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		server, hits := webhookHitCounter(t)
		t.Setenv("WEBHOOK_URL", server.URL)

		messageStore := newTestMessageStore(t)
		message := buildTextMessage(
			phonePN,
			phonePN,
			types.EmptyJID,
			types.EmptyJID,
			false,
			"private text",
		)

		handleMessage(newTestClient(&mockLIDStore{}), messageStore, message, testLogger())

		if count := queryMessageCount(messageStore, phonePN.String()); count != 1 {
			t.Fatalf("stored text messages = %d, want 1", count)
		}
		if got := hits.Load(); got != 0 {
			t.Fatalf("blocked text webhook calls = %d, want 0", got)
		}
	})

	t.Run("image", func(t *testing.T) {
		server, hits := webhookHitCounter(t)
		t.Setenv("WEBHOOK_URL", server.URL)

		messageStore := newTestMessageStore(t)
		message := buildImageMessage(phonePN, phonePN, false, "private image")

		handleMessage(newTestClient(&mockLIDStore{}), messageStore, message, testLogger())

		if count := queryMessageCount(messageStore, phonePN.String()); count != 1 {
			t.Fatalf("stored image messages = %d, want 1", count)
		}
		if got := hits.Load(); got != 0 {
			t.Fatalf("blocked image webhook calls = %d, want 0", got)
		}
	})

	t.Run("reaction", func(t *testing.T) {
		server, hits := webhookHitCounter(t)
		t.Setenv("WEBHOOK_URL", server.URL)

		messageStore := newTestMessageStore(t)
		message := buildReactionMessage(
			phonePN,
			phonePN,
			false,
			"private-target",
			"👍",
		)

		handleMessage(newTestClient(&mockLIDStore{}), messageStore, message, testLogger())

		mediaType, _, found := queryMessageMediaTypeAndFilename(
			messageStore,
			phonePN.String(),
			message.Info.ID,
		)
		if !found || mediaType != "reaction" {
			t.Fatalf("blocked reaction was not preserved locally: found=%v media_type=%q", found, mediaType)
		}
		if got := hits.Load(); got != 0 {
			t.Fatalf("blocked reaction webhook calls = %d, want 0", got)
		}
	})
}

func TestHandleMessageWebhookUsesCanonicalChatAndEventTimestamp(t *testing.T) {
	server, webhookPayloads := captureWebhook(t)
	t.Setenv("WEBHOOK_URL", server.URL)

	messageStore := newTestMessageStore(t)
	client := newTestClient(&mockLIDStore{})
	grantTimestamp := time.Unix(1_720_001_000, 0).UTC()

	if err := messageStore.StoreChat(
		phonePN.String(),
		"Canonical chat",
		grantTimestamp,
	); err != nil {
		t.Fatalf("seed canonical chat: %v", err)
	}
	if err := messageStore.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{
			ChatJID: phonePN.String(),
			ReadNew: true,
		}},
		grantTimestamp,
	); err != nil {
		t.Fatalf("grant new-message access: %v", err)
	}

	oldMessage := buildTextMessage(
		phoneLID,
		phoneLID,
		phonePN,
		types.EmptyJID,
		false,
		"old private message",
	)
	oldMessage.Info.ID = "old-private-message"
	oldMessage.Info.Timestamp = grantTimestamp.Add(-time.Second)
	handleMessage(client, messageStore, oldMessage, testLogger())

	select {
	case payload := <-webhookPayloads:
		t.Fatalf("old event crossed the read-new boundary: %#v", payload)
	default:
	}

	newMessage := buildTextMessage(
		phoneLID,
		phoneLID,
		phonePN,
		types.EmptyJID,
		false,
		"new allowed message",
	)
	newMessage.Info.ID = "new-allowed-message"
	newMessage.Info.Timestamp = grantTimestamp.Add(time.Second)
	handleMessage(client, messageStore, newMessage, testLogger())

	select {
	case payload := <-webhookPayloads:
		if payload.ChatJID != phonePN.String() {
			t.Fatalf("webhook chat JID = %q, want canonical %q", payload.ChatJID, phonePN.String())
		}
		if payload.Content != "new allowed message" {
			t.Fatalf("webhook content = %q, want new allowed message", payload.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for allowed canonical webhook")
	}

	if count := queryMessageCount(messageStore, phonePN.String()); count != 2 {
		t.Fatalf("canonical locally stored messages = %d, want 2", count)
	}
	if count := queryMessageCount(messageStore, phoneLID.String()); count != 0 {
		t.Fatalf("LID locally stored messages = %d, want 0", count)
	}
}
