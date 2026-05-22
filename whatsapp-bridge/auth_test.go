package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckBearerToken(t *testing.T) {
	const token = "abcd1234abcd1234abcd1234abcd1234"
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"empty header", "", false},
		{"wrong scheme", "Basic " + token, false},
		{"correct token", "Bearer " + token, true},
		{"correct token with whitespace", "Bearer  " + token + "  ", true},
		{"wrong token", "Bearer xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", false},
		{"shorter token", "Bearer abcd", false},
		{"longer token", "Bearer " + token + "extra", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkBearerToken(tc.header, token); got != tc.want {
				t.Fatalf("checkBearerToken(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestHostAllowed(t *testing.T) {
	allowed := buildAllowedHosts(8080)
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"LocalHost:8080", true},
		{"[::1]:8080", true},
		{"127.0.0.1:9090", false}, // wrong port
		{"localhost", false},      // missing port
		{"example.com:8080", false},
		{"127.0.0.1.evil.com:8080", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			if got := hostAllowed(tc.host, allowed); got != tc.want {
				t.Fatalf("hostAllowed(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestWithAuthRejectsMissingToken(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	allowed := buildAllowedHosts(8080)
	called := false
	handler := withAuth(token, allowed, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/health", nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Fatalf("expected WWW-Authenticate Bearer challenge, got %q", got)
	}
	if called {
		t.Fatal("handler should not have been invoked")
	}
}

func TestWithAuthRejectsBadHost(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	allowed := buildAllowedHosts(8080)
	handler := withAuth(token, allowed, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "http://evil.example.com/api/health", nil)
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for bad Host, got %d", rec.Code)
	}
}

func TestWithAuthAcceptsValidRequest(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	allowed := buildAllowedHosts(8080)
	handler := withAuth(token, allowed, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/health", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestLoadOrCreateBridgeTokenFromEnv(t *testing.T) {
	const fixed = "env-supplied-token-1234567890"
	t.Setenv("WHATSAPP_BRIDGE_TOKEN", fixed)

	tok, fresh, err := loadOrCreateBridgeToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != fixed {
		t.Fatalf("expected env token, got %q", tok)
	}
	if fresh {
		t.Fatal("env-provided token should not be reported as freshly generated")
	}
}

func TestLoadOrCreateBridgeTokenRejectsShortEnv(t *testing.T) {
	t.Setenv("WHATSAPP_BRIDGE_TOKEN", "tooShort")
	if _, _, err := loadOrCreateBridgeToken(); err == nil {
		t.Fatal("expected error for too-short token, got nil")
	}
}
