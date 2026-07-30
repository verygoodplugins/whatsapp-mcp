package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testMCPBridgeToken = "mcp-bridge-token-for-handler-tests-123"

func newMCPAccessHandlerTest(t *testing.T) (*http.ServeMux, *MessageStore, string) {
	t.Helper()
	store := newMCPAccessTestStore(t)
	chatJID := "15551234567@s.whatsapp.net"
	seedMCPAccessChat(t, store, chatJID, "Alice", nil)
	handler := newRESTMux(newTestClient(&mockLIDStore{}), store, 8080, testMCPBridgeToken, nil)
	return handler, store, chatJID
}

func authenticatedMCPRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testMCPBridgeToken)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestMCPWriteHandlersDenyUnconfiguredChat(t *testing.T) {
	handler, _, chatJID := newMCPAccessHandlerTest(t)
	cases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "send",
			path: "/api/send",
			body: `{"recipient":"` + chatJID + `","message":"hello"}`,
		},
		{
			name: "reaction",
			path: "/api/react",
			body: `{"recipient":"` + chatJID + `","message_id":"message-1","emoji":"👍"}`,
		},
		{
			name: "typing",
			path: "/api/typing",
			body: `{"recipient":"` + chatJID + `","is_typing":true}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, authenticatedMCPRequest(http.MethodPost, tc.path, tc.body))
			if resp.Code != http.StatusForbidden {
				t.Fatalf("%s status=%d body=%s, want 403", tc.path, resp.Code, resp.Body.String())
			}
			if !strings.Contains(resp.Body.String(), "admin panel") {
				t.Fatalf("%s denial should explain how to grant access: %s", tc.path, resp.Body.String())
			}
			if strings.Contains(resp.Body.String(), chatJID) {
				t.Fatalf("%s denial leaked the canonical chat JID: %s", tc.path, resp.Body.String())
			}
		})
	}
}

func TestMCPDownloadRequiresReadableMessage(t *testing.T) {
	handler, store, chatJID := newMCPAccessHandlerTest(t)
	messageTime := time.Unix(1_780_000_000, 0).UTC()
	seedMCPAccessMessage(t, store, "message-1", chatJID, "private media", messageTime)

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, authenticatedMCPRequest(
		http.MethodPost,
		"/api/download",
		`{"message_id":"message-1","chat_jid":"`+chatJID+`"}`,
	))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("download status=%d body=%s, want 403", resp.Code, resp.Body.String())
	}

	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{ChatJID: chatJID, ReadHistory: true}},
		messageTime.Add(time.Minute),
	); err != nil {
		t.Fatalf("grant history access: %v", err)
	}
	allowedResp := httptest.NewRecorder()
	handler.ServeHTTP(allowedResp, authenticatedMCPRequest(
		http.MethodPost,
		"/api/download",
		`{"message_id":"message-1","chat_jid":"`+chatJID+`"}`,
	))
	if allowedResp.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"readable download should pass ACL and reach connection check, status=%d body=%s",
			allowedResp.Code,
			allowedResp.Body.String(),
		)
	}
}

func TestMCPWriteHandlerAllowsConfiguredChatPastACL(t *testing.T) {
	handler, store, chatJID := newMCPAccessHandlerTest(t)
	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{ChatJID: chatJID, CanSend: true}},
		time.Now(),
	); err != nil {
		t.Fatalf("grant send access: %v", err)
	}

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, authenticatedMCPRequest(
		http.MethodPost,
		"/api/send",
		`{"recipient":"`+chatJID+`","message":"hello"}`,
	))
	if resp.Code == http.StatusForbidden {
		t.Fatalf("configured chat was blocked by ACL: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "Not connected to WhatsApp") {
		t.Fatalf("configured send did not reach WhatsApp connection check: %s", resp.Body.String())
	}
}

func TestMCPStartNewConversationAllowsOnlyFirstDirectSend(t *testing.T) {
	handler, store, knownBlockedJID := newMCPAccessHandlerTest(t)
	if err := store.UpdateMCPAccessSettings(true, 1, time.Now()); err != nil {
		t.Fatalf("enable starting new conversations: %v", err)
	}
	newDirectJID := "15559876543@s.whatsapp.net"

	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, authenticatedMCPRequest(
		http.MethodPost,
		"/api/send",
		`{"recipient":"`+newDirectJID+`","message":"hello"}`,
	))
	if allowed.Code == http.StatusForbidden {
		t.Fatalf("new direct conversation was blocked: %s", allowed.Body.String())
	}
	if !strings.Contains(allowed.Body.String(), "Not connected to WhatsApp") {
		t.Fatalf("new direct send did not reach WhatsApp connection check: %s", allowed.Body.String())
	}

	deniedCases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "known blocked chat",
			path: "/api/send",
			body: `{"recipient":"` + knownBlockedJID + `","message":"hello"}`,
		},
		{
			name: "unknown group",
			path: "/api/send",
			body: `{"recipient":"120363099999999999@g.us","message":"hello"}`,
		},
		{
			name: "quoted reply",
			path: "/api/send",
			body: `{"recipient":"` + newDirectJID + `","message":"reply","quoted_message_id":"unknown"}`,
		},
		{
			name: "reaction",
			path: "/api/react",
			body: `{"recipient":"` + newDirectJID + `","message_id":"unknown","emoji":"👍"}`,
		},
		{
			name: "typing",
			path: "/api/typing",
			body: `{"recipient":"` + newDirectJID + `","is_typing":true}`,
		},
	}
	for _, tc := range deniedCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, authenticatedMCPRequest(http.MethodPost, tc.path, tc.body))
			if resp.Code != http.StatusForbidden {
				t.Fatalf("%s status=%d body=%s, want 403", tc.path, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestMCPStartNewConversationRejectsMalformedPhoneNumber(t *testing.T) {
	handler, store, _ := newMCPAccessHandlerTest(t)
	if err := store.UpdateMCPAccessSettings(true, 1, time.Now()); err != nil {
		t.Fatalf("enable starting new conversations: %v", err)
	}

	for _, recipient := range []string{
		"short",
		"123456@s.whatsapp.net",
		"1234567890123456@s.whatsapp.net",
		"55119999abc@s.whatsapp.net",
	} {
		t.Run(recipient, func(t *testing.T) {
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, authenticatedMCPRequest(
				http.MethodPost,
				"/api/send",
				`{"recipient":"`+recipient+`","message":"hello"}`,
			))
			if resp.Code != http.StatusForbidden {
				t.Fatalf("malformed recipient status=%d body=%s, want 403", resp.Code, resp.Body.String())
			}
		})
	}
}
