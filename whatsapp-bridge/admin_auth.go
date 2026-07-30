package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	adminTokenFilePath     = "store/.admin-token"
	adminSessionHeader     = "X-WhatsApp-MCP-Admin-Session"
	adminSessionTTL        = 12 * time.Hour
	adminTokenMinLength    = 16
	adminTokenMaxFileBytes = 4096
)

type adminSessionManager struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	token    string
}

func loadOrCreateAdminToken() (token string, freshlyGenerated bool, err error) {
	return loadOrCreateAdminTokenAtPath(adminTokenFilePath)
}

func loadOrCreateAdminTokenAtPath(path string) (token string, freshlyGenerated bool, err error) {
	if env := strings.TrimSpace(os.Getenv("WHATSAPP_ADMIN_TOKEN")); env != "" {
		validated, validateErr := validateAdminTokenValue(env, "WHATSAPP_ADMIN_TOKEN")
		if validateErr != nil {
			return "", false, validateErr
		}
		return validated, false, nil
	}

	existing, readErr := readSecureAdminTokenFile(path)
	if readErr == nil {
		return existing, false, nil
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		return "", false, readErr
	}

	newToken, genErr := randomHexToken()
	if genErr != nil {
		return "", false, genErr
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return "", false, fmt.Errorf("create admin token dir: %w", mkErr)
	}
	if writeErr := writeNewAdminTokenFile(path, newToken); writeErr != nil {
		if errors.Is(writeErr, os.ErrExist) {
			// Another bridge may have won the O_EXCL race. Only accept its
			// token after applying the same strict validation as a normal load.
			existing, readErr = readSecureAdminTokenFile(path)
			if readErr == nil {
				return existing, false, nil
			}
		}
		return "", false, writeErr
	}
	return newToken, true, nil
}

