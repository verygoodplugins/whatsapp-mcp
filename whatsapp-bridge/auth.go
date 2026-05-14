package main

// Bridge authentication and request validation.
//
// The REST listener binds to 127.0.0.1, but loopback is not a meaningful
// trust boundary on a developer workstation: any local process or browser
// tab (via DNS rebinding) can issue requests. We add two layers:
//
//  1. Bearer-token auth. A 256-bit token is generated at first start and
//     stored at store/.bridge-token (mode 0600). The MCP server reads it
//     either from WHATSAPP_BRIDGE_TOKEN or by reading that file. Every
//     /api/* request must carry "Authorization: Bearer <token>".
//
//  2. Host header allow-list. Even with auth, an attacker who tricks a
//     browser into resolving evil.example.com to 127.0.0.1 (DNS rebinding)
//     could send the loopback request from same-origin context. By
//     restricting Host to {127.0.0.1:<port>, localhost:<port>, [::1]:<port>}
//     we close that hole.
//
// Backwards compatibility: this is a breaking change. Existing deploys must
// either set WHATSAPP_BRIDGE_TOKEN to the printed token in the MCP server
// env, or read the token file. The bridge prints a loud one-time banner at
// startup when it generates a fresh token so users notice.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// tokenFileMode is read/write for owner only — never group/other readable.
const tokenFileMode = 0o600

// tokenFilePath is the on-disk location of the bridge auth token, relative
// to the bridge's working directory. The MCP server reads this file as a
// fallback when WHATSAPP_BRIDGE_TOKEN is unset.
const tokenFilePath = "store/.bridge-token"

// tokenByteLen is the entropy size of generated tokens. 32 bytes (256 bits)
// is overkill for an HMAC-quality secret on a single host but trivial to
// generate and leaves zero margin for guessing attacks.
const tokenByteLen = 32

// loadOrCreateBridgeToken returns the persisted token, generating one if
// the file does not exist yet. A WHATSAPP_BRIDGE_TOKEN env var, if set,
// always wins — useful for ephemeral containers where you want to inject
// the token from outside instead of mounting the file.
func loadOrCreateBridgeToken() (token string, freshlyGenerated bool, err error) {
	if env := strings.TrimSpace(os.Getenv("WHATSAPP_BRIDGE_TOKEN")); env != "" {
		if len(env) < 16 {
			return "", false, errors.New("WHATSAPP_BRIDGE_TOKEN is too short (need at least 16 chars)")
		}
		return env, false, nil
	}

	if data, readErr := os.ReadFile(tokenFilePath); readErr == nil {
		existing := strings.TrimSpace(string(data))
		if existing != "" {
			return existing, false, nil
		}
		// File exists but is empty — fall through to regenerate.
	} else if !os.IsNotExist(readErr) {
		return "", false, fmt.Errorf("read %s: %w", tokenFilePath, readErr)
	}

	// Generate a new token.
	buf := make([]byte, tokenByteLen)
	if _, genErr := rand.Read(buf); genErr != nil {
		return "", false, fmt.Errorf("generate bridge token: %w", genErr)
	}
	newToken := hex.EncodeToString(buf)

	// Ensure parent directory exists. main.go already creates store/ before
	// this is called, but being defensive here keeps the helper testable.
	if mkErr := os.MkdirAll(filepath.Dir(tokenFilePath), 0o755); mkErr != nil {
		return "", false, fmt.Errorf("create token dir: %w", mkErr)
	}
	if writeErr := os.WriteFile(tokenFilePath, []byte(newToken+"\n"), tokenFileMode); writeErr != nil {
		return "", false, fmt.Errorf("write %s: %w", tokenFilePath, writeErr)
	}
	return newToken, true, nil
}

// printTokenBanner prints a high-visibility startup message when a fresh
// token has been written. This is the user's only chance to copy the token
// without having to cat the file, so it's intentionally noisy.
func printTokenBanner(token string, port int) {
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════════════")
	fmt.Println("  WHATSAPP BRIDGE AUTH TOKEN — first-time setup")
	fmt.Println("════════════════════════════════════════════════════════════════════")
	fmt.Printf("  Token:          %s\n", token)
	fmt.Printf("  Stored at:      %s (mode 0600)\n", tokenFilePath)
	fmt.Printf("  Bridge URL:     http://127.0.0.1:%d/api\n", port)
	fmt.Println()
	fmt.Println("  The MCP server must send this token on every request:")
	fmt.Println("    Authorization: Bearer <token>")
	fmt.Println()
	fmt.Println("  Configure the whatsapp-mcp-server with one of:")
	fmt.Println("    export WHATSAPP_BRIDGE_TOKEN=<token>")
	fmt.Printf("    (or let it read %s automatically)\n", tokenFilePath)
	fmt.Println("════════════════════════════════════════════════════════════════════")
	fmt.Println()
}

// withAuth wraps an http.HandlerFunc with bearer-token + Host validation.
// The set of accepted Host values is computed once at server start; we pass
// it in instead of recomputing per request.
func withAuth(token string, allowedHosts map[string]struct{}, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, allowedHosts) {
			// 403 — request reached us with a Host we don't recognise.
			// Likely a DNS-rebinding attempt or misconfigured proxy.
			http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
			return
		}
		if !checkBearerToken(r.Header.Get("Authorization"), token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="whatsapp-bridge"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// hostAllowed performs an exact, case-insensitive match against the
// allow-list. r.Host already includes the port for non-default ports, which
// is exactly what we want — listening on :8080 means "localhost" without a
// port should not match.
func hostAllowed(host string, allowed map[string]struct{}) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	_, ok := allowed[h]
	return ok
}

// checkBearerToken returns true iff the Authorization header carries our
// token. Uses constant-time comparison to avoid timing leaks (the token is
// long enough that this is largely paranoia, but it's free).
func checkBearerToken(authHeader, expected string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return false
	}
	got := strings.TrimSpace(authHeader[len(prefix):])
	if len(got) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

// buildAllowedHosts returns the static allow-list for a given bind port.
// We accept the three loopback spellings (IPv4, name, IPv6) because the
// MCP server's choice of WHATSAPP_API_URL determines which Host header the
// underlying HTTP client emits.
func buildAllowedHosts(port int) map[string]struct{} {
	return map[string]struct{}{
		fmt.Sprintf("127.0.0.1:%d", port): {},
		fmt.Sprintf("localhost:%d", port): {},
		fmt.Sprintf("[::1]:%d", port):     {},
	}
}
