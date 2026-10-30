package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// GroupParticipantInfo is one member of a group, as GET /api/group-info
// reports it. JID is the phone-number JID where the bridge can resolve one
// (a member known only by LID keeps the LID); Phone is its user part when it
// is a phone number, else empty.
type GroupParticipantInfo struct {
	JID          string `json:"jid"`
	Phone        string `json:"phone,omitempty"`
	IsAdmin      bool   `json:"is_admin"`
	IsSuperAdmin bool   `json:"is_super_admin"`
}

// GroupInfoResponse is the body of GET /api/group-info.
type GroupInfoResponse struct {
	JID          string                 `json:"jid"`
	Name         string                 `json:"name"`
	Participants []GroupParticipantInfo `json:"participants"`
}

// groupInfoResponse maps whatsmeow's group info to the response, resolving
// each member through resolve (resolveUserJID in the bridge).
func groupInfoResponse(info *types.GroupInfo, resolve func(j, alt types.JID) types.JID) GroupInfoResponse {
	out := GroupInfoResponse{JID: info.JID.String(), Name: info.Name, Participants: make([]GroupParticipantInfo, 0, len(info.Participants))}
	for _, p := range info.Participants {
		j := resolve(p.JID, p.PhoneNumber)
		gp := GroupParticipantInfo{JID: j.String(), IsAdmin: p.IsAdmin, IsSuperAdmin: p.IsSuperAdmin}
		if j.Server == types.DefaultUserServer {
			gp.Phone = j.User
		}
		out.Participants = append(out.Participants, gp)
	}
	return out
}

// registerGroupInfoEndpoint serves GET /api/group-info?jid=<group>@g.us: the
// group's name and members, fetched live from WhatsApp.
func registerGroupInfoEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client) {
	mux.HandleFunc("/api/group-info", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeErr := func(status int, msg string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(SendMessageResponse{Success: false, Message: msg})
		}
		jid, err := types.ParseJID(r.URL.Query().Get("jid"))
		if err != nil || jid.Server != types.GroupServer {
			writeErr(http.StatusBadRequest, "jid must be a group JID (...@g.us)")
			return
		}
		if !client.IsConnected() {
			writeErr(http.StatusServiceUnavailable, "Not connected to WhatsApp")
			return
		}
		info, err := client.GetGroupInfo(context.Background(), jid)
		if err != nil {
			writeErr(http.StatusBadGateway, fmt.Sprintf("Failed to get group info: %v", err))
			return
		}
		_ = json.NewEncoder(w).Encode(groupInfoResponse(info, func(j, alt types.JID) types.JID { return resolveUserJID(client, j, alt) }))
	}))
}
