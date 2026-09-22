package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type mediaDownloadCalls struct{ downloads, scheduled int }

// Run background callbacks inline so a forbidden schedule cannot escape an
// assertion or race with cleanup of the download stub.
func stubAutomaticDownloads(t *testing.T, path string, failFirst bool) *mediaDownloadCalls {
	t.Helper()
	calls := &mediaDownloadCalls{}
	originalDownload, originalSchedule := downloadMediaForMessage, scheduleMediaDownload
	downloadMediaForMessage = func(_ *whatsmeow.Client, _ *MessageStore, _ string, _ string) (bool, string, string, string, error) {
		calls.downloads++
		return !failFirst || calls.downloads > 1, "", "", path, nil
	}
	scheduleMediaDownload = func(download func()) {
		calls.scheduled++
		download()
	}
	t.Cleanup(func() {
		downloadMediaForMessage, scheduleMediaDownload = originalDownload, originalSchedule
	})
	return calls
}

func mediaControlMessage(kind string, chat types.JID) *events.Message {
	msg := buildImageMessage(chat, phonePN, false, "attachment caption")
	if kind == "image" {
		msg.Message.ImageMessage.URL = proto.String("https://example.invalid/image")
		msg.Message.ImageMessage.MediaKey = []byte("test-media-key")
		msg.Message.ImageMessage.Mimetype = proto.String("image/png")
	} else {
		msg.Message = &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
			Caption:  proto.String("attachment caption"),
			FileName: proto.String("example.pdf"),
			URL:      proto.String("https://example.invalid/document"),
			MediaKey: []byte("test-media-key"),
		}}
	}
	return msg
}

func TestHandleMessage_AutomaticMediaControls(t *testing.T) {
	for _, tc := range []struct {
		name, setting, kind string
		webhook, failFirst  bool
		wantCalls, wantJobs int
	}{
		{"disabled image", "false", "image", true, false, 0, 0},
		{"disabled document", "false", "document", true, false, 0, 0},
		{"disabled image and webhook", "false", "image", false, false, 0, 0},
		{"disabled document and webhook", "false", "document", false, false, 0, 0},
		{"default image", "", "image", true, false, 1, 0},
		{"enabled image", "true", "image", true, false, 1, 0},
		{"default document", "", "document", true, false, 1, 1},
		{"default image without webhook", "", "image", false, false, 1, 1},
		{"default document without webhook", "", "document", false, false, 1, 1},
		{"default failed image retry", "", "image", true, true, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WHATSAPP_AUTO_DOWNLOAD_MEDIA", tc.setting)
			t.Setenv("WEBHOOK_ENABLED", "false")
			if tc.webhook {
				t.Setenv("WEBHOOK_ENABLED", "true")
			}
			srv, webhookCh := captureWebhook(t)
			t.Setenv("WEBHOOK_URL", srv.URL)

			imageBytes := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
			path := filepath.Join(t.TempDir(), "image.jpg")
			if err := os.WriteFile(path, imageBytes, 0600); err != nil {
				t.Fatal(err)
			}
			calls := stubAutomaticDownloads(t, path, tc.failFirst)
			ms := newTestMessageStore(t)
			msg := mediaControlMessage(tc.kind, phonePN)
			handleMessage(newTestClient(&mockLIDStore{}), ms, msg, testLogger())

			if count := queryMessageCount(ms, phonePN.String()); count != 1 {
				t.Fatalf("expected stored message, got %d", count)
			}
			if calls.downloads != tc.wantCalls || calls.scheduled != tc.wantJobs {
				t.Fatalf("downloads/scheduled = %d/%d, want %d/%d", calls.downloads, calls.scheduled, tc.wantCalls, tc.wantJobs)
			}
			select {
			case payload := <-webhookCh:
				if !tc.webhook {
					t.Fatal("disabled webhook received a message")
				}
				if payload.Content != "attachment caption" {
					t.Errorf("caption = %q", payload.Content)
				}
				wantBase64 := ""
				if tc.kind == "image" {
					if payload.MediaType != "image" || payload.MessageID != msg.Info.ID || payload.MediaFilename == "" {
						t.Error("image webhook lost attachment metadata")
					}
					wantMIME := "image/png"
					if tc.wantCalls > 0 && !tc.failFirst {
						wantBase64 = base64.StdEncoding.EncodeToString(imageBytes)
						wantMIME = "image/jpeg" // Downloaded bytes override the declared type.
					}
					if payload.MimeType != wantMIME {
						t.Errorf("image MIME type = %q, want %q", payload.MimeType, wantMIME)
					}
				}
				if payload.MediaBase64 != wantBase64 {
					t.Error("webhook image bytes did not match download policy")
				}
			default:
				if tc.webhook {
					t.Fatal("expected metadata/text webhook")
				}
			}
		})
	}
}

func TestHandleMessage_StatusStoredWithoutDownloadsOrWebhooks(t *testing.T) {
	for _, kind := range []string{"image", "document", "text", "reaction"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("WHATSAPP_AUTO_DOWNLOAD_MEDIA", "true")
			t.Setenv("WEBHOOK_ENABLED", "true")
			srv, webhookCh := captureWebhook(t)
			t.Setenv("WEBHOOK_URL", srv.URL)
			calls := stubAutomaticDownloads(t, "", false)
			msg := mediaControlMessage(kind, types.StatusBroadcastJID)
			if kind == "text" {
				msg.Message = &waProto.Message{Conversation: proto.String("status text")}
			} else if kind == "reaction" {
				msg = buildReactionMessage(types.StatusBroadcastJID, phonePN, false, "status-target", "👍")
			}
			ms := newTestMessageStore(t)
			handleMessage(newTestClient(&mockLIDStore{}), ms, msg, testLogger())
			if count := queryMessageCount(ms, types.StatusBroadcastJID.String()); count != 1 {
				t.Fatalf("expected stored status, got %d", count)
			}
			if calls.downloads != 0 || calls.scheduled != 0 {
				t.Fatal("status media must never be downloaded automatically")
			}
			select {
			case <-webhookCh:
				t.Fatal("status must never be forwarded")
			default:
			}
		})
	}
}
