package main

// Group management endpoints (local addition, not upstream).
//
//   POST /api/create-group  {"name": "...", "participants": ["5527...", ...], "topic": "..."}
//   → {"ok": true, "jid": "1203...@g.us"}
//
// Participants are phone numbers with country code or full JIDs. The bridge's
// own number is added by WhatsApp implicitly. Group names are limited to 25
// characters by WhatsApp.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type createGroupRequest struct {
	Name         string   `json:"name"`
	Participants []string `json:"participants"`
	Topic        string   `json:"topic"`
}

func registerGroupEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, allowedMediaRoots []string) {
	registerGroupAdminEndpoints(mux, auth, client)
	registerGroupInfoEndpoint(mux, auth, client)
	registerGroupPhotoEndpoint(mux, auth, client, allowedMediaRoots)
	mux.HandleFunc("/api/create-group", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req createGroupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || len(req.Participants) == 0 {
			http.Error(w, "name and participants are required", http.StatusBadRequest)
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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		info, err := client.CreateGroup(ctx, whatsmeow.ReqCreateGroup{Name: req.Name, Participants: jids})
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		resp := map[string]interface{}{"ok": true, "jid": info.JID.String()}
		if req.Topic != "" {
			if err := client.SetGroupTopic(ctx, info.JID, "", "", req.Topic); err != nil {
				resp["topic_error"] = err.Error()
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

type revokeRequest struct {
	Chat      string `json:"chat"`
	MessageID string `json:"message_id"`
}

type participantsRequest struct {
	Group        string   `json:"group"`
	Action       string   `json:"action"` // add | remove
	Participants []string `json:"participants"`
}

func registerGroupAdminEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client) {
	// POST /api/revoke {"chat": "<jid>", "message_id": "<id>"} — deletes an own message for everyone.
	mux.HandleFunc("/api/revoke", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req revokeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Chat == "" || req.MessageID == "" {
			http.Error(w, "chat and message_id are required", http.StatusBadRequest)
			return
		}
		chat, err := types.ParseJID(req.Chat)
		if err != nil {
			http.Error(w, "invalid chat JID: "+err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if _, err := client.RevokeMessage(ctx, chat, types.MessageID(req.MessageID)); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))

	// POST /api/group-participants {"group": "<jid>", "action": "add|remove", "participants": [...]}
	mux.HandleFunc("/api/group-participants", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req participantsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Group == "" || len(req.Participants) == 0 || (req.Action != "add" && req.Action != "remove") {
			http.Error(w, "group, action (add|remove) and participants are required", http.StatusBadRequest)
			return
		}
		group, err := types.ParseJID(req.Group)
		if err != nil {
			http.Error(w, "invalid group JID: "+err.Error(), http.StatusBadRequest)
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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := client.UpdateGroupParticipants(ctx, group, jids, whatsmeow.ParticipantChange(req.Action))
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "result": res})
	}))
}

func registerGroupInfoEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client) {
	// GET /api/group-info?jid=<group jid>
	mux.HandleFunc("/api/group-info", auth(func(w http.ResponseWriter, r *http.Request) {
		group, err := types.ParseJID(r.URL.Query().Get("jid"))
		if err != nil {
			http.Error(w, "invalid jid: "+err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		info, err := client.GetGroupInfo(ctx, group)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(info)
	}))
}

type groupPhotoRequest struct {
	Group string `json:"group"`
	Path  string `json:"path"` // JPEG file inside an allowed media root
}

func registerGroupPhotoEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, allowedMediaRoots []string) {
	// POST /api/group-photo {"group": "<jid>", "path": "<jpeg under allowed media root>"}
	mux.HandleFunc("/api/group-photo", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req groupPhotoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Group == "" || req.Path == "" {
			http.Error(w, "group and path are required", http.StatusBadRequest)
			return
		}
		group, err := types.ParseJID(req.Group)
		if err != nil {
			http.Error(w, "invalid group JID: "+err.Error(), http.StatusBadRequest)
			return
		}
		clean, err := validateMediaPath(req.Path, allowedMediaRoots)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		pictureID, err := client.SetGroupPhoto(ctx, group, data)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "true", "picture_id": pictureID})
	}))
}
