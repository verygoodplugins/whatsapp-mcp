package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func restrictedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var err error
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	policy := ownerPolicy{Version: 1, AccessEnabled: true, AllowedGroups: []string{"123-456@g.us"}, Capabilities: map[string]bool{}}
	for _, key := range capabilityNames {
		policy.Capabilities[key] = key == "read_messages" || key == "cache_messages"
	}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WHATSAPP_POLICY_FILE", path)
	return path
}

func TestRestrictedPolicyAndRevocation(t *testing.T) {
	path := restrictedFixture(t)
	if !cacheAllowed("123-456@g.us") || cacheAllowed("999@g.us") || cacheAllowed("123@s.whatsapp.net") {
		t.Fatal("group scope mismatch")
	}
	policy, err := loadOwnerPolicy()
	if err != nil {
		t.Fatal(err)
	}
	policy.AccessEnabled = false
	data, _ := json.Marshal(policy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if cacheAllowed("123-456@g.us") {
		t.Fatal("revocation not reloaded")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOwnerPolicy(); err == nil {
		t.Fatal("permissive policy accepted")
	}
}

func TestRestrictedJSONValidation(t *testing.T) {
	path := restrictedFixture(t)
	data, _ := os.ReadFile(path)
	for _, bad := range []string{
		strings.Replace(string(data), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(data), `"media":false`, `"media":true`, 1),
		strings.Replace(string(data), `"access_enabled":true`, `"access_enabled":null`, 1),
		strings.Replace(string(data), `123-456@g.us`, `123@s.whatsapp.net`, 1),
		string(data) + `{}`,
	} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadOwnerPolicy(); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}

func TestRestrictedRoutesAndEgressDisabled(t *testing.T) {
	restrictedFixture(t)
	t.Setenv("WEBHOOK_ENABLED", "true")
	t.Setenv("WHATSAPP_AUTO_DOWNLOAD_MEDIA", "true")
	if webhooksEnabled() || autoDownloadMediaEnabled() {
		t.Fatal("implicit egress enabled")
	}
	mux := newRESTMux(nil, nil, 18080, "synthetic-test-token", nil)
	for _, route := range []string{"send", "mark-read", "react", "typing", "download", "history"} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18080/api/"+route, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer synthetic-test-token")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d", route, response.Code)
		}
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/api/health", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated health accepted")
	}
}

func TestRestrictedStorageAndEarlyLiveFilter(t *testing.T) {
	restrictedFixture(t)
	ms := newTestMessageStore(t)
	for _, jid := range []string{"123-456@g.us", "999@g.us", "123@s.whatsapp.net", "status@broadcast"} {
		if err := ms.StoreChat(jid, "fixture", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := ms.StoreMessage("duplicate", jid, "fixture", "text", time.Now(), false, "image", "private-filename", "private-url", []byte("key"), nil, nil, 1, ""); err != nil {
			t.Fatal(err)
		}
		want := 0
		if jid == "123-456@g.us" {
			want = 1
		}
		if got := queryMessageCount(ms, jid); got != want {
			t.Fatalf("%s count=%d", jid, got)
		}
	}
	var filename, url string
	var key []byte
	if err := ms.db.QueryRow("SELECT filename,url,media_key FROM messages").Scan(&filename, &url, &key); err != nil {
		t.Fatal(err)
	}
	if filename != "" || url != "" || len(key) != 0 {
		t.Fatal("media secrets cached")
	}
	// A nil client would panic if the forbidden message got past the entry gate.
	handleMessage(nil, ms, &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("999", "g.us")}}, Message: &waProto.Message{Conversation: proto.String("forbidden")}}, testLogger())
	if queryMessageCount(ms, "999@g.us") != 0 {
		t.Fatal("forbidden event cached")
	}
}
