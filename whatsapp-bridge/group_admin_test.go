package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestGroupAdminEndpointsValidation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	photo := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(photo, []byte("jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	if err := os.WriteFile(outside, []byte("jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}

	identity := func(h http.HandlerFunc) http.HandlerFunc { return h }
	register := func(mux *http.ServeMux) { registerGroupAdminEndpoints(mux, identity, nil, []string{root}) }
	const group = "120363000000000000@g.us"

	cases := []struct {
		name, method, target, body string
		wantStatus                 int
	}{
		{"participants: wrong method", http.MethodGet, "/api/group-participants", "", http.StatusMethodNotAllowed},
		{"participants: invalid json", http.MethodPost, "/api/group-participants", "{not json", http.StatusBadRequest},
		{"participants: unknown action", http.MethodPost, "/api/group-participants", `{"group":"` + group + `","action":"promote","participants":["15551234567"]}`, http.StatusBadRequest},
		{"participants: none given", http.MethodPost, "/api/group-participants", `{"group":"` + group + `","action":"add"}`, http.StatusBadRequest},
		{"participants: user jid as group", http.MethodPost, "/api/group-participants", `{"group":"15551234567@s.whatsapp.net","action":"add","participants":["15557654321"]}`, http.StatusBadRequest},
		{"participants: not connected", http.MethodPost, "/api/group-participants", `{"group":"` + group + `","action":"remove","participants":["15551234567"]}`, http.StatusServiceUnavailable},
		{"settings: wrong method", http.MethodGet, "/api/group-settings", "", http.StatusMethodNotAllowed},
		{"settings: nothing to change", http.MethodPost, "/api/group-settings", `{"group":"` + group + `"}`, http.StatusBadRequest},
		{"settings: missing group", http.MethodPost, "/api/group-settings", `{"name":"Team"}`, http.StatusBadRequest},
		{"settings: not connected", http.MethodPost, "/api/group-settings", `{"group":"` + group + `","topic":"Weekly sync"}`, http.StatusServiceUnavailable},
		{"photo: wrong method", http.MethodGet, "/api/group-photo", "", http.StatusMethodNotAllowed},
		{"photo: missing path", http.MethodPost, "/api/group-photo", `{"group":"` + group + `"}`, http.StatusBadRequest},
		{"photo: missing group", http.MethodPost, "/api/group-photo", `{"path":"` + photo + `"}`, http.StatusBadRequest},
		{"photo: outside media roots", http.MethodPost, "/api/group-photo", `{"group":"` + group + `","path":"` + outside + `"}`, http.StatusBadRequest},
		{"photo: not connected", http.MethodPost, "/api/group-photo", `{"group":"` + group + `","path":"` + photo + `"}`, http.StatusServiceUnavailable},
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
