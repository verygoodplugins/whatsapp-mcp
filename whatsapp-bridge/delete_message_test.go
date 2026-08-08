package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const deleteTestToken = "supersecrettoken1234567890abcdef"

func newDeleteTestMux(t *testing.T) http.Handler {
	t.Helper()
	return newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, deleteTestToken, nil)
}

func newDeleteRequest(body string, withAuth bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/delete", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:54321"
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+deleteTestToken)
	}
	return req
}

// The delete endpoint revokes messages on the recipient's device, so it must sit
// behind the same bearer-token gate as every other /api/* route rather than on
// the default mux.
func TestDeleteHandlerRequiresAuth(t *testing.T) {
	handler := newDeleteTestMux(t)
	body := `{"chat_jid":"12025551234@s.whatsapp.net","message_id":"3AABCDEF01234567","for_everyone":true}`

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, newDeleteRequest(body, false))

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated delete to return 401, got %d", resp.Code)
	}
}

func TestDeleteHandlerRejectsWrongToken(t *testing.T) {
	handler := newDeleteTestMux(t)
	req := newDeleteRequest(`{"chat_jid":"a@s.whatsapp.net","message_id":"3AABCDEF01234567"}`, false)
	req.Header.Set("Authorization", "Bearer wrongtokenwrongtokenwrongtoken00")

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected wrong-token delete to return 401, got %d", resp.Code)
	}
}

func TestDeleteHandlerRejectsNonPost(t *testing.T) {
	handler := newDeleteTestMux(t)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/delete", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer "+deleteTestToken)

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected GET to return 405, got %d", resp.Code)
	}
}

func TestDeleteHandlerRejectsMalformedBody(t *testing.T) {
	handler := newDeleteTestMux(t)

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, newDeleteRequest("{", true))

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected malformed body to return 400, got %d", resp.Code)
	}
}

// chat_jid and message_id identify the message to revoke; neither has a sensible
// default, so an incomplete request must fail before any send is attempted.
func TestDeleteHandlerRequiresChatJIDAndMessageID(t *testing.T) {
	handler := newDeleteTestMux(t)
	cases := []struct {
		name string
		body string
	}{
		{"missing message_id", `{"chat_jid":"12025551234@s.whatsapp.net"}`},
		{"missing chat_jid", `{"message_id":"3AABCDEF01234567"}`},
		{"both empty", `{"chat_jid":"","message_id":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, newDeleteRequest(tc.body, true))

			if resp.Code != http.StatusBadRequest {
				t.Fatalf("expected %s to return 400, got %d", tc.name, resp.Code)
			}
			if !strings.Contains(resp.Body.String(), "required") {
				t.Fatalf("expected error naming the required fields, got %q", resp.Body.String())
			}
		})
	}
}
