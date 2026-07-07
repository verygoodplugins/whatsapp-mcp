package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// setWebhookAuthToken sets the package-level outbound webhook token for the
// duration of a test and restores the previous value on cleanup.
func setWebhookAuthToken(t *testing.T, token string) {
	t.Helper()
	prev := webhookAuthToken
	webhookAuthToken = token
	t.Cleanup(func() { webhookAuthToken = prev })
}

// TestSendWebhookAttachesBridgeTokenHeader verifies that outbound webhook POSTs
// carry the shared bridge token as an "X-Bridge-Token" header so the hub's
// fail-closed inbound-auth middleware (autohub PR #898) accepts them. The token
// travels on a dedicated header, not Authorization, so it never collides with a
// receiver's own Authorization-based auth (see
// TestSendWebhookPreservesURLBasicAuth).
func TestSendWebhookAttachesBridgeTokenHeader(t *testing.T) {
	const token = "test-bridge-token-1234567890abcdef"

	var gotToken, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Bridge-Token")
		gotContentType = r.Header.Get("Content-Type")
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("WEBHOOK_URL", srv.URL)
	setWebhookAuthToken(t, token)

	SendWebhook("123@s.whatsapp.net", "hello", "123@s.whatsapp.net", false, "", "", "")

	if gotToken != token {
		t.Fatalf("X-Bridge-Token header = %q, want %q", gotToken, token)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type header = %q, want application/json", gotContentType)
	}
}

// TestSendWebhookOmitsBridgeTokenHeaderWhenNoToken verifies that when no bridge
// token is configured the webhook still fires WITHOUT an X-Bridge-Token header,
// so deployments that predate the token rollout keep working.
func TestSendWebhookOmitsBridgeTokenHeaderWhenNoToken(t *testing.T) {
	var gotToken string
	var received bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		gotToken = r.Header.Get("X-Bridge-Token")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("WEBHOOK_URL", srv.URL)
	setWebhookAuthToken(t, "")

	SendWebhook("123@s.whatsapp.net", "hi", "123@s.whatsapp.net", false, "", "", "")

	if !received {
		t.Fatal("webhook was not delivered")
	}
	if gotToken != "" {
		t.Fatalf("expected no X-Bridge-Token header, got %q", gotToken)
	}
}

// TestSendWebhookPreservesURLBasicAuth is a regression test for a Codex review
// finding on PR #153: net/http automatically derives an "Authorization: Basic"
// header from credentials embedded in the webhook URL (http://user:pass@host/...)
// whenever the outgoing request's Authorization header is otherwise unset. An
// earlier version of this fix sent the bridge token via Authorization, which
// silently clobbered that behavior for any receiver relying on URL userinfo.
// Sending the token as X-Bridge-Token instead must leave Authorization
// untouched so Go's built-in URL-credential handling keeps working.
func TestSendWebhookPreservesURLBasicAuth(t *testing.T) {
	const user, pass = "hookuser", "hookpass"
	const token = "test-bridge-token-1234567890abcdef"

	var gotAuth, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotToken = r.Header.Get("X-Bridge-Token")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}
	u.User = url.UserPassword(user, pass)

	t.Setenv("WEBHOOK_URL", u.String())
	setWebhookAuthToken(t, token)

	SendWebhook("123@s.whatsapp.net", "hello", "123@s.whatsapp.net", false, "", "", "")

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	if gotAuth != wantAuth {
		t.Fatalf("Authorization header = %q, want %q (URL basic auth must survive)", gotAuth, wantAuth)
	}
	if gotToken != token {
		t.Fatalf("X-Bridge-Token header = %q, want %q", gotToken, token)
	}
}
