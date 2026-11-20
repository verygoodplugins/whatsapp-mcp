package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.mau.fi/whatsmeow"
)

func TestProfilePictureStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("iq: %w", whatsmeow.ErrProfilePictureNotSet), http.StatusNotFound},
		{fmt.Errorf("iq: %w", whatsmeow.ErrProfilePictureUnauthorized), http.StatusForbidden},
		{errors.New("timeout"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := profilePictureStatus(tc.err); got != tc.want {
			t.Errorf("profilePictureStatus(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// A nil client is enough: every case returns at or before the connection
// check, and Client.IsConnected is nil-safe (returns false).
func TestProfilePictureEndpointValidation(t *testing.T) {
	identity := func(h http.HandlerFunc) http.HandlerFunc { return h }
	mux := http.NewServeMux()
	registerProfilePictureEndpoint(mux, identity, nil)

	cases := []struct {
		name, method, target string
		wantStatus           int
	}{
		{"wrong method", http.MethodPost, "/api/profile-picture?jid=1234567890@s.whatsapp.net", http.StatusMethodNotAllowed},
		{"missing jid", http.MethodGet, "/api/profile-picture", http.StatusBadRequest},
		{"bare number", http.MethodGet, "/api/profile-picture?jid=1234567890", http.StatusBadRequest},
		{"not connected", http.MethodGet, "/api/profile-picture?jid=1234567890@s.whatsapp.net", http.StatusServiceUnavailable},
		{"group not connected", http.MethodGet, "/api/profile-picture?jid=120363000000000000@g.us", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}
