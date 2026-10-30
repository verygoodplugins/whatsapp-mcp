package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// WhatsApp Business "lists" (labels in the protocol) live in app state, not in
// messages. The bridge keeps a copy of every list and which chats are in it,
// fed by the LabelEdit / LabelAssociationChat events WhatsApp sends when any
// device changes them, so callers can read and change lists over REST.

func ensureLabelTables(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS labels (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			color INTEGER NOT NULL DEFAULT 0,
			list_type INTEGER NOT NULL DEFAULT 0,
			order_index INTEGER NOT NULL DEFAULT 0,
			deleted BOOLEAN NOT NULL DEFAULT 0,
			action TEXT NOT NULL DEFAULT '{}',
			updated_at TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS chat_labels (
			chat_jid TEXT NOT NULL,
			label_id TEXT NOT NULL,
			updated_at TIMESTAMP,
			PRIMARY KEY (chat_jid, label_id)
		);
		CREATE INDEX IF NOT EXISTS idx_chat_labels_label ON chat_labels(label_id);
	`)
	return err
}

func handleLabelEdit(store *MessageStore, v *events.LabelEdit) {
	a := v.Action
	if a == nil {
		return
	}
	// The whole action is kept so an edit from here can send it back with
	// only the name or colour changed (a label edit replaces every field).
	raw, err := protojson.Marshal(a)
	if err != nil {
		raw = []byte("{}")
	}
	_, err = store.db.Exec(`
		INSERT INTO labels (id, name, color, list_type, order_index, deleted, action, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, color=excluded.color,
			list_type=excluded.list_type, order_index=excluded.order_index,
			deleted=excluded.deleted, action=excluded.action, updated_at=excluded.updated_at`,
		v.LabelID, a.GetName(), a.GetColor(), int32(a.GetType()), a.GetOrderIndex(), a.GetDeleted(), string(raw), v.Timestamp)
	if err != nil {
		fmt.Printf("Failed to store label %s: %v\n", v.LabelID, err)
	}
}

func handleLabelAssociationChat(store *MessageStore, v *events.LabelAssociationChat) {
	var err error
	if v.Action != nil && v.Action.GetLabeled() {
		_, err = store.db.Exec(`INSERT INTO chat_labels (chat_jid, label_id, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(chat_jid, label_id) DO UPDATE SET updated_at=excluded.updated_at`,
			v.JID.String(), v.LabelID, v.Timestamp)
	} else {
		_, err = store.db.Exec(`DELETE FROM chat_labels WHERE chat_jid = ? AND label_id = ?`, v.JID.String(), v.LabelID)
	}
	if err != nil {
		fmt.Printf("Failed to store label %s on %s: %v\n", v.LabelID, v.JID, err)
	}
}

type labelChat struct {
	JID   string `json:"jid"`
	Phone string `json:"phone"` // digits; "" for a LID whose number WhatsApp never gave us
}

type labelRow struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Color      int32       `json:"color"`
	ListType   string      `json:"list_type"`
	OrderIndex int32       `json:"order_index"`
	Chats      []labelChat `json:"chats"`
}

// chatPhone is the number behind a chat JID: the user part of a phone JID,
// or the number WhatsApp mapped to a LID. The phone files list members under
// the LID, while callers usually know a contact by number.
func chatPhone(ctx context.Context, client *whatsmeow.Client, jid types.JID) string {
	switch jid.Server {
	case types.DefaultUserServer:
		return jid.User
	case types.HiddenUserServer:
		if pn, err := client.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
			return pn.User
		}
	}
	return ""
}

// phoneDigits reduces a phone number as typed ("+1 555-123-4567",
// "0015551234567") to its digits without an international "00" prefix,
// or "" when too few digits are left to be a number.
func phoneDigits(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := strings.TrimPrefix(b.String(), "00")
	if len(digits) < 6 {
		return ""
	}
	return digits
}

func listLabels(ctx context.Context, client *whatsmeow.Client, db *sql.DB) ([]labelRow, error) {
	rows, err := db.Query(`SELECT id, name, color, list_type, order_index FROM labels WHERE deleted = 0 ORDER BY order_index, CAST(id AS INTEGER)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	labels := []labelRow{}
	index := map[string]int{}
	for rows.Next() {
		var l labelRow
		var listType int32
		if err := rows.Scan(&l.ID, &l.Name, &l.Color, &listType, &l.OrderIndex); err != nil {
			return nil, err
		}
		l.ListType = waSyncAction.LabelEditAction_ListType(listType).String()
		l.Chats = []labelChat{}
		index[l.ID] = len(labels)
		labels = append(labels, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	assoc, err := db.Query(`SELECT label_id, chat_jid FROM chat_labels ORDER BY chat_jid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = assoc.Close() }()
	for assoc.Next() {
		var labelID, raw string
		if err := assoc.Scan(&labelID, &raw); err != nil {
			return nil, err
		}
		i, ok := index[labelID]
		if !ok {
			continue
		}
		chat := labelChat{JID: raw}
		if jid, err := types.ParseJID(raw); err == nil {
			chat.Phone = chatPhone(ctx, client, jid)
		}
		labels[i].Chats = append(labels[i].Chats, chat)
	}
	if err := assoc.Err(); err != nil {
		return nil, err
	}
	return labels, nil
}

// storedLabelAction is the last action seen for a list, nil if it is unknown.
func storedLabelAction(db *sql.DB, id string) (*waSyncAction.LabelEditAction, error) {
	var raw string
	err := db.QueryRow(`SELECT action FROM labels WHERE id = ?`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	action := &waSyncAction.LabelEditAction{}
	if err := protojson.Unmarshal([]byte(raw), action); err != nil {
		return nil, err
	}
	return action, nil
}

// newLabelSlot picks the id and position for a new list. WhatsApp ids are
// small integers chosen by the device that creates the list.
func newLabelSlot(db *sql.DB) (string, int32, error) {
	var maxID, maxOrder sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(CAST(id AS INTEGER)), MAX(order_index) FROM labels`).Scan(&maxID, &maxOrder); err != nil {
		return "", 0, err
	}
	return strconv.FormatInt(maxID.Int64+1, 10), int32(maxOrder.Int64 + 1), nil
}

// registerLabelEndpoints adds:
//
//	GET  /api/labels[?resync=1]   every list with the chats in it; resync re-reads app state first
//	POST /api/labels {id?, name?, color?, deleted?}   create (no id), rename/recolour or delete a list
//	POST /api/labels/chat {jid|phone, label_id, labeled}   put a chat in a list or take it out;
//	     a phone is resolved to the chat's LID as the phone app files it, a jid is used as given
func registerLabelEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, store *MessageStore) {
	writeErr := func(w http.ResponseWriter, status int, msg string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": msg})
	}

	mux.HandleFunc("/api/labels", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			if r.URL.Query().Get("resync") == "1" {
				if !client.IsConnected() {
					writeErr(w, http.StatusServiceUnavailable, "not connected to WhatsApp")
					return
				}
				// A full sync replays every label event; start from empty so
				// lists and memberships removed meanwhile don't linger.
				if _, err := store.db.Exec(`DELETE FROM chat_labels; DELETE FROM labels;`); err != nil {
					writeErr(w, http.StatusInternalServerError, err.Error())
					return
				}
				if err := client.FetchAppState(ctx, appstate.WAPatchRegular, true, false); err != nil {
					writeErr(w, http.StatusBadGateway, fmt.Sprintf("FetchAppState failed: %v", err))
					return
				}
			}
			labels, err := listLabels(ctx, client, store.db)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "labels": labels})

		case http.MethodPost:
			var req struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Color   *int32 `json:"color"`
				Deleted bool   `json:"deleted"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			req.Name = strings.TrimSpace(req.Name)
			if req.ID == "" && req.Name == "" {
				writeErr(w, http.StatusBadRequest, "name is required for a new list")
				return
			}
			if !client.IsConnected() {
				writeErr(w, http.StatusServiceUnavailable, "not connected to WhatsApp")
				return
			}
			var action *waSyncAction.LabelEditAction
			if req.ID != "" {
				stored, err := storedLabelAction(store.db, req.ID)
				if err != nil {
					writeErr(w, http.StatusInternalServerError, err.Error())
					return
				}
				if stored == nil {
					writeErr(w, http.StatusNotFound, "no such list")
					return
				}
				action = stored
			} else {
				id, order, err := newLabelSlot(store.db)
				if err != nil {
					writeErr(w, http.StatusInternalServerError, err.Error())
					return
				}
				req.ID = id
				// The same fields the phone writes for a list it creates;
				// whatsmeow's BuildLabelEdit leaves the type out, and the phone
				// only shows type CUSTOM entries as lists.
				action = &waSyncAction.LabelEditAction{
					Color:         proto.Int32(0),
					OrderIndex:    proto.Int32(order),
					IsActive:      proto.Bool(true),
					Type:          waSyncAction.LabelEditAction_CUSTOM.Enum(),
					IsImmutable:   proto.Bool(false),
					MuteEndTimeMS: proto.Int64(0),
				}
			}
			if req.Name != "" {
				action.Name = proto.String(req.Name)
			}
			if req.Color != nil {
				action.Color = req.Color
			}
			action.Deleted = proto.Bool(req.Deleted)
			patch := appstate.PatchInfo{
				Type: appstate.WAPatchRegular,
				Mutations: []appstate.MutationInfo{{
					Index:   []string{appstate.IndexLabelEdit, req.ID},
					Version: 3,
					Value:   &waSyncAction.SyncActionValue{LabelEditAction: action},
				}},
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			if err := client.SendAppState(ctx, patch); err != nil {
				writeErr(w, http.StatusBadGateway, fmt.Sprintf("SendAppState failed: %v", err))
				return
			}
			handleLabelEdit(store, &events.LabelEdit{Timestamp: time.Now(), LabelID: req.ID, Action: action})
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "id": req.ID, "name": action.GetName(), "deleted": req.Deleted})

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/api/labels/chat", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			JID     string `json:"jid"`
			Phone   string `json:"phone"`
			LabelID string `json:"label_id"`
			Labeled bool   `json:"labeled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		phone := phoneDigits(req.Phone)
		if req.LabelID == "" || (req.JID == "" && phone == "") {
			writeErr(w, http.StatusBadRequest, "jid or phone, and label_id, are required")
			return
		}
		if !client.IsConnected() {
			writeErr(w, http.StatusServiceUnavailable, "not connected to WhatsApp")
			return
		}
		var jid types.JID
		var err error
		if req.JID != "" {
			jid, err = types.ParseJID(req.JID)
		} else {
			jid, err = resolveRecipientJID(client, phone)
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := client.SendAppState(ctx, appstate.BuildLabelChat(jid, req.LabelID, req.Labeled)); err != nil {
			writeErr(w, http.StatusBadGateway, fmt.Sprintf("SendAppState failed: %v", err))
			return
		}
		handleLabelAssociationChat(store, &events.LabelAssociationChat{JID: jid, Timestamp: time.Now(), LabelID: req.LabelID,
			Action: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(req.Labeled)}})
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "jid": jid.String(), "label_id": req.LabelID, "labeled": req.Labeled})
	}))
}
