package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// maxMediaBase64Bytes is the maximum file size that will be base64-encoded and
// included in a webhook payload. Files larger than this limit are skipped to
// avoid excessive memory use and oversized HTTP requests.
const maxMediaBase64Bytes = 10 * 1024 * 1024 // 10 MB

// webhookClient is used for all outbound webhook POSTs. The 30-second timeout
// prevents a slow or unreachable endpoint from blocking message handling
// indefinitely.
var webhookClient = &http.Client{Timeout: 30 * time.Second}

// WebhookPayload represents the data sent to the webhook
type WebhookPayload struct {
	Sender          string `json:"sender"`
	Content         string `json:"content"`
	ChatJID         string `json:"chatJID"`
	IsFromMe        bool   `json:"isFromMe"`
	QuotedMessageId string `json:"quotedMessageId,omitempty"`
	QuotedSender    string `json:"quotedSender,omitempty"`
	QuotedContent   string `json:"quotedContent,omitempty"`
	// Media fields - populated when the message contains an image attachment
	MessageID     string `json:"messageId,omitempty"`
	MediaType     string `json:"mediaType,omitempty"`
	MimeType      string `json:"mimeType,omitempty"`
	MediaFilename string `json:"mediaFilename,omitempty"`
	MediaBase64   string `json:"mediaBase64,omitempty"`
}

// validateWebhookURL parses the configured webhook URL and rejects values that
// would silently send chat content (and base64 image data) somewhere unsafe.
//
// Rules:
//   - scheme must be http or https
//   - host must be present and parseable
//   - if the host is non-loopback, scheme MUST be https (no plain-HTTP message
//     content over the network). Set WEBHOOK_ALLOW_INSECURE=true to override
//     for local-network/dev setups; this is logged loudly at startup.
func validateWebhookURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("WEBHOOK_URL is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("WEBHOOK_URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("WEBHOOK_URL is missing a host")
	}

	host := u.Hostname()
	loopback := host == "localhost" || host == "::1"
	if !loopback {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			loopback = true
		}
	}

	if !loopback && u.Scheme != "https" {
		allow := strings.EqualFold(strings.TrimSpace(os.Getenv("WEBHOOK_ALLOW_INSECURE")), "true")
		if !allow {
			return nil, fmt.Errorf(
				"WEBHOOK_URL points to a non-loopback host %q over plain HTTP; "+
					"refusing to leak message content. Use https:// or set "+
					"WEBHOOK_ALLOW_INSECURE=true to override at your own risk",
				host,
			)
		}
		fmt.Printf("⚠ WEBHOOK_ALLOW_INSECURE=true: sending message content over plain HTTP to %s\n", host)
	}
	return u, nil
}

// webhookURLOnce caches the validated webhook URL so we don't re-validate (and
// re-log) for every message. validateErr is sticky: if the configured URL is
// invalid we log once and skip webhook delivery for the lifetime of the process.
var (
	webhookURLOnce  sync.Once
	webhookURLValue string
	webhookURLErr   error
)

func resolveWebhookURL() (string, error) {
	webhookURLOnce.Do(func() {
		raw := os.Getenv("WEBHOOK_URL")
		if raw == "" {
			raw = "http://localhost:8769/whatsapp/webhook"
		}
		u, err := validateWebhookURL(raw)
		if err != nil {
			webhookURLErr = err
			fmt.Printf("⚠ Disabling webhook: %v\n", err)
			return
		}
		webhookURLValue = u.String()
	})
	return webhookURLValue, webhookURLErr
}

// sendWebhookPayload marshals and POSTs a WebhookPayload to the configured webhook URL.
func sendWebhookPayload(payload WebhookPayload) {
	webhookURL, err := resolveWebhookURL()
	if err != nil {
		// Already logged once in resolveWebhookURL; stay quiet on the hot path.
		return
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("Error marshaling webhook payload: %v\n", err)
		return
	}

	resp, err := webhookClient.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("Error sending webhook: %v\n", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 200 {
		fmt.Printf("✓ Webhook sent for message from %s\n", payload.Sender)
	} else {
		fmt.Printf("⚠ Webhook failed with status %d\n", resp.StatusCode)
	}
}

// SendWebhook sends a text-only message to the webhook endpoint.
func SendWebhook(sender, content, chatJID string, isFromMe bool, quotedMessageId, quotedSender, quotedContent string) {
	sendWebhookPayload(WebhookPayload{
		Sender:          sender,
		Content:         content,
		ChatJID:         chatJID,
		IsFromMe:        isFromMe,
		QuotedMessageId: quotedMessageId,
		QuotedSender:    quotedSender,
		QuotedContent:   quotedContent,
	})
}

// SendWebhookWithMedia sends a message to the webhook endpoint including base64-encoded
// image data read from localPath. If localPath is empty or unreadable the webhook is
// still sent – just without the MediaBase64 field so the text caption is not lost.
func SendWebhookWithMedia(
	sender, content, chatJID string,
	isFromMe bool,
	quotedMessageId, quotedSender, quotedContent string,
	messageID, mediaType, mimeType, mediaFilename, localPath string,
) {
	var mediaBase64 string
	if localPath != "" {
		info, statErr := os.Stat(localPath)
		if statErr != nil {
			fmt.Printf("⚠ Could not stat media file for base64 encoding: %v\n", statErr)
		} else if info.Size() > maxMediaBase64Bytes {
			fmt.Printf("⚠ Media file too large for base64 encoding (%d bytes), skipping MediaBase64\n", info.Size())
		} else if data, err := os.ReadFile(localPath); err == nil {
			mediaBase64 = base64.StdEncoding.EncodeToString(data)
		} else {
			fmt.Printf("⚠ Could not read media file for base64 encoding: %v\n", err)
		}
	}

	sendWebhookPayload(WebhookPayload{
		Sender:          sender,
		Content:         content,
		ChatJID:         chatJID,
		IsFromMe:        isFromMe,
		QuotedMessageId: quotedMessageId,
		QuotedSender:    quotedSender,
		QuotedContent:   quotedContent,
		MessageID:       messageID,
		MediaType:       mediaType,
		MimeType:        mimeType,
		MediaFilename:   mediaFilename,
		MediaBase64:     mediaBase64,
	})
}

// In main.go, handleMessage forwards webhooks for messages with text content.
// It will forward self-sent messages when the env var FORWARD_SELF=true.
