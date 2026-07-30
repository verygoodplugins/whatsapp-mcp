package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testAdminToken  = "admin-token-that-is-long-and-separate-123"
	testBridgeToken = "bridge-token-that-the-mcp-receives-456"
	testAdminOrigin = "http://127.0.0.1:8080"
)

func newAdminUITestMux(t *testing.T) (*http.ServeMux, *MessageStore) {
	t.Helper()
	store := newMCPAccessTestStore(t)
	mux := http.NewServeMux()
	registerAdminUI(mux, nil, store, 8080, testAdminToken)
	return mux, store
}

func loginAdminForTest(t *testing.T, handler http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		testAdminOrigin+"/admin/login",
		strings.NewReader(`{"token":"`+testAdminToken+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testAdminOrigin)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("admin login status=%d body=%s", resp.Code, resp.Body.String())
	}
	if cookie := resp.Header().Get("Set-Cookie"); cookie != "" {
		t.Fatalf("admin login must not set a bearer cookie, got %q", cookie)
	}
	var payload struct {
		SessionToken string `json:"session_token"`
		ExpiresAt    int64  `json:"expires_at_unix"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode admin login response: %v", err)
	}
	if payload.SessionToken == "" || payload.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("admin login returned invalid session metadata: %#v", payload)
	}
	return payload.SessionToken
}

func addAdminSession(req *http.Request, sessionToken string) {
	req.Header.Set(adminSessionHeader, sessionToken)
}

func TestAdminUIStaticPageHasStrictSecurityHeaders(t *testing.T) {
	mux, _ := newAdminUITestMux(t)
	req := httptest.NewRequest(http.MethodGet, testAdminOrigin+"/admin/", nil)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("admin page status=%d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "Acesso do modelo") {
		t.Fatalf("admin page missing expected content: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "Iniciar novas conversas individuais") {
		t.Fatalf("admin page missing new-conversation setting: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `class="language-select"`) ||
		!strings.Contains(resp.Body.String(), `type="module"`) {
		t.Fatalf("admin page missing modular language controls: %s", resp.Body.String())
	}
	if got := resp.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("admin page CSP=%q, want frame protection", got)
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("admin page Cache-Control=%q, want no-store", got)
	}
}

func TestAdminUITranslationCatalogIsServedFromEmbeddedAssets(t *testing.T) {
	mux, _ := newAdminUITestMux(t)
	req := httptest.NewRequest(http.MethodGet, testAdminOrigin+"/admin/i18n/en.json", nil)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("English catalog status=%d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("English catalog content type=%q, want JSON", got)
	}
	if !strings.Contains(resp.Body.String(), `"hero.title": "Model access"`) {
		t.Fatalf("English catalog missing expected translation: %s", resp.Body.String())
	}
}

func TestAdminLoginRejectsBridgeTokenAndCrossOrigin(t *testing.T) {
	mux, _ := newAdminUITestMux(t)

	t.Run("bridge token", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPost,
			testAdminOrigin+"/admin/login",
			strings.NewReader(`{"token":"`+testBridgeToken+`"}`),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", testAdminOrigin)
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("bridge token login status=%d, want 401", resp.Code)
		}
	})

	t.Run("cross origin", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPost,
			testAdminOrigin+"/admin/login",
			strings.NewReader(`{"token":"`+testAdminToken+`"}`),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://attacker.example")
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("cross-origin login status=%d, want 403", resp.Code)
		}
	})
}

func TestAdminLoginRejectsTrailingJSON(t *testing.T) {
	mux, _ := newAdminUITestMux(t)
	req := httptest.NewRequest(
		http.MethodPost,
		testAdminOrigin+"/admin/login",
		strings.NewReader(`{"token":"`+testAdminToken+`"}{"token":"ignored"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testAdminOrigin)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON login status=%d, want 400", resp.Code)
	}
}

