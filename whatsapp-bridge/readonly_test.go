package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The bridge is read-only: every endpoint that could change something on
// WhatsApp was removed, so nothing the MCP server can reach sends a message,
// a reaction, a read receipt or a typing indicator. A route coming back is a
// regression, not a feature — these paths must stay unrouted.
func TestWriteEndpointsAreNotRouted(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	handler := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, token)

	for _, route := range []string{"/api/send", "/api/react", "/api/mark-read", "/api/typing"} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+route, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)

		if resp.Code != http.StatusNotFound {
			t.Errorf("%s is routed (status %d); write endpoints must not exist", route, resp.Code)
		}
	}
}

// Reading has to keep working, or read-only would just mean broken.
func TestReadEndpointsStayRouted(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	handler := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, token)

	for _, route := range []string{"/api/health", "/api/download"} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+route, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)

		if resp.Code == http.StatusNotFound {
			t.Errorf("%s is not routed; read endpoints must stay available", route)
		}
	}
}
