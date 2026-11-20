package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// registerForwardEndpoint adds POST /api/forward
// {"chat_jid": "...", "message_id": "...", "recipient": "..."}.
// It resends a stored text message to another chat with WhatsApp's
// "Forwarded" label, the same thing the phone's Forward action produces.
// Only text is supported: forwarding media would need the original upload's
// mimetype, which the store does not keep.
func registerForwardEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, messageStore *MessageStore) {
	mux.HandleFunc("/api/forward", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeErr := func(status int, msg string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": msg})
		}
		var req struct {
			ChatJID   string `json:"chat_jid"`
			MessageID string `json:"message_id"`
			Recipient string `json:"recipient"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChatJID == "" || req.MessageID == "" || req.Recipient == "" {
			writeErr(http.StatusBadRequest, "chat_jid, message_id and recipient are required")
			return
		}
		if !client.IsConnected() {
			writeErr(http.StatusServiceUnavailable, "not connected to WhatsApp")
			return
		}

		// Bare phone numbers are stored under the default user server.
		sourceChatJID := req.ChatJID
		if !strings.Contains(sourceChatJID, "@") {
			sourceChatJID += "@" + types.DefaultUserServer
		}

		var content string
		var mediaType sql.NullString
		err := messageStore.db.QueryRow(
			"SELECT content, media_type FROM messages WHERE id = ? AND chat_jid = ?",
			req.MessageID, sourceChatJID,
		).Scan(&content, &mediaType)
		if err == sql.ErrNoRows {
			writeErr(http.StatusNotFound, "message not found in that chat")
			return
		}
		if err != nil {
			writeErr(http.StatusInternalServerError, fmt.Sprintf("store lookup failed: %v", err))
			return
		}
		if mediaType.String != "" || strings.TrimSpace(content) == "" {
			writeErr(http.StatusBadRequest, "only text messages can be forwarded")
			return
		}

		recipientJID, err := resolveRecipientJID(client, req.Recipient)
		if err != nil {
			writeErr(http.StatusBadRequest, fmt.Sprintf("invalid recipient: %v", err))
			return
		}
		msg := &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text:        proto.String(content),
			ContextInfo: &waProto.ContextInfo{IsForwarded: proto.Bool(true), ForwardingScore: proto.Uint32(1)},
		}}
		chatJID := resolveUserJID(client, recipientJID, types.EmptyJID).String()
		if settings, err := messageStore.GetChatEphemeralSettings(chatJID); err == nil {
			applyChatEphemeralSettings(msg, settings)
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		resp, err := client.SendMessage(ctx, recipientJID, msg)
		if err != nil {
			writeErr(http.StatusBadGateway, fmt.Sprintf("send failed: %v", err))
			return
		}
		// Same reason as sendWhatsAppMessage: our own sends are not echoed back.
		if client.Store.ID != nil {
			timestamp := resp.Timestamp
			if timestamp.IsZero() {
				timestamp = time.Now()
			}
			_ = messageStore.StoreChat(chatJID, "", timestamp)
			if err := messageStore.StoreMessage(resp.ID, chatJID, client.Store.ID.User, content, timestamp, true,
				"", "", "", nil, nil, nil, 0, ""); err != nil {
				fmt.Printf("Warning: forward sent but not stored: %v\n", err)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "id": resp.ID})
	}))
}
