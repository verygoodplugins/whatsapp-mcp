package main

import (
	"strings"
	"testing"
)

// TestValidateWebhookURL covers the input rules enforced before any outbound
// webhook delivery happens. The validator is conservative on purpose: when in
// doubt it should error and the caller logs+disables delivery rather than
// silently leaking message content.
func TestValidateWebhookURL(t *testing.T) {
	t.Setenv("WEBHOOK_ALLOW_INSECURE", "")

	cases := []struct {
		name      string
		input     string
		wantErr   bool
		errSubstr string
	}{
		{name: "loopback http allowed", input: "http://localhost:8769/whatsapp/webhook"},
		{name: "loopback ipv4 http allowed", input: "http://127.0.0.1:8769/hook"},
		{name: "loopback ipv6 http allowed", input: "http://[::1]:8769/hook"},
		{name: "remote https allowed", input: "https://example.com/hook"},
		{name: "remote http rejected", input: "http://example.com/hook", wantErr: true, errSubstr: "plain HTTP"},
		{name: "ftp scheme rejected", input: "ftp://localhost/hook", wantErr: true, errSubstr: "scheme"},
		{name: "missing host rejected", input: "http:///hook", wantErr: true, errSubstr: "host"},
		{name: "garbage rejected", input: "://not-a-url", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateWebhookURL(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.input)
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("expected error containing %q, got %v", tc.errSubstr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

// TestValidateWebhookURLAllowInsecureOverride verifies that operators can
// explicitly opt out of the HTTPS requirement for non-loopback hosts. This
// matters for trusted local-network webhook receivers where TLS is genuinely
// not feasible (e.g. Home Assistant on a LAN-only network).
func TestValidateWebhookURLAllowInsecureOverride(t *testing.T) {
	t.Setenv("WEBHOOK_ALLOW_INSECURE", "true")
	if _, err := validateWebhookURL("http://192.168.1.50:8123/hook"); err != nil {
		t.Fatalf("expected override to allow plain-HTTP LAN webhook, got error: %v", err)
	}
}
