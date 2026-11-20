package main

// "Delete for me": remove messages from this WhatsApp account only.
//
// WhatsApp implements it as an app state mutation (collection regular_high,
// index "deleteMessageForMe") that the phone and every linked device apply, so
// it works for messages of any sender and any age. whatsmeow ships the index
// constant and the protobuf for it but no Build helper, hence
// buildDeleteMessageForMePatch below. The other participants keep their copies.
//
// Deleting for everyone (revoking) is a different, send-side operation and is
// not handled here.
//
// Layout: deleteMessagesForMe is the transport-independent core. It only needs
// the three bridge capabilities in deleteForMeDeps, so it can be driven by the
// HTTP handler below, by another caller, or by a test with fakes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// maxDeleteForMeBatch caps the messages one request may delete. Keep in sync
// with MAX_DELETE_BATCH in whatsapp-mcp-server/whatsapp.py.
const maxDeleteForMeBatch = 200

// DeleteForMeRequest is the request body for the /api/delete-for-me endpoint.
type DeleteForMeRequest struct {
	ChatJID    string   `json:"chat_jid"`
	MessageIDs []string `json:"message_ids"`
}

// storedMessageRef is what an app state patch needs to address a message.
type storedMessageRef struct {
	ID        string
	Timestamp time.Time
	FromMe    bool
}

