package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// profilePictureStatus maps whatsmeow's profile picture errors to HTTP
// statuses: no picture is 404, a picture hidden by privacy settings is 403.
func profilePictureStatus(err error) int {
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet):
		return http.StatusNotFound
	case errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// registerProfilePictureEndpoint wires GET /api/profile-picture?jid=<user or
// group JID>, which returns the current profile picture URL and ID.
func registerProfilePictureEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client) {
	mux.HandleFunc("/api/profile-picture", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw := r.URL.Query().Get("jid")
		jid, err := types.ParseJID(raw)
		if raw == "" || err != nil || jid.User == "" || jid.Server == "" {
			http.Error(w, "jid must be a user or group JID (for example 1234567890@s.whatsapp.net)", http.StatusBadRequest)
			return
		}
		if !client.IsConnected() {
			http.Error(w, "Not connected to WhatsApp", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		info, err := client.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{})
		if err == nil && info == nil {
			err = whatsmeow.ErrProfilePictureNotSet
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(profilePictureStatus(err))
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": info.URL, "id": info.ID})
	}))
}
