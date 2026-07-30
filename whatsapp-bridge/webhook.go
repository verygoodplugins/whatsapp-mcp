package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// maxMediaBase64Bytes is the maximum file size that will be base64-encoded and
// included in a webhook payload. Files larger than this limit are skipped to
// avoid excessive memory use and oversized HTTP requests.
const maxMediaBase64Bytes = 10 * 1024 * 1024 // 10 MB

// webhookClient is used for all outbound webhook POSTs. The 30-second timeout
// prevents a slow or unreachable endpoint from blocking message handling
// indefinitely. Redirects are never followed: WEBHOOK_URL is a single
// operator-configured endpoint, not a browsable URL, and following a 3xx
// would forward X-Bridge-Token to whatever host the redirect names — Go only
// strips Authorization/Cookie on cross-origin redirects, not custom headers.
var webhookClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// webhookAuthToken is the shared bridge token attached as an
// "X-Bridge-Token" header to every outbound webhook POST. It is populated
// once at startup from loadOrCreateBridgeToken() (see auth.go and main.go) —
// the same token the bridge already requires on inbound /api/* requests.
// When empty (no token configured yet) the header is omitted so deployments
// that predate the token rollout keep working. The receiving hub enforces
// this token on its inbound webhook route once its own WHATSAPP_BRIDGE_TOKEN
// is set to the matching value (autohub PR #898), which accepts the token via
// this header or "Authorization: Bearer". A dedicated header is used here
// (rather than Authorization) so it never collides with a receiver's own
// Authorization-based auth — e.g. HTTP Basic auth that net/http derives
// automatically from credentials embedded in WEBHOOK_URL.
var webhookAuthToken string

// defaultWebhookURL is used when WEBHOOK_URL is not set. It is a var (not a
// const) so tests can point it at a local test server. It must never receive
// the bridge token: unlike an operator-configured WEBHOOK_URL, nothing has
// vetted this address, so any other local process that happens to bind this
// port could otherwise capture the token just by being reachable.
var defaultWebhookURL = "http://localhost:8769/whatsapp/webhook"

// WebhookPayload represents the data sent to the webhook
type WebhookPayload struct {
	EventType       string   `json:"eventType,omitempty"`
	Sender          string   `json:"sender"`
	Content         string   `json:"content"`
	ChatJID         string   `json:"chatJID"`
	IsFromMe        bool     `json:"isFromMe"`
	QuotedMessageId string   `json:"quotedMessageId,omitempty"`
	QuotedSender    string   `json:"quotedSender,omitempty"`
	QuotedContent   string   `json:"quotedContent,omitempty"`
	QuotedIsFromMe  *bool    `json:"quotedIsFromMe,omitempty"`
	MentionedJIDs   []string `json:"mentionedJids,omitempty"`
	// Media fields - populated when the message contains an image attachment
	MessageID     string `json:"messageId,omitempty"`
	MediaType     string `json:"mediaType,omitempty"`
	MimeType      string `json:"mimeType,omitempty"`
	MediaFilename string `json:"mediaFilename,omitempty"`
	MediaBase64   string `json:"mediaBase64,omitempty"`
	// Reaction fields - populated when EventType is "reaction".
	ReactionToMessageID string  `json:"reactionToMessageId,omitempty"`
	ReactionEmoji       *string `json:"reactionEmoji,omitempty"`
	ReactionRemoved     *bool   `json:"reactionRemoved,omitempty"`
}

