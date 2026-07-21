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
// for one chat at runtime. The response arrives as an events.HistorySync with
// type ON_DEMAND, which the existing handler already stores, so no new storage
// or parsing path is introduced.
//
// As with pair-time sync, the phone has the final word on how much it returns
// (see AGENTS.md gotcha #4), so Count is a request, not a guarantee.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	// defaultHistoryCount is used when the caller omits count. whatsmeow
	// documents 50 as the recommended number to request at a time.
	defaultHistoryCount = 50
	// maxHistoryCount bounds a single request so a typo can't ask the phone
	// for an unbounded backfill. Callers wanting more should page by calling
	// again, since the anchor moves back as older messages land.
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

// anchorTime converts the stored timestamp column into a time.Time. The type
// depends on the SQLite driver: cgo go-sqlite3 parses a column declared
// TIMESTAMP into a time.Time on scan (the production path, handled by the first
// case), while a pure-Go driver hands back the raw string.
//
// The string layouts must cover every format a driver might have *written*. We
// list them literally rather than importing go-sqlite3's exported
// SQLiteTimestampFormats, because that identifier lives in a cgo file and
// disappears under CGO_ENABLED=0 — importing it would break builds without a C
// compiler.
func anchorTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case []byte:
		return anchorTime(string(t))
	case string:
		for _, layout := range []string{
			"2006-01-02 15:04:05 -0700 MST",           // Go's time.Time.String() (e.g. modernc default)
			"2006-01-02 15:04:05.999999999 -0700 MST", // same, with fractional seconds
			"2006-01-02 15:04:05.999999999-07:00",     // cgo go-sqlite3 write format
			"2006-01-02T15:04:05.999999999-07:00",     // cgo go-sqlite3 write format, RFC3339-ish T
			time.RFC3339,
		} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

// historyAnchorInfo builds the anchor MessageInfo for an on-demand history
// request. The phone returns messages from *before* this message, so callers
// pass the oldest message already stored for the chat.
//
// BuildHistorySyncRequest reads only Chat, ID, IsFromMe and Timestamp — the
// HistorySyncOnDemandRequest wire message carries no field for the anchor's
// sender or a group flag — so only those four are set here.
func historyAnchorInfo(chat types.JID, msgID string, fromMe bool, ts time.Time) types.MessageInfo {
	return types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			IsFromMe: fromMe,
		},
		ID:        msgID,
		Timestamp: ts,
	}
}

// oldestStoredMessage returns the anchor fields for the oldest message the
// bridge currently holds for a chat. It returns sql.ErrNoRows when the chat has
// no stored messages to anchor on.
func oldestStoredMessage(store *MessageStore, chatJID string) (id string, fromMe bool, ts time.Time, err error) {
	var rawTS any
	err = store.db.QueryRow(
		`SELECT id, is_from_me, timestamp
		   FROM messages
		  WHERE chat_jid = ?
		  ORDER BY timestamp ASC
		  LIMIT 1`,
		chatJID,
	).Scan(&id, &fromMe, &rawTS)
	if err != nil {
		return "", false, time.Time{}, err
	}
	return id, fromMe, anchorTime(rawTS), nil
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

		id, fromMe, ts, err := oldestStoredMessage(messageStore, req.ChatJID)
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(http.StatusNotFound,
				"No stored message for this chat to anchor the request; send or receive one message first")
			return
		}
		if err != nil {
			writeErr(http.StatusInternalServerError, fmt.Sprintf("Failed to look up anchor message: %v", err))
			return
		}
		if ts.IsZero() {
			writeErr(http.StatusInternalServerError, "Stored anchor message has an unreadable timestamp")
			return
		}

		info := historyAnchorInfo(chatJID, id, fromMe, ts)

		msg := client.BuildHistorySyncRequest(&info, count)
		if msg == nil {
			writeErr(http.StatusInternalServerError, "Failed to build history sync request")
			return
		}

		// SendPeerMessage is the transport whatsmeow documents for this request
		// type. It resolves our own JID and sets the peer flag internally, so
		// that addressing logic isn't duplicated (and drifted) here.
		if _, err := client.SendPeerMessage(context.Background(), msg); err != nil {
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
