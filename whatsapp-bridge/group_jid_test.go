package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveGroupRequest runs one request through a mux built by register. A nil
// client is enough: every case returns at or before the connection check,
// and Client.IsConnected is nil-safe (returns false).
func serveGroupRequest(t *testing.T, register func(*http.ServeMux), method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	register(mux)
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, reader))
	return rec
}

func TestParseGroupJID(t *testing.T) {
	if jid, err := parseGroupJID("120363000000000000@g.us"); err != nil || jid.User != "120363000000000000" {
		t.Fatalf("valid group JID: got %v, %v", jid, err)
	}
	for _, raw := range []string{"", "15551234567", "15551234567@s.whatsapp.net", "@g.us"} {
		if _, err := parseGroupJID(raw); err == nil {
			t.Errorf("parseGroupJID(%q) = nil error, want error", raw)
		}
	}
}
