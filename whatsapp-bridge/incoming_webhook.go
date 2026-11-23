package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Optional per-chat incoming-message webhook.
//
// Independent of the main WEBHOOK_URL pipeline (webhook.go): it fires only for
// new, live, inbound messages (never self-sent, never history sync) whose chat
// JID is listed in INCOMING_WEBHOOK_JIDS, and POSTs a small fixed JSON shape to
// INCOMING_WEBHOOK_URL. Both env vars unset/empty means the feature is off.
//
// INCOMING_WEBHOOK_URL is treated as a secret (it commonly embeds a token), so
// it is never logged — including inside net/http errors, which quote the URL.

const (
	incomingWebhookTimeout = 10 * time.Second
	// incomingWebhookMaxInFlight bounds concurrent deliveries so a stalled
	// receiver cannot pile up goroutines; excess events are dropped and logged.
	incomingWebhookMaxInFlight = 16
)

// IncomingWebhookPayload is the JSON body POSTed to INCOMING_WEBHOOK_URL.
type IncomingWebhookPayload struct {
	ChatJID     string `json:"chat_jid"`
	SenderName  string `json:"sender_name"`
	SenderPhone string `json:"sender_phone"`
	Timestamp   string `json:"timestamp"` // RFC 3339, UTC
	MessageID   string `json:"message_id"`
	Content     string `json:"content"`
	MediaType   string `json:"media_type"`
}

type incomingWebhook struct {
	url    string
	jids   map[string]struct{}
	client *http.Client
	slots  chan struct{}
	logf   func(format string, args ...any)
}

// incomingWebhookConfig is read once at startup; nil means disabled.
var incomingWebhookConfig = newIncomingWebhookFromEnv()

func newIncomingWebhookFromEnv() *incomingWebhook {
	return newIncomingWebhook(os.Getenv("INCOMING_WEBHOOK_URL"), os.Getenv("INCOMING_WEBHOOK_JIDS"), defaultIncomingWebhookLogf)
}

func defaultIncomingWebhookLogf(format string, args ...any) {
	fmt.Printf("[incoming-webhook] "+format+"\n", args...)
}

// newIncomingWebhook returns nil (disabled) unless rawURL is a valid http(s)
// URL and at least one JID is listed.
func newIncomingWebhook(rawURL, rawJIDs string, logf func(string, ...any)) *incomingWebhook {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	jids := map[string]struct{}{}
	for _, j := range strings.Split(rawJIDs, ",") {
		if j = strings.TrimSpace(j); j != "" {
			jids[j] = struct{}{}
		}
	}
	if len(jids) == 0 {
		logf("INCOMING_WEBHOOK_URL set but INCOMING_WEBHOOK_JIDS is empty: disabled")
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// Do not echo the URL or the parse error (which quotes it).
		logf("INCOMING_WEBHOOK_URL is not a valid http(s) URL: disabled")
		return nil
	}
	logf("enabled for %d chat JID(s)", len(jids))
	return &incomingWebhook{
		url:  rawURL,
		jids: jids,
		client: &http.Client{
			Timeout: incomingWebhookTimeout,
			// Single operator-configured endpoint; never follow redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		slots: make(chan struct{}, incomingWebhookMaxInFlight),
		logf:  logf,
	}
}

// matches reports whether any of the given chat JID forms (e.g. resolved
// phone JID and original LID JID) is in the allow-list.
func (w *incomingWebhook) matches(chatJIDs ...string) bool {
	if w == nil {
		return false
	}
	for _, j := range chatJIDs {
		if _, ok := w.jids[j]; ok {
			return true
		}
	}
	return false
}

// notifyAsync delivers payload in the background. It never blocks the caller:
// if all delivery slots are busy the event is dropped with a log line.
// Returns false when the event was dropped (or the webhook is disabled).
func (w *incomingWebhook) notifyAsync(payload IncomingWebhookPayload) bool {
	if w == nil {
		return false
	}
	select {
	case w.slots <- struct{}{}:
	default:
		w.logf("dropped message %s: too many deliveries in flight", payload.MessageID)
		return false
	}
	go func() {
		defer func() { <-w.slots }()
		defer func() {
			if r := recover(); r != nil {
				w.logf("panic delivering message %s: %v", payload.MessageID, r)
			}
		}()
		w.deliver(payload)
	}()
	return true
}

func (w *incomingWebhook) deliver(payload IncomingWebhookPayload) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.logf("marshal failed for message %s: %v", payload.MessageID, err)
		return
	}
	req, err := http.NewRequest(http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		w.logf("build request failed for message %s", payload.MessageID)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		w.logf("delivery failed for message %s: %v", payload.MessageID, redactURLError(err))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.logf("delivery for message %s got HTTP %d", payload.MessageID, resp.StatusCode)
	}
}

// redactURLError strips the request URL that *url.Error embeds in its message.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// buildIncomingWebhookPayload maps handleMessage's resolved values to the
// payload. sender_phone is only set when the sender resolved to a phone JID
// (LID-only senders have no known number).
func buildIncomingWebhookPayload(chatJID, pushName string, sender types.JID, ts time.Time, messageID, content, mediaType string) IncomingWebhookPayload {
	phone := ""
	if sender.Server == types.DefaultUserServer {
		phone = sender.User
	}
	return IncomingWebhookPayload{
		ChatJID:     chatJID,
		SenderName:  strings.TrimSpace(pushName),
		SenderPhone: phone,
		Timestamp:   ts.UTC().Format(time.RFC3339),
		MessageID:   messageID,
		Content:     content,
		MediaType:   mediaType,
	}
}
