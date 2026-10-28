package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// GroupParticipantsRequest is the body of POST /api/group-participants.
type GroupParticipantsRequest struct {
	Group        string   `json:"group"`
	Action       string   `json:"action"` // add | remove
	Participants []string `json:"participants"`
}

// GroupSettingsRequest is the body of POST /api/group-settings. Empty fields
// are left unchanged.
type GroupSettingsRequest struct {
	Group string `json:"group"`
	Name  string `json:"name"`
	Topic string `json:"topic"`
}

// GroupPhotoRequest is the body of POST /api/group-photo.
type GroupPhotoRequest struct {
	Group string `json:"group"`
	Path  string `json:"path"` // JPEG inside one of the allowed media roots
}

func writeGroupError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// registerGroupAdminEndpoints wires the endpoints that change an existing
// group: participants, name and description, and photo.
func registerGroupAdminEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, allowedMediaRoots []string) {
	mux.HandleFunc("/api/group-participants", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req GroupParticipantsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		if len(req.Participants) == 0 || (req.Action != "add" && req.Action != "remove") {
			http.Error(w, "action (add or remove) and participants are required", http.StatusBadRequest)
			return
		}
		group, err := parseGroupJID(req.Group)
		if err != nil {
			http.Error(w, "invalid group: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !client.IsConnected() {
			http.Error(w, "Not connected to WhatsApp", http.StatusServiceUnavailable)
			return
		}
		jids := make([]types.JID, 0, len(req.Participants))
		for _, p := range req.Participants {
			jid, err := resolveRecipientJID(client, p)
			if err != nil {
				http.Error(w, "invalid participant "+p+": "+err.Error(), http.StatusBadRequest)
				return
			}
			jids = append(jids, jid)
		}
		ctx, cancel := context.WithTimeout(r.Context(), groupRequestTimeout)
		defer cancel()
		result, err := client.UpdateGroupParticipants(ctx, group, jids, whatsmeow.ParticipantChange(req.Action))
		if err != nil {
			writeGroupError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "result": result})
	}))

	mux.HandleFunc("/api/group-settings", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req GroupSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		if req.Name == "" && req.Topic == "" {
			http.Error(w, "name or topic is required", http.StatusBadRequest)
			return
		}
		group, err := parseGroupJID(req.Group)
		if err != nil {
			http.Error(w, "invalid group: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !client.IsConnected() {
			http.Error(w, "Not connected to WhatsApp", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), groupRequestTimeout)
		defer cancel()
		resp := map[string]interface{}{"ok": true}
		if req.Name != "" {
			if err := client.SetGroupName(ctx, group, req.Name); err != nil {
				resp["ok"] = false
				resp["name_error"] = err.Error()
			}
		}
		if req.Topic != "" {
			if err := client.SetGroupTopic(ctx, group, "", "", req.Topic); err != nil {
				resp["ok"] = false
				resp["topic_error"] = err.Error()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if resp["ok"] == false {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))

	mux.HandleFunc("/api/group-photo", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req GroupPhotoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		if req.Path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		group, err := parseGroupJID(req.Group)
		if err != nil {
			http.Error(w, "invalid group: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Same allow-list as outbound media in /api/send (see media_path.go).
		clean, err := validateMediaPath(req.Path, allowedMediaRoots)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			http.Error(w, "cannot read path: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !client.IsConnected() {
			http.Error(w, "Not connected to WhatsApp", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), groupRequestTimeout)
		defer cancel()
		pictureID, err := client.SetGroupPhoto(ctx, group, data)
		if err != nil {
			writeGroupError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "picture_id": pictureID})
	}))
}
