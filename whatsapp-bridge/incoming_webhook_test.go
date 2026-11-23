package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCapture) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestIncomingWebhookDisabledWhenUnset(t *testing.T) {
	logs := &logCapture{}
	cases := []struct{ url, jids string }{
		{"", ""},
		{"", "123@s.whatsapp.net"},
		{"https://example.invalid/hook", ""},
		{"https://example.invalid/hook", " , "},
		{"ftp://example.invalid/secret", "123@s.whatsapp.net"},
		{"not a url secret-token", "123@s.whatsapp.net"},
	}
	for _, c := range cases {
		w := newIncomingWebhook(c.url, c.jids, logs.logf)
		if w != nil {
			t.Fatalf("expected disabled for url=%q jids=%q", c.url, c.jids)
		}
		if w.matches("123@s.whatsapp.net") || w.notifyAsync(IncomingWebhookPayload{}) {
			t.Fatalf("nil webhook must be a no-op")
		}
	}
	if strings.Contains(logs.all(), "secret") {
		t.Fatalf("URL leaked into logs: %s", logs.all())
	}
}

func TestIncomingWebhookMatches(t *testing.T) {
	w := newIncomingWebhook("http://127.0.0.1:1/x", " 111@s.whatsapp.net, 222@g.us ,", (&logCapture{}).logf)
	if w == nil {
		t.Fatal("expected enabled")
	}
	if !w.matches("111@s.whatsapp.net") || !w.matches("x@lid", "222@g.us") {
		t.Fatal("expected match")
	}
	if w.matches("333@s.whatsapp.net") {
		t.Fatal("unexpected match")
	}
}

func TestIncomingWebhookDeliversPayload(t *testing.T) {
	got := make(chan IncomingWebhookPayload, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("bad request: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		var p IncomingWebhookPayload
		if err := json.Unmarshal(body, &p); err != nil {
			t.Errorf("bad json: %v", err)
		}
		got <- p
	}))
	defer srv.Close()

	w := newIncomingWebhook(srv.URL+"/hook", "111@s.whatsapp.net", (&logCapture{}).logf)
	ts := time.Date(2026, 11, 23, 10, 0, 0, 0, time.FixedZone("x", 7*3600))
	payload := buildIncomingWebhookPayload("111@s.whatsapp.net", " Alice ",
		types.NewJID("111", types.DefaultUserServer), ts, "MID1", "hello", "")
	if !w.notifyAsync(payload) {
		t.Fatal("expected dispatch")
	}
	select {
	case p := <-got:
		want := IncomingWebhookPayload{ChatJID: "111@s.whatsapp.net", SenderName: "Alice", SenderPhone: "111",
			Timestamp: "2026-11-23T03:00:00Z", MessageID: "MID1", Content: "hello"}
		if p != want {
			t.Fatalf("got %+v want %+v", p, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook not delivered")
	}
}

func TestIncomingWebhookJSONKeys(t *testing.T) {
	b, _ := json.Marshal(IncomingWebhookPayload{})
	for _, k := range []string{"chat_jid", "sender_name", "sender_phone", "timestamp", "message_id", "content", "media_type"} {
		if !strings.Contains(string(b), `"`+k+`"`) {
			t.Fatalf("missing key %s in %s", k, b)
		}
	}
}

func TestIncomingWebhookLIDSenderHasNoPhone(t *testing.T) {
	p := buildIncomingWebhookPayload("c", "", types.NewJID("999", types.HiddenUserServer), time.Unix(0, 0), "m", "", "image")
	if p.SenderPhone != "" || p.MediaType != "image" {
		t.Fatalf("unexpected payload %+v", p)
	}
}

func TestIncomingWebhookFailureDoesNotLogURL(t *testing.T) {
	// Closed server: connection refused, error must not contain the path token.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()
	logs := &logCapture{}
	w := newIncomingWebhook(addr+"/secret-token-xyz", "111@s.whatsapp.net", logs.logf)
	w.deliver(IncomingWebhookPayload{MessageID: "M"})
	out := logs.all()
	if !strings.Contains(out, "delivery failed for message M") {
		t.Fatalf("expected failure log, got %q", out)
	}
	if strings.Contains(out, "secret-token-xyz") {
		t.Fatalf("URL leaked into logs: %q", out)
	}
}

func TestIncomingWebhookNonBlockingWhenSaturated(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	logs := &logCapture{}
	w := newIncomingWebhook(srv.URL, "a", logs.logf)
	for i := 0; i < incomingWebhookMaxInFlight; i++ {
		if !w.notifyAsync(IncomingWebhookPayload{MessageID: fmt.Sprint(i)}) {
			t.Fatalf("slot %d should be available", i)
		}
	}
	start := time.Now()
	if w.notifyAsync(IncomingWebhookPayload{MessageID: "overflow"}) {
		t.Fatal("expected drop when saturated")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("notifyAsync blocked")
	}
	if !strings.Contains(logs.all(), "dropped message overflow") {
		t.Fatalf("expected drop log, got %q", logs.all())
	}
}
