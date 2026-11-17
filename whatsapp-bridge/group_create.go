package main

import (
	"context"
	"encoding/json"
	"net/http"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// CreateGroupRequest is the body of POST /api/create-group.
type CreateGroupRequest struct {
	Name         string   `json:"name"`
	Participants []string `json:"participants"`
	Topic        string   `json:"topic"`
	// Community creates a community instead of a plain group. Joining it
	// requires admin approval.
	Community bool `json:"community"`
	// Parent creates the group inside this community (a group JID).
	Parent string `json:"parent"`
	// Approval requires admin approval to join the group.
	Approval bool `json:"approval"`
}

// registerGroupCreateEndpoints wires POST /api/create-group and
// GET /api/subgroups onto an existing mux.
func registerGroupCreateEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client) {
	mux.HandleFunc("/api/create-group", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req CreateGroupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if len(req.Participants) == 0 && !req.Community && req.Parent == "" {
			http.Error(w, "participants are required unless community or parent is set", http.StatusBadRequest)
			return
		}
		if req.Community && req.Parent != "" {
			http.Error(w, "community and parent cannot be combined", http.StatusBadRequest)
			return
		}
		create := whatsmeow.ReqCreateGroup{Name: req.Name}
		if req.Parent != "" {
			parent, err := parseGroupJID(req.Parent)
			if err != nil {
				http.Error(w, "invalid parent: "+err.Error(), http.StatusBadRequest)
				return
			}
			create.GroupLinkedParent = types.GroupLinkedParent{LinkedParentJID: parent}
		}
		if req.Community {
			create.GroupParent = types.GroupParent{IsParent: true, DefaultMembershipApprovalMode: "request_required"}
		}
		if req.Community || req.Approval {
			create.GroupMembershipApprovalMode = types.GroupMembershipApprovalMode{IsJoinApprovalRequired: true}
		}
		if !client.IsConnected() {
			http.Error(w, "Not connected to WhatsApp", http.StatusServiceUnavailable)
			return
		}
		for _, p := range req.Participants {
			jid, err := resolveRecipientJID(client, p)
			if err != nil {
				http.Error(w, "invalid participant "+p+": "+err.Error(), http.StatusBadRequest)
				return
			}
			create.Participants = append(create.Participants, jid)
		}

		ctx, cancel := context.WithTimeout(r.Context(), groupRequestTimeout)
		defer cancel()
		info, err := client.CreateGroup(ctx, create)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		// The group exists at this point, so later failures are reported
		// alongside ok=true instead of failing the request.
		resp := map[string]interface{}{"ok": true, "jid": info.JID.String()}
		if link, err := client.GetGroupInviteLink(ctx, info.JID, false); err == nil {
			resp["invite_link"] = link
		} else {
			resp["invite_error"] = err.Error()
		}
		if req.Topic != "" {
			if err := client.SetGroupTopic(ctx, info.JID, "", "", req.Topic); err != nil {
				resp["topic_error"] = err.Error()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))

	mux.HandleFunc("/api/subgroups", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		community, err := parseGroupJID(r.URL.Query().Get("jid"))
		if err != nil {
			http.Error(w, "invalid jid: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !client.IsConnected() {
			http.Error(w, "Not connected to WhatsApp", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), groupRequestTimeout)
		defer cancel()
		subgroups, err := client.GetSubGroups(ctx, community)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(subgroups)
	}))
}
