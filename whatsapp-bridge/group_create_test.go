package main

import (
	"net/http"
	"testing"
)

func TestGroupCreateEndpointsValidation(t *testing.T) {
	identity := func(h http.HandlerFunc) http.HandlerFunc { return h }
	register := func(mux *http.ServeMux) { registerGroupCreateEndpoints(mux, identity, nil) }

	cases := []struct {
		name, method, target, body string
		wantStatus                 int
	}{
		{"create: wrong method", http.MethodGet, "/api/create-group", "", http.StatusMethodNotAllowed},
		{"create: invalid json", http.MethodPost, "/api/create-group", "{not json", http.StatusBadRequest},
		{"create: missing name", http.MethodPost, "/api/create-group", `{"participants":["15551234567"]}`, http.StatusBadRequest},
		{"create: plain group without participants", http.MethodPost, "/api/create-group", `{"name":"Team"}`, http.StatusBadRequest},
		{"create: community with parent", http.MethodPost, "/api/create-group", `{"name":"Team","community":true,"parent":"120363000000000001@g.us"}`, http.StatusBadRequest},
		{"create: parent is not a group", http.MethodPost, "/api/create-group", `{"name":"Team","parent":"15551234567@s.whatsapp.net"}`, http.StatusBadRequest},
		{"create: not connected", http.MethodPost, "/api/create-group", `{"name":"Team","participants":["15551234567"]}`, http.StatusServiceUnavailable},
		{"create: community not connected", http.MethodPost, "/api/create-group", `{"name":"Customers","community":true}`, http.StatusServiceUnavailable},
		{"create: subgroup not connected", http.MethodPost, "/api/create-group", `{"name":"News","parent":"120363000000000001@g.us"}`, http.StatusServiceUnavailable},
		{"subgroups: wrong method", http.MethodPost, "/api/subgroups?jid=120363000000000001@g.us", "", http.StatusMethodNotAllowed},
		{"subgroups: missing jid", http.MethodGet, "/api/subgroups", "", http.StatusBadRequest},
		{"subgroups: user jid", http.MethodGet, "/api/subgroups?jid=15551234567@s.whatsapp.net", "", http.StatusBadRequest},
		{"subgroups: not connected", http.MethodGet, "/api/subgroups?jid=120363000000000001@g.us", "", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveGroupRequest(t, register, tc.method, tc.target, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}