func TestAdminChatsRequireSessionAndNeverIncludeMessageContent(t *testing.T) {
	mux, store := newAdminUITestMux(t)
	chatJID := "15551234567@s.whatsapp.net"
	messageTime := time.Unix(1_770_000_000, 0).UTC()
	seedMCPAccessChat(t, store, chatJID, "Alice", &messageTime)
	seedMCPAccessMessage(t, store, "secret", chatJID, "DO-NOT-EXPOSE", messageTime)

	unauthenticated := httptest.NewRequest(http.MethodGet, testAdminOrigin+"/api/admin/chats", nil)
	unauthenticatedResp := httptest.NewRecorder()
	mux.ServeHTTP(unauthenticatedResp, unauthenticated)
	if unauthenticatedResp.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated chats status=%d, want 401", unauthenticatedResp.Code)
	}

	sessionToken := loginAdminForTest(t, mux)
	req := httptest.NewRequest(http.MethodGet, testAdminOrigin+"/api/admin/chats", nil)
	addAdminSession(req, sessionToken)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("authenticated chats status=%d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "DO-NOT-EXPOSE") {
		t.Fatalf("admin chats leaked message content: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"name":"Alice"`) {
		t.Fatalf("admin chats missing expected metadata: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"allow_start_new_conversations":false`) {
		t.Fatalf("admin chats missing default global settings: %s", resp.Body.String())
	}
}

func TestAdminSettingsUpdateRequiresConfirmationRevision(t *testing.T) {
	mux, store := newAdminUITestMux(t)
	sessionToken := loginAdminForTest(t, mux)

	crossOrigin := httptest.NewRequest(
		http.MethodPut,
		testAdminOrigin+"/api/admin/settings",
		strings.NewReader(`{"allow_start_new_conversations":true,"expected_revision":1}`),
	)
	addAdminSession(crossOrigin, sessionToken)
	crossOrigin.Header.Set("Content-Type", "application/json")
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOriginResp := httptest.NewRecorder()
	mux.ServeHTTP(crossOriginResp, crossOrigin)
	if crossOriginResp.Code != http.StatusForbidden {
		t.Fatalf("cross-origin settings status=%d, want 403", crossOriginResp.Code)
	}

	req := httptest.NewRequest(
		http.MethodPut,
		testAdminOrigin+"/api/admin/settings",
		strings.NewReader(`{"allow_start_new_conversations":true,"expected_revision":1}`),
	)
	addAdminSession(req, sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testAdminOrigin)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("settings update status=%d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"allow_start_new_conversations":true`) ||
		!strings.Contains(resp.Body.String(), `"revision":2`) {
		t.Fatalf("settings update returned unexpected body: %s", resp.Body.String())
	}
	settings, err := store.GetMCPAccessSettings()
	if err != nil {
		t.Fatalf("load settings after admin update: %v", err)
	}
	if !settings.AllowStartNewConversations {
		t.Fatal("admin settings update was not persisted")
	}

	stale := httptest.NewRequest(
		http.MethodPut,
		testAdminOrigin+"/api/admin/settings",
		strings.NewReader(`{"allow_start_new_conversations":false,"expected_revision":1}`),
	)
	addAdminSession(stale, sessionToken)
	stale.Header.Set("Content-Type", "application/json")
	stale.Header.Set("Origin", testAdminOrigin)
	staleResp := httptest.NewRecorder()
	mux.ServeHTTP(staleResp, stale)
	if staleResp.Code != http.StatusConflict {
		t.Fatalf("stale settings status=%d body=%s, want 409", staleResp.Code, staleResp.Body.String())
	}
}

func TestAdminPermissionUpdateUsesSessionOriginAndServerTime(t *testing.T) {
	mux, store := newAdminUITestMux(t)
	chatJID := "15551234567@s.whatsapp.net"
	seedMCPAccessChat(t, store, chatJID, "Alice", nil)
	sessionToken := loginAdminForTest(t, mux)

	body, err := json.Marshal(map[string]interface{}{
		"updates": []map[string]interface{}{{
			"chat_jid":     chatJID,
			"read_new":     true,
			"read_history": true,
			"can_send":     true,
		}},
	})
	if err != nil {
		t.Fatalf("marshal permission update: %v", err)
	}

	crossOrigin := httptest.NewRequest(
		http.MethodPut,
		testAdminOrigin+"/api/admin/permissions",
		strings.NewReader(string(body)),
	)
	addAdminSession(crossOrigin, sessionToken)
	crossOrigin.Header.Set("Content-Type", "application/json")
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOriginResp := httptest.NewRecorder()
	mux.ServeHTTP(crossOriginResp, crossOrigin)
	if crossOriginResp.Code != http.StatusForbidden {
		t.Fatalf("cross-origin update status=%d, want 403", crossOriginResp.Code)
	}

	before := time.Now().Add(-time.Second).Unix()
	req := httptest.NewRequest(
		http.MethodPut,
		testAdminOrigin+"/api/admin/permissions",
		strings.NewReader(string(body)),
	)
	addAdminSession(req, sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testAdminOrigin)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	after := time.Now().Add(time.Second).Unix()
	if resp.Code != http.StatusOK {
		t.Fatalf("permission update status=%d body=%s", resp.Code, resp.Body.String())
	}

	chats, err := store.ListMCPAccessChats()
	if err != nil {
		t.Fatalf("list updated permissions: %v", err)
	}
	chat := findMCPAdminChat(t, chats, chatJID)
	if !chat.ReadNew || !chat.ReadHistory || !chat.CanSend {
		t.Fatalf("permission update not applied: %#v", chat)
	}
	if chat.ReadNewSinceUnix == nil || chat.ReadHistoryThroughUnix == nil {
		t.Fatalf("server did not assign read endpoints: %#v", chat)
	}
	if *chat.ReadNewSinceUnix < before || *chat.ReadNewSinceUnix > after ||
		*chat.ReadHistoryThroughUnix < before || *chat.ReadHistoryThroughUnix > after {
		t.Fatalf("read endpoints were not server timestamps: %#v", chat)
	}
}

func TestAdminPermissionUpdateRejectsUnknownJSONFields(t *testing.T) {
	mux, store := newAdminUITestMux(t)
	chatJID := "15551234567@s.whatsapp.net"
	seedMCPAccessChat(t, store, chatJID, "Alice", nil)
	sessionToken := loginAdminForTest(t, mux)

	req := httptest.NewRequest(
		http.MethodPut,
		testAdminOrigin+"/api/admin/permissions",
		strings.NewReader(`{"updates":[{"chat_jid":"`+chatJID+`","can_send":true,"timestamp":9999999999}]}`),
	)
	addAdminSession(req, sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testAdminOrigin)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field update status=%d, want 400", resp.Code)
	}
}

func TestAdminPermissionUpdateReturnsConflictForStaleRevision(t *testing.T) {
	mux, store := newAdminUITestMux(t)
	chatJID := "15551234567@s.whatsapp.net"
	seedMCPAccessChat(t, store, chatJID, "Alice", nil)
	sessionToken := loginAdminForTest(t, mux)

	update := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(
			http.MethodPut,
			testAdminOrigin+"/api/admin/permissions",
			strings.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", testAdminOrigin)
		addAdminSession(req, sessionToken)
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		return resp
	}

	first := update(
		`{"updates":[{"chat_jid":"` + chatJID + `","expected_revision":0,"read_new":true}]}`,
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first permission update status=%d body=%s", first.Code, first.Body.String())
	}

	stale := update(
		`{"updates":[{"chat_jid":"` + chatJID + `","expected_revision":0,"can_send":true}]}`,
	)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale permission update status=%d body=%s, want 409", stale.Code, stale.Body.String())
	}
	if !strings.Contains(stale.Body.String(), "refresh") {
		t.Fatalf("stale permission update should tell the operator to refresh: %s", stale.Body.String())
	}
}