func readSecureAdminTokenFile(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing symlink admin token file %s", path)
	}
	if !before.Mode().IsRegular() {
		return "", fmt.Errorf("admin token path %s is not a regular file", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	opened, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat open admin token %s: %w", path, err)
	}
	if !os.SameFile(before, opened) {
		return "", fmt.Errorf("admin token file %s changed while opening", path)
	}
	if opened.Mode().Perm() != tokenFileMode {
		return "", fmt.Errorf(
			"admin token file %s has insecure permissions %04o (want %04o)",
			path,
			opened.Mode().Perm(),
			tokenFileMode,
		)
	}
	if opened.Size() > adminTokenMaxFileBytes {
		return "", fmt.Errorf("admin token file %s is unexpectedly large", path)
	}

	data, err := io.ReadAll(io.LimitReader(file, adminTokenMaxFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > adminTokenMaxFileBytes {
		return "", fmt.Errorf("admin token file %s is unexpectedly large", path)
	}

	after, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("reinspect %s: %w", path, err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) {
		return "", fmt.Errorf("admin token file %s changed while reading", path)
	}

	return validateAdminTokenValue(string(data), path)
}

func writeNewAdminTokenFile(path, token string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, tokenFileMode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()

	if err := file.Chmod(tokenFileMode); err != nil {
		return fmt.Errorf("secure %s: %w", path, err)
	}
	if _, err := io.WriteString(file, token+"\n"); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	closed = true
	return nil
}

func validateAdminTokenValue(value, source string) (string, error) {
	token := strings.TrimSpace(value)
	if len(token) < adminTokenMinLength {
		return "", fmt.Errorf("%s is too short (need at least %d chars)", source, adminTokenMinLength)
	}
	return token, nil
}

func validateAdminTokenSeparation(bridgeToken, adminToken string) error {
	bridgeToken = strings.TrimSpace(bridgeToken)
	adminToken = strings.TrimSpace(adminToken)
	if constantTimeTokenEqual(bridgeToken, adminToken) {
		return errors.New("WHATSAPP_ADMIN_TOKEN must be different from WHATSAPP_BRIDGE_TOKEN")
	}
	return nil
}

func randomHexToken() (string, error) {
	buf := make([]byte, tokenByteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func printAdminBanner(port int) {
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════════════")
	fmt.Println("  WHATSAPP MCP ADMIN — first-time setup")
	fmt.Println("════════════════════════════════════════════════════════════════════")
	fmt.Printf("  Admin panel:    http://127.0.0.1:%d/admin/\n", port)
	fmt.Printf("  Stored at:      %s (mode 0600)\n", adminTokenFilePath)
	fmt.Println()
	fmt.Printf("  Read %s locally and paste it into the admin panel.\n", adminTokenFilePath)
	fmt.Println("  Never provide that token to an MCP client or model.")
	fmt.Println("════════════════════════════════════════════════════════════════════")
	fmt.Println()
}

func newAdminSessionManager(token string) *adminSessionManager {
	return &adminSessionManager{
		sessions: make(map[string]time.Time),
		token:    token,
	}
}

func (m *adminSessionManager) loginHandler(allowedHosts map[string]struct{}) http.HandlerFunc {
	type loginRequest struct {
		Token string `json:"token"`
	}
	type loginResponse struct {
		OK           bool   `json:"ok"`
		SessionToken string `json:"session_token"`
		ExpiresAt    int64  `json:"expires_at_unix"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		setAdminSecurityHeaders(w)
		if !hostAllowed(r.Host, allowedHosts) {
			http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasSameOrigin(r) {
			http.Error(w, "Forbidden: origin not allowed", http.StatusForbidden)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var req loginRequest
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if err := ensureJSONBodyConsumed(decoder); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if !constantTimeTokenEqual(strings.TrimSpace(req.Token), m.token) {
			http.Error(w, "Invalid admin token", http.StatusUnauthorized)
			return
		}

		sessionToken, err := randomHexToken()
		if err != nil {
			http.Error(w, "Could not create session", http.StatusInternalServerError)
			return
		}
		expires := time.Now().Add(adminSessionTTL)
		m.mu.Lock()
		m.sessions[sessionToken] = expires
		m.pruneExpiredLocked(time.Now())
		m.mu.Unlock()

		writeJSON(w, http.StatusOK, loginResponse{
			OK:           true,
			SessionToken: sessionToken,
			ExpiresAt:    expires.Unix(),
		})
	}
}

func (m *adminSessionManager) logoutHandler(allowedHosts map[string]struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setAdminSecurityHeaders(w)
		if !hostAllowed(r.Host, allowedHosts) {
			http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasSameOrigin(r) {
			http.Error(w, "Forbidden: origin not allowed", http.StatusForbidden)
			return
		}
		sessionToken := adminSessionToken(r)
		if !m.validSession(sessionToken, time.Now()) {
			writeAdminUnauthorized(w)
			return
		}
		m.mu.Lock()
		delete(m.sessions, sessionToken)
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (m *adminSessionManager) withSession(
	allowedHosts map[string]struct{},
	h http.HandlerFunc,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setAdminSecurityHeaders(w)
		if !hostAllowed(r.Host, allowedHosts) {
			http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
			return
		}
		if isMutatingMethod(r.Method) && !hasSameOrigin(r) {
			http.Error(w, "Forbidden: origin not allowed", http.StatusForbidden)
			return
		}
		if !m.validSession(adminSessionToken(r), time.Now()) {
			writeAdminUnauthorized(w)
			return
		}
		h(w, r)
	}
}

func adminSessionToken(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(adminSessionHeader))
}

func writeAdminUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `AdminSession realm="whatsapp-mcp-admin"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

func (m *adminSessionManager) validSession(token string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	expires, ok := m.sessions[token]
	if !ok || !expires.After(now) {
		delete(m.sessions, token)
		return false
	}
	m.pruneExpiredLocked(now)
	return true
}

func (m *adminSessionManager) pruneExpiredLocked(now time.Time) {
	for token, expires := range m.sessions {
		if !expires.After(now) {
			delete(m.sessions, token)
		}
	}
}

func constantTimeTokenEqual(got, expected string) bool {
	if len(got) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func hasSameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" && strings.EqualFold(parsed.Host, r.Host)
}

func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func setAdminSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
