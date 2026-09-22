package main

import (
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestHandleMessage_StoreFailureSkipsMediaDownloadsButForwardsWebhook(t *testing.T) {
	for _, tc := range []struct {
		name          string
		msg           func() *events.Message
		wantMessageID string
		wantContent   string
	}{
		{
			name: "synchronous image download",
			msg: func() *events.Message {
				msg := buildImageMessage(phonePN, phonePN, false, "")
				msg.Message.ImageMessage.URL = proto.String("https://example.invalid/image")
				msg.Message.ImageMessage.MediaKey = []byte("test-media-key")
				return msg
			},
			wantMessageID: "test-img-001",
		},
		{
			name: "background document download",
			msg: func() *events.Message {
				msg := buildImageMessage(phonePN, phonePN, false, "")
				msg.Message = &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
					Caption:  proto.String("invoice attached"),
					FileName: proto.String("invoice.pdf"),
					URL:      proto.String("https://example.invalid/document"),
					MediaKey: []byte("test-media-key"),
				}}
				return msg
			},
			wantMessageID: "test-img-001",
			wantContent:   "invoice attached",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, webhookCh := captureWebhook(t)
			t.Setenv("WEBHOOK_URL", srv.URL)

			client := newTestClient(&mockLIDStore{})
			ms := newTestMessageStore(t)
			// Make StoreMessage fail deterministically while StoreChat still works.
			if _, err := ms.db.Exec("DROP TABLE messages"); err != nil {
				t.Fatalf("drop messages: %v", err)
			}

			var downloadCalls atomic.Int32
			var backgroundSchedules atomic.Int32
			originalDownload := downloadMediaForMessage
			originalSchedule := scheduleMediaDownload
			downloadMediaForMessage = func(_ *whatsmeow.Client, _ *MessageStore, _ string, _ string) (bool, string, string, string, error) {
				downloadCalls.Add(1)
				return false, "", "", "", nil
			}
			scheduleMediaDownload = func(func()) { backgroundSchedules.Add(1) }
			t.Cleanup(func() {
				downloadMediaForMessage = originalDownload
				scheduleMediaDownload = originalSchedule
			})

			handleMessage(client, ms, tc.msg(), testLogger())

			if got := downloadCalls.Load(); got != 0 {
				t.Fatalf("downloadMediaForMessage called %d times after StoreMessage failed", got)
			}
			if got := backgroundSchedules.Load(); got != 0 {
				t.Fatalf("background media download scheduled %d times after StoreMessage failed", got)
			}

			select {
			case payload := <-webhookCh:
				if payload.MessageID != tc.wantMessageID {
					t.Errorf("expected messageId=%q, got %q", tc.wantMessageID, payload.MessageID)
				}
				if payload.Content != tc.wantContent {
					t.Errorf("expected content=%q, got %q", tc.wantContent, payload.Content)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("webhook must still be sent when the local store fails")
			}
		})
	}
}

func TestHandleMessage_StoredImageDownloadsAndForwardsWebhook(t *testing.T) {
	srv, webhookCh := captureWebhook(t)
	t.Setenv("WEBHOOK_URL", srv.URL)

	client := newTestClient(&mockLIDStore{})
	ms := newTestMessageStore(t)
	msg := buildImageMessage(phonePN, phonePN, false, "")
	msg.Message.ImageMessage.URL = proto.String("https://example.invalid/image")
	msg.Message.ImageMessage.MediaKey = []byte("test-media-key")

	var downloadCalls atomic.Int32
	originalDownload := downloadMediaForMessage
	downloadMediaForMessage = func(_ *whatsmeow.Client, _ *MessageStore, messageID, chatJID string) (bool, string, string, string, error) {
		if messageID != msg.Info.ID || chatJID != phonePN.String() {
			t.Errorf("download called for %s in %s, want %s in %s", messageID, chatJID, msg.Info.ID, phonePN)
		}
		downloadCalls.Add(1)
		return true, "", "", "", nil
	}
	t.Cleanup(func() { downloadMediaForMessage = originalDownload })

	handleMessage(client, ms, msg, testLogger())

	if got := downloadCalls.Load(); got != 1 {
		t.Fatalf("downloadMediaForMessage called %d times, want 1 after a successful StoreMessage", got)
	}
	if count := queryMessageCount(ms, phonePN.String()); count != 1 {
		t.Fatalf("expected stored message after successful StoreMessage, got %d", count)
	}
	select {
	case payload := <-webhookCh:
		if payload.MessageID != msg.Info.ID {
			t.Errorf("expected messageId=%q, got %q", msg.Info.ID, payload.MessageID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook must be sent after a successful store and media download")
	}
}
