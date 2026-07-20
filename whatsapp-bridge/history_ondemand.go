package main

// On-demand history sync.
//
// whatsmeow only *receives* whatever history the phone pushes at pair time.
// `--full-history-pair` widens that request, but it only applies to a fresh
// pair, so recovering a gap in a single chat otherwise means deleting
// whatsapp.db, re-scanning the QR and re-syncing everything.
//
// whatsmeow already exposes the primitive for a targeted request
// (Client.BuildHistorySyncRequest); it just isn't reachable from the bridge.
// This file wires it to POST /api/history so older messages can be requested
// for one chat at runtime. Results arrive through the existing
// events.HistorySync handler, so no new storage or parsing path is introduced.
//
// As with pair-time sync, the phone has the final word on how much it returns
// (see AGENTS.md gotcha #4), so Count is a request, not a guarantee.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// HistoryRequest is the JSON body accepted by POST /api/history.
type HistoryRequest struct {
	ChatJID string `json:"chat_jid"`
	Count   int    `json:"count,omitempty"`
}

const (
	// defaultHistoryCount is used when the caller omits count.
	defaultHistoryCount = 50
	// maxHistoryCount bounds a single request so a typo can't ask the phone
	// for an unbounded backfill.
	maxHistoryCount = 500
)

// clampHistoryCount normalises a requested message count into a sane range.
func clampHistoryCount(n int) int {
	switch {
	case n <= 0:
		return defaultHistoryCount
	case n > maxHistoryCount:
		return maxHistoryCount
	default:
		return n
	}
}

// anchorTime converts the timestamp column into a time.Time. Depending on the
// SQLite driver and how the row was written, the value comes back either
// already typed or as a formatted string, so both are handled.
func anchorTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case []byte:
		return anchorTime(string(t))
	case string:
		for _, layout := range []string{
			"2006-01-02 15:04:05 -0700 MST",
			"2006-01-02 15:04:05.999999999 -0700 MST",
			time.RFC3339,
		} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

// historyAnchorInfo builds the MessageInfo that anchors an on-demand history
// request. The phone returns messages from *before* this message, so callers
// pass the oldest message already stored for the chat.
//
// Sender matters for group chats, where the anchor's author is a participant
// rather than the chat itself; for our own messages it is the paired account.
func historyAnchorInfo(chat types.JID, msgID, sender string, fromMe bool, ts time.Time, own types.JID) types.MessageInfo {
	senderJID := own
	if !fromMe {
		// Default to the chat itself, which is correct for a 1:1 chat.
		senderJID = chat
		if sender != "" {
			if strings.Contains(sender, "@") {
				if parsed, err := types.ParseJID(sender); err == nil {
					senderJID = parsed
				}
			} else {
				senderJID = types.JID{User: sender, Server: types.DefaultUserServer}
			}
		}
	}
	return types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   senderJID,
			IsFromMe: fromMe,
			IsGroup:  chat.Server == types.GroupServer,
		},
		ID:        msgID,
		Timestamp: ts,
	}
}

// oldestStoredMessage returns the anchor fields for the oldest message the
// bridge currently holds for a chat.
func oldestStoredMessage(store *MessageStore, chatJID string) (id, sender string, fromMe bool, ts time.Time, err error) {
	var rawTS any
	err = store.db.QueryRow(
		`SELECT id, sender, is_from_me, timestamp
		   FROM messages
		  WHERE chat_jid = ?
		  ORDER BY timestamp ASC
		  LIMIT 1`,
		chatJID,
	).Scan(&id, &sender, &fromMe, &rawTS)
	if err != nil {
		return "", "", false, time.Time{}, err
	}
	return id, sender, fromMe, anchorTime(rawTS), nil
}

// registerHistoryEndpoint wires POST /api/history onto an existing mux.
func registerHistoryEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, messageStore *MessageStore) {
	mux.HandleFunc("/api/history", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		fmt.Printf("→ /api/history from=%q user_agent=%q\n", r.RemoteAddr, r.UserAgent())

		var req HistoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		if req.ChatJID == "" {
			http.Error(w, "chat_jid is required", http.StatusBadRequest)
			return
		}
		count := clampHistoryCount(req.Count)

		w.Header().Set("Content-Type", "application/json")

		writeErr := func(status int, msg string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(SendMessageResponse{Success: false, Message: msg})
		}

		if !client.IsConnected() {
			writeErr(http.StatusServiceUnavailable, "Not connected to WhatsApp")
			return
		}
		if client.Store == nil || client.Store.ID == nil {
			writeErr(http.StatusServiceUnavailable, "Client is not paired")
			return
		}

		chatJID, err := types.ParseJID(req.ChatJID)
		if err != nil {
			writeErr(http.StatusBadRequest, fmt.Sprintf("Invalid chat_jid: %v", err))
			return
		}

		id, sender, fromMe, ts, err := oldestStoredMessage(messageStore, req.ChatJID)
		if err != nil {
			writeErr(http.StatusNotFound,
				"No stored message for this chat to anchor the request; send or receive one message first")
			return
		}
		if ts.IsZero() {
			writeErr(http.StatusInternalServerError, "Stored anchor message has an unreadable timestamp")
			return
		}

		own := client.Store.ID.ToNonAD()
		info := historyAnchorInfo(chatJID, id, sender, fromMe, ts, own)

		msg := client.BuildHistorySyncRequest(&info, count)
		if msg == nil {
			writeErr(http.StatusInternalServerError, "Failed to build history sync request")
			return
		}

		if _, err := client.SendMessage(context.Background(), own, msg, whatsmeow.SendRequestExtra{Peer: true}); err != nil {
			writeErr(http.StatusInternalServerError, fmt.Sprintf("Failed to send history request: %v", err))
			return
		}

		fmt.Printf("← /api/history chat=%q count=%d anchor=%q\n", req.ChatJID, count, id)
		_ = json.NewEncoder(w).Encode(SendMessageResponse{
			Success: true,
			Message: fmt.Sprintf(
				"Requested up to %d messages older than %s for %s. History arrives asynchronously; the phone decides how much is returned.",
				count, ts.Format(time.RFC3339), req.ChatJID),
		})
	}))
}