// sendWebhookPayload authorizes, builds, marshals and POSTs a WebhookPayload
// to the configured webhook URL.
//
// Authorization lives at this lowest-level egress point so every webhook
// event type fails closed. The builder is evaluated only after access is
// granted, which also avoids reading and base64-encoding media from blocked
// chats.
func sendWebhookPayload(
	messageStore *MessageStore,
	eventTimestamp time.Time,
	chatJID string,
	buildPayload func() WebhookPayload,
) {
	if messageStore == nil || messageStore.db == nil {
		fmt.Printf("⚠ Webhook suppressed for %s: MCP access store unavailable\n", chatJID)
		return
	}
	if eventTimestamp.IsZero() {
		fmt.Printf("⚠ Webhook suppressed for %s: event timestamp unavailable\n", chatJID)
		return
	}
	if buildPayload == nil {
		fmt.Printf("⚠ Webhook suppressed for %s: payload builder unavailable\n", chatJID)
		return
	}

	parsedChatJID, err := types.ParseJID(chatJID)
	if err != nil || parsedChatJID.ToNonAD().String() != chatJID {
		fmt.Printf("⚠ Webhook suppressed: non-canonical chat JID %q\n", chatJID)
		return
	}

	allowed, err := messageStore.CanMCPReadTimestamp(chatJID, eventTimestamp)
	if err != nil {
		fmt.Printf("⚠ Webhook suppressed for %s: failed to check MCP read access: %v\n", chatJID, err)
		return
	}
	if !allowed {
		fmt.Printf("🔒 Webhook suppressed for %s: MCP read access denied\n", chatJID)
		return
	}

	payload := buildPayload()
	if payload.ChatJID != chatJID {
		fmt.Printf(
			"⚠ Webhook suppressed: authorized chat JID %q does not match payload chat JID %q\n",
			chatJID,
			payload.ChatJID,
		)
		return
	}

	webhookURL := os.Getenv("WEBHOOK_URL")
	explicitlyConfigured := webhookURL != ""
	if !explicitlyConfigured {
		webhookURL = defaultWebhookURL
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("Error marshaling webhook payload: %v\n", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("Error building webhook request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// Authenticate to the hub's fail-closed inbound webhook route with the
	// shared bridge token, via a dedicated header so it can never clobber a
	// receiver's own Authorization-based auth (see webhookAuthToken doc
	// comment above). Only attach it when BOTH a token is configured AND
	// WEBHOOK_URL was explicitly set by the operator — the bridge token also
	// authorizes /api/* calls like sending messages, and the implicit local
	// default is not a destination anyone vetted, so it must never receive it.
	if webhookAuthToken != "" && explicitlyConfigured {
		req.Header.Set("X-Bridge-Token", webhookAuthToken)
	}

	resp, err := webhookClient.Do(req)
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
func SendWebhook(
	messageStore *MessageStore,
	eventTimestamp time.Time,
	sender, content, chatJID string,
	isFromMe bool,
	quotedMessageId, quotedSender, quotedContent string,
	quotedIsFromMe *bool,
	mentionedJIDs []string,
) {
	sendWebhookPayload(messageStore, eventTimestamp, chatJID, func() WebhookPayload {
		return WebhookPayload{
			Sender:          sender,
			Content:         content,
			ChatJID:         chatJID,
			IsFromMe:        isFromMe,
			QuotedMessageId: quotedMessageId,
			QuotedSender:    quotedSender,
			QuotedContent:   quotedContent,
			QuotedIsFromMe:  quotedIsFromMe,
			MentionedJIDs:   mentionedJIDs,
		}
	})
}

// SendWebhookWithMedia sends a message to the webhook endpoint including base64-encoded
// image data read from localPath. If localPath is empty or unreadable the webhook is
// still sent – just without the MediaBase64 field so the text caption is not lost.
func SendWebhookWithMedia(
	messageStore *MessageStore,
	eventTimestamp time.Time,
	sender, content, chatJID string,
	isFromMe bool,
	quotedMessageId, quotedSender, quotedContent string,
	quotedIsFromMe *bool, mentionedJIDs []string,
	messageID, mediaType, mimeType, mediaFilename, localPath string,
) {
	sendWebhookPayload(messageStore, eventTimestamp, chatJID, func() WebhookPayload {
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

		return WebhookPayload{
			Sender:          sender,
			Content:         content,
			ChatJID:         chatJID,
			IsFromMe:        isFromMe,
			QuotedMessageId: quotedMessageId,
			QuotedSender:    quotedSender,
			QuotedContent:   quotedContent,
			QuotedIsFromMe:  quotedIsFromMe,
			MentionedJIDs:   mentionedJIDs,
			MessageID:       messageID,
			MediaType:       mediaType,
			MimeType:        mimeType,
			MediaFilename:   mediaFilename,
			MediaBase64:     mediaBase64,
		}
	})
}

// SendReactionWebhook sends a typed reaction event to the webhook endpoint.
func SendReactionWebhook(
	messageStore *MessageStore,
	eventTimestamp time.Time,
	sender, chatJID string,
	isFromMe bool,
	messageID, reactionToMessageID, emoji string,
) {
	removed := emoji == ""
	sendWebhookPayload(messageStore, eventTimestamp, chatJID, func() WebhookPayload {
		return WebhookPayload{
			EventType:           "reaction",
			Sender:              sender,
			Content:             emoji,
			ChatJID:             chatJID,
			IsFromMe:            isFromMe,
			MessageID:           messageID,
			MediaType:           "reaction",
			ReactionToMessageID: reactionToMessageID,
			ReactionEmoji:       &emoji,
			ReactionRemoved:     &removed,
		}
	})
}

// In main.go, handleMessage forwards webhooks for messages with text content.
// It will forward self-sent messages when the env var FORWARD_SELF=true.