// GetMessageRefs returns the stored messages among ids in chatJID, keyed by ID.
// IDs that are not stored are simply absent from the result.
func (store *MessageStore) GetMessageRefs(chatJID string, ids []string) (map[string]storedMessageRef, error) {
	refs := make(map[string]storedMessageRef, len(ids))
	if len(ids) == 0 {
		return refs, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, 1+len(ids))
	args = append(args, chatJID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := store.db.Query(
		`SELECT id, timestamp, is_from_me FROM messages WHERE chat_jid = ? AND id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var rawTimestamp any
		var fromMe bool
		if err := rows.Scan(&id, &rawTimestamp, &fromMe); err != nil {
			return nil, err
		}
		ts := anchorTime(rawTimestamp)
		if ts.IsZero() {
			return nil, fmt.Errorf("message %q has an unreadable timestamp %v", id, rawTimestamp)
		}
		refs[id] = storedMessageRef{ID: id, Timestamp: ts, FromMe: fromMe}
	}
	return refs, rows.Err()
}

// DeleteMessages removes the given messages from the local store. Unlike
// MarkMessageDeleted (a retraction by the sender, which keeps the content), a
// delete-for-me erases the row: the user asked for the message to be gone.
func (store *MessageStore) DeleteMessages(chatJID string, ids []string) error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM messages WHERE id = ? AND chat_jid = ?`, id, chatJID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// buildDeleteMessageForMePatch builds one app state patch deleting every ref in
// chat for this account. The mutation index is
// [deleteMessageForMe, chat, messageID, fromMe, participant]; the participant
// slot is "0" for DMs and for our own messages, the only cases the core
// accepts. WhatsApp matches the message on chat + ID + fromMe, so the timestamp
// only needs to be close.
func buildDeleteMessageForMePatch(chat types.JID, refs []storedMessageRef) appstate.PatchInfo {
	mutations := make([]appstate.MutationInfo, 0, len(refs))
	for _, ref := range refs {
		fromMe := "0"
		if ref.FromMe {
			fromMe = "1"
		}
		mutations = append(mutations, appstate.MutationInfo{
			Index:   []string{appstate.IndexDeleteMessageForMe, chat.String(), ref.ID, fromMe, "0"},
			Version: 3,
			Value: &waSyncAction.SyncActionValue{
				DeleteMessageForMeAction: &waSyncAction.DeleteMessageForMeAction{
					DeleteMedia:      proto.Bool(true),
					MessageTimestamp: proto.Int64(ref.Timestamp.Unix()),
				},
			},
		})
	}
	return appstate.PatchInfo{Type: appstate.WAPatchRegularHigh, Mutations: mutations}
}

// deleteForMeDeps are the bridge capabilities the operation needs. They are
// injected so the connected path can be exercised without a live client.
type deleteForMeDeps struct {
	isConnected  func() bool
	resolveChat  func(chat string) (types.JID, error)
	sendAppState func(ctx context.Context, patch appstate.PatchInfo) error
}

func newDeleteForMeDeps(client *whatsmeow.Client) deleteForMeDeps {
	return deleteForMeDeps{
		isConnected: client.IsConnected,
		resolveChat: func(chat string) (types.JID, error) { return resolveRecipientJID(client, chat) },
		sendAppState: func(ctx context.Context, patch appstate.PatchInfo) error {
			return client.SendAppState(ctx, patch)
		},
	}
}

// deleteForMeError pairs a failure with the HTTP status that describes it.
type deleteForMeError struct {
	Status  int
	Message string
}

func (e *deleteForMeError) Error() string { return e.Message }

// deleteForMeResult reports what the core did.
type deleteForMeResult struct {
	Deleted int
	// Warning is set when the patch went out but the local cleanup failed.
	Warning string
}

// deleteMessagesForMe deletes ids from chatKey for this account. chatKey is the
// storage form of the chat JID (phone JID for DMs). Validation against the
// store is all-or-nothing and happens before any external side effect.
func deleteMessagesForMe(ctx context.Context, deps deleteForMeDeps, store *MessageStore, chatKey string, ids []string) (deleteForMeResult, *deleteForMeError) {
	chat, err := types.ParseJID(chatKey)
	if err != nil || chat.User == "" || chat.Server == "" {
		return deleteForMeResult{}, &deleteForMeError{http.StatusBadRequest, "Invalid chat_jid"}
	}

	stored, err := store.GetMessageRefs(chatKey, ids)
	if err != nil {
		return deleteForMeResult{}, &deleteForMeError{http.StatusInternalServerError, fmt.Sprintf("Failed to look up messages: %v", err)}
	}
	refs := make([]storedMessageRef, 0, len(ids))
	var missing []string
	for _, id := range ids {
		ref, ok := stored[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if chat.Server == types.GroupServer && !ref.FromMe {
			// The participant slot of the mutation index needs the sender in the
			// form the phone uses (PN or LID), which is not verified yet.
			return deleteForMeResult{}, &deleteForMeError{http.StatusBadRequest, "deleting other participants' group messages is not supported yet"}
		}
		refs = append(refs, ref)
	}
	if len(missing) > 0 {
		return deleteForMeResult{}, &deleteForMeError{http.StatusNotFound, fmt.Sprintf("messages not found in chat: %s", strings.Join(missing, ", "))}
	}

	if !deps.isConnected() {
		return deleteForMeResult{}, &deleteForMeError{http.StatusServiceUnavailable, "WhatsApp client is not connected. Please wait for reconnection."}
	}

	// MCP storage uses phone-form chat JIDs; address the chat the way the phone
	// does (PN -> LID for migrated DMs), as /api/mark-read does.
	appChat, err := deps.resolveChat(chatKey)
	if err != nil || appChat.User == "" || appChat.Server == "" {
		return deleteForMeResult{}, &deleteForMeError{http.StatusBadRequest, "Invalid chat_jid"}
	}

	if err := deps.sendAppState(ctx, buildDeleteMessageForMePatch(appChat, refs)); err != nil {
		return deleteForMeResult{}, &deleteForMeError{http.StatusInternalServerError, fmt.Sprintf("Failed to delete messages: %v", err)}
	}

	result := deleteForMeResult{Deleted: len(refs)}
	if err := store.DeleteMessages(chatKey, ids); err != nil {
		// The patch already went out; report it rather than failing the call.
		fmt.Printf("Warning: failed to remove deleted messages from the local store for %s: %v\n", chatKey, err)
		result.Warning = "local store cleanup failed"
	}
	return result, nil
}

// normalizeDeleteIDs trims and de-duplicates ids, keeping their order. The
// returned string is a client-facing reason when the list is unusable.
func normalizeDeleteIDs(raw []string) ([]string, string) {
	if len(raw) == 0 {
		return nil, "chat_jid and message_ids are required"
	}
	if len(raw) > maxDeleteForMeBatch {
		return nil, fmt.Sprintf("at most %d message_ids per request", maxDeleteForMeBatch)
	}
	ids := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, "message_ids must not contain empty values"
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, ""
}

func deleteForMeHandler(deps deleteForMeDeps, store *MessageStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req DeleteForMeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		if req.ChatJID == "" {
			http.Error(w, "chat_jid and message_ids are required", http.StatusBadRequest)
			return
		}
		ids, reason := normalizeDeleteIDs(req.MessageIDs)
		if reason != "" {
			http.Error(w, reason, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		result, derr := deleteMessagesForMe(context.Background(), deps, store, req.ChatJID, ids)
		if derr != nil {
			w.WriteHeader(derr.Status)
			_ = json.NewEncoder(w).Encode(SendMessageResponse{Success: false, Message: derr.Message})
			return
		}

		msg := fmt.Sprintf("%d message(s) deleted for me", result.Deleted)
		if result.Warning != "" {
			msg += " (" + result.Warning + ")"
		}
		fmt.Printf("← /api/delete-for-me chat=%q deleted=%d\n", req.ChatJID, result.Deleted)
		_ = json.NewEncoder(w).Encode(SendMessageResponse{Success: true, Message: msg})
	}
}

func registerDeleteForMeEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, messageStore *MessageStore) {
	mux.HandleFunc("/api/delete-for-me", auth(deleteForMeHandler(newDeleteForMeDeps(client), messageStore)))
}
