package main

import (
	"io"
	"net/http"
	"net/http/httptest"
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

// TestSendWebhookAttachesBearerToken verifies that outbound webhook POSTs carry
// the shared bridge token as an "Authorization: Bearer <token>" header so the
// hub's fail-closed inbound-auth middleware (autohub PR #898) accepts them.
func TestSendWebhookAttachesBearerToken(t *testing.T) {
	const token = "test-bridge-token-1234567890abcdef"

	var gotAuth, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("WEBHOOK_URL", srv.URL)
	setWebhookAuthToken(t, token)

	SendWebhook("123@s.whatsapp.net", "hello", "123@s.whatsapp.net", false, "", "", "")

	if want := "Bearer " + token; gotAuth != want {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, want)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type header = %q, want application/json", gotContentType)
	}
}

// TestSendWebhookOmitsBearerWhenNoToken verifies that when no bridge token is
// configured the webhook still fires WITHOUT an Authorization header, so
// deployments that predate the token rollout keep working.
func TestSendWebhookOmitsBearerWhenNoToken(t *testing.T) {
	var gotAuth string
	var received bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("WEBHOOK_URL", srv.URL)
	setWebhookAuthToken(t, "")

	SendWebhook("123@s.whatsapp.net", "hi", "123@s.whatsapp.net", false, "", "", "")

	if !received {
		t.Fatal("webhook was not delivered")
	}
	if gotAuth != "" {
		t.Fatalf("expected no Authorization header, got %q", gotAuth)
	}
}