func TestAdminSessionHeaderCannotBeReplacedByCookieAndLogoutRevokesIt(t *testing.T) {
	mux, _ := newAdminUITestMux(t)
	sessionToken := loginAdminForTest(t, mux)

	cookieOnly := httptest.NewRequest(http.MethodGet, testAdminOrigin+"/api/admin/chats", nil)
	cookieOnly.AddCookie(&http.Cookie{Name: "whatsapp_mcp_admin", Value: sessionToken})
	cookieOnlyResp := httptest.NewRecorder()
	mux.ServeHTTP(cookieOnlyResp, cookieOnly)
	if cookieOnlyResp.Code != http.StatusUnauthorized {
		t.Fatalf("cookie-only request status=%d, want 401", cookieOnlyResp.Code)
	}

	logout := httptest.NewRequest(http.MethodPost, testAdminOrigin+"/admin/logout", nil)
	logout.Header.Set("Origin", testAdminOrigin)
	addAdminSession(logout, sessionToken)
	logoutResp := httptest.NewRecorder()
	mux.ServeHTTP(logoutResp, logout)
	if logoutResp.Code != http.StatusOK {
		t.Fatalf("admin logout status=%d body=%s", logoutResp.Code, logoutResp.Body.String())
	}

	afterLogout := httptest.NewRequest(http.MethodGet, testAdminOrigin+"/api/admin/chats", nil)
	addAdminSession(afterLogout, sessionToken)
	afterLogoutResp := httptest.NewRecorder()
	mux.ServeHTTP(afterLogoutResp, afterLogout)
	if afterLogoutResp.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d, want 401", afterLogoutResp.Code)
	}
}
