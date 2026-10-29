package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// WhatsApp's own limits for changing a sent message, as its apps apply them:
// an edit within 15 minutes, "delete for everyone" within about two days.
const (
	editWindow   = 15 * time.Minute
	revokeWindow = 48 * time.Hour
)

// OwnMessageRequest is the request body for /api/edit and /api/revoke.
type OwnMessageRequest struct {
	ChatJID   string `json:"chat_jid"`
	MessageID string `json:"message_id"`
	Text      string `json:"text,omitempty"` // /api/edit only: the new text
}

// OwnMessage returns when we sent messageID in chatJID and its media type;
// found is false when the row is missing, someone else's, or already deleted.
func (store *MessageStore) OwnMessage(messageID, chatJID string) (sent time.Time, mediaType string, found bool, err error) {
	var media sql.NullString
	err = store.db.QueryRow(
		`SELECT timestamp, media_type FROM messages
		 WHERE id = ? AND chat_jid = ? AND is_from_me = 1 AND deleted_at IS NULL`,
		messageID, chatJID,
	).Scan(&sent, &media)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", false, nil
	}
	if err != nil {
		return time.Time{}, "", false, err
	}
	return sent, media.String, true, nil
}

// registerOwnMessageEndpoints adds /api/edit (new text for one of our text
// messages) and /api/revoke (delete one of our messages for everyone). Our
// own sends never come back as events, so the store is updated here, the
// same way an edit or revoke from another of our devices is recorded.
func registerOwnMessageEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, store *MessageStore) {
	mux.HandleFunc("/api/edit", auth(func(w http.ResponseWriter, r *http.Request) {
		changeOwnMessage(w, r, client, store, false)
	}))
	mux.HandleFunc("/api/revoke", auth(func(w http.ResponseWriter, r *http.Request) {
		changeOwnMessage(w, r, client, store, true)
	}))
}

func changeOwnMessage(w http.ResponseWriter, r *http.Request, client *whatsmeow.Client, store *MessageStore, revoke bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req OwnMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChatJID == "" || req.MessageID == "" ||
		(!revoke && strings.TrimSpace(req.Text) == "") {
		http.Error(w, "chat_jid, message_id and (for an edit) text are required", http.StatusBadRequest)
		return
	}
	chatJID, err := types.ParseJID(req.ChatJID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid chat_jid: %v", err), http.StatusBadRequest)
		return
	}
	sent, mediaType, found, err := store.OwnMessage(req.MessageID, req.ChatJID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "Not a message we sent in this chat, or already deleted", http.StatusNotFound)
		return
	}
	window, limit := editWindow, "15 minutes"
	if revoke {
		window, limit = revokeWindow, "48 hours"
	}
	if time.Since(sent) > window {
		http.Error(w, "Too late: WhatsApp allows this within "+limit+" of sending", http.StatusConflict)
		return
	}
	if !revoke && mediaType != "" {
		http.Error(w, "Only text messages can be edited", http.StatusBadRequest)
		return
	}

	var msg *waProto.Message
	if revoke {
		msg = client.BuildRevoke(chatJID, types.EmptyJID, req.MessageID)
	} else {
		msg = client.BuildEdit(chatJID, req.MessageID, &waProto.Message{Conversation: proto.String(req.Text)})
	}
	resp, err := client.SendMessage(context.Background(), chatJID, msg)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if revoke {
		err = store.MarkMessageDeleted(req.MessageID, req.ChatJID, resp.Timestamp)
	} else {
		err = store.MarkMessageEdited(req.MessageID, req.ChatJID, req.Text, resp.Timestamp)
	}
	if err != nil {
		fmt.Printf("Sent, but failed to record it in the store: %v\n", err)
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
