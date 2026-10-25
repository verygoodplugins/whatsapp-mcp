package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

const deleteForMeToken = "supersecrettoken1234567890abcdef"

func seedMessage(t *testing.T, ms *MessageStore, id, chatJID string, fromMe bool, ts time.Time) {
	t.Helper()
	if _, err := ms.db.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me)
		 VALUES (?, ?, '15551234567', 'hi', ?, ?)`,
		id, chatJID, ts, fromMe,
	); err != nil {
		t.Fatalf("seed message %s: %v", id, err)
	}
}

func countMessages(t *testing.T, ms *MessageStore, chatJID string) int {
	t.Helper()
	var n int
	if err := ms.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_jid = ?`, chatJID).Scan(&n); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return n
}

func postJSON(t *testing.T, handler http.Handler, method, body string, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1:8080/api/delete-for-me", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

// fakeBridge stands in for the live WhatsApp client behind deleteForMeDeps.
type fakeBridge struct {
	connected  bool
	resolved   types.JID
	resolveErr error
	sendErr    error
	patches    []appstate.PatchInfo
}

func (f *fakeBridge) deps() deleteForMeDeps {
	return deleteForMeDeps{
		isConnected: func() bool { return f.connected },
		resolveChat: func(string) (types.JID, error) { return f.resolved, f.resolveErr },
		sendAppState: func(_ context.Context, patch appstate.PatchInfo) error {
			f.patches = append(f.patches, patch)
			return f.sendErr
		},
	}
}

func connectedBridge() *fakeBridge {
	return &fakeBridge{connected: true, resolved: types.NewJID("123456789012345", types.HiddenUserServer)}
}

func TestBuildDeleteMessageForMePatch(t *testing.T) {
	chat := types.NewJID("15551234567", types.DefaultUserServer)
	ts := time.Unix(1760000000, 0)
	patch := buildDeleteMessageForMePatch(chat, []storedMessageRef{
		{ID: "IN1", Timestamp: ts, FromMe: false},
		{ID: "OUT1", Timestamp: ts.Add(time.Minute), FromMe: true},
	})

	if patch.Type != appstate.WAPatchRegularHigh {
		t.Fatalf("patch type = %q, want %q", patch.Type, appstate.WAPatchRegularHigh)
	}
	if len(patch.Mutations) != 2 {
		t.Fatalf("got %d mutations, want 2", len(patch.Mutations))
	}
	wantIndex := [][]string{
		{"deleteMessageForMe", chat.String(), "IN1", "0", "0"},
		{"deleteMessageForMe", chat.String(), "OUT1", "1", "0"},
	}
	for i, m := range patch.Mutations {
		if strings.Join(m.Index, "|") != strings.Join(wantIndex[i], "|") {
			t.Errorf("mutation %d index = %v, want %v", i, m.Index, wantIndex[i])
		}
		if m.Version != 3 {
			t.Errorf("mutation %d version = %d, want 3", i, m.Version)
		}
		action := m.Value.GetDeleteMessageForMeAction()
		if action == nil {
			t.Fatalf("mutation %d has no DeleteMessageForMeAction", i)
		}
		if !action.GetDeleteMedia() {
			t.Errorf("mutation %d should ask the phone to drop the media too", i)
		}
	}
	if got := patch.Mutations[1].Value.GetDeleteMessageForMeAction().GetMessageTimestamp(); got != ts.Add(time.Minute).Unix() {
		t.Errorf("message timestamp = %d, want %d", got, ts.Add(time.Minute).Unix())
	}
}

func TestNormalizeDeleteIDs(t *testing.T) {
	ids, reason := normalizeDeleteIDs([]string{" a ", "b", "a"})
	if reason != "" || strings.Join(ids, ",") != "a,b" {
		t.Errorf("got %v %q, want [a b] with no reason", ids, reason)
	}
	for name, raw := range map[string][]string{
		"empty list":     nil,
		"blank id":       {"a", "  "},
		"over the limit": make([]string, maxDeleteForMeBatch+1),
	} {
		if _, reason := normalizeDeleteIDs(raw); reason == "" {
			t.Errorf("%s: expected a rejection reason", name)
		}
	}
}

func TestDeleteForMeHandler_InvalidRequests_Return400(t *testing.T) {
	handler := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, deleteForMeToken, nil)
	tooMany := `"` + strings.Repeat(`x","`, maxDeleteForMeBatch) + `x"`

	cases := []struct {
		name string
		body string
	}{
		{"empty body", `{}`},
		{"missing message_ids", `{"chat_jid":"15551234567@s.whatsapp.net"}`},
		{"missing chat_jid", `{"message_ids":["3AABCDEF01234567"]}`},
		{"empty message_id", `{"message_ids":["3AABCDEF01234567","  "],"chat_jid":"15551234567@s.whatsapp.net"}`},
		{"invalid chat_jid", `{"message_ids":["3AABCDEF01234567"],"chat_jid":"@s.whatsapp.net"}`},
		{"too many message_ids", `{"message_ids":[` + tooMany + `],"chat_jid":"15551234567@s.whatsapp.net"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if resp := postJSON(t, handler, http.MethodPost, tc.body, deleteForMeToken); resp.Code != http.StatusBadRequest {
				t.Errorf("body=%q: expected 400, got %d (%s)", tc.body, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestDeleteForMeHandler_AuthAndMethod(t *testing.T) {
	handler := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, deleteForMeToken, nil)
	body := `{"message_ids":["3AABCDEF01234567"],"chat_jid":"15551234567@s.whatsapp.net"}`

	if resp := postJSON(t, handler, http.MethodPost, body, ""); resp.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", resp.Code)
	}
	if resp := postJSON(t, handler, http.MethodGet, body, deleteForMeToken); resp.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", resp.Code)
	}
}

func TestDeleteForMeHandler_Disconnected_Returns503AndKeepsMessages(t *testing.T) {
	ms := newTestMessageStore(t)
	chat := "15551234567@s.whatsapp.net"
	seedMessage(t, ms, "DM1", chat, false, time.Unix(1760000000, 0))
	// Through the real mux: the unconnected test client must report 503.
	handler := newRESTMux(newTestClient(&mockLIDStore{}), ms, 8080, deleteForMeToken, nil)

	resp := postJSON(t, handler, http.MethodPost, `{"chat_jid":"`+chat+`","message_ids":["DM1"]}`, deleteForMeToken)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for disconnected client, got %d (%s)", resp.Code, resp.Body.String())
	}
	if n := countMessages(t, ms, chat); n != 1 {
		t.Errorf("nothing may be deleted locally when the patch was not sent, %d rows left", n)
	}
}

func TestDeleteForMe_UnknownMessage_Returns404AndSendsNothing(t *testing.T) {
	ms := newTestMessageStore(t)
	chat := "15551234567@s.whatsapp.net"
	seedMessage(t, ms, "KNOWN", chat, false, time.Unix(1760000000, 0))
	bridge := connectedBridge()

	resp := postJSON(t, deleteForMeHandler(bridge.deps(), ms), http.MethodPost,
		`{"chat_jid":"`+chat+`","message_ids":["KNOWN","GHOST"]}`, "")

	if resp.Code != http.StatusNotFound || !strings.Contains(resp.Body.String(), "GHOST") {
		t.Fatalf("expected 404 naming GHOST, got %d (%s)", resp.Code, resp.Body.String())
	}
	if len(bridge.patches) != 0 || countMessages(t, ms, chat) != 1 {
		t.Errorf("a rejected request must not send a patch or delete rows (patches=%d)", len(bridge.patches))
	}
}

func TestDeleteForMe_GroupMessageFromOthers_Returns400(t *testing.T) {
	ms := newTestMessageStore(t)
	group := "120363012345678901@g.us"
	seedMessage(t, ms, "THEIRS", group, false, time.Unix(1760000000, 0))
	bridge := connectedBridge()

	resp := postJSON(t, deleteForMeHandler(bridge.deps(), ms), http.MethodPost,
		`{"chat_jid":"`+group+`","message_ids":["THEIRS"]}`, "")

	if resp.Code != http.StatusBadRequest || len(bridge.patches) != 0 {
		t.Errorf("expected 400 and no patch, got %d (patches=%d)", resp.Code, len(bridge.patches))
	}
}

func TestDeleteForMe_SendsPatchForResolvedChatAndRemovesRows(t *testing.T) {
	ms := newTestMessageStore(t)
	chat := "15551234567@s.whatsapp.net"
	ts := time.Unix(1760000000, 0)
	seedMessage(t, ms, "THEIRS", chat, false, ts)
	seedMessage(t, ms, "MINE", chat, true, ts.Add(time.Minute))
	seedMessage(t, ms, "KEEP", chat, false, ts.Add(2*time.Minute))
	bridge := connectedBridge()

	resp := postJSON(t, deleteForMeHandler(bridge.deps(), ms), http.MethodPost,
		`{"chat_jid":"`+chat+`","message_ids":["THEIRS","MINE","THEIRS"]}`, "")

	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "2 message(s) deleted for me") {
		t.Fatalf("expected 200 with 2 deleted, got %d (%s)", resp.Code, resp.Body.String())
	}
	if len(bridge.patches) != 1 || len(bridge.patches[0].Mutations) != 2 {
		t.Fatalf("expected one patch with 2 mutations (duplicate ID collapsed), got %+v", bridge.patches)
	}
	first := bridge.patches[0].Mutations[0].Index
	if first[1] != bridge.resolved.String() || first[2] != "THEIRS" || first[3] != "0" {
		t.Errorf("patch must address the resolved chat and original direction, got %v", first)
	}
	if n := countMessages(t, ms, chat); n != 1 {
		t.Errorf("only KEEP should remain locally, %d rows left", n)
	}
}

func TestDeleteForMe_OwnGroupMessage_IsAllowed(t *testing.T) {
	ms := newTestMessageStore(t)
	group := "120363012345678901@g.us"
	seedMessage(t, ms, "MINE", group, true, time.Unix(1760000000, 0))
	bridge := &fakeBridge{connected: true, resolved: types.NewJID("120363012345678901", types.GroupServer)}

	resp := postJSON(t, deleteForMeHandler(bridge.deps(), ms), http.MethodPost,
		`{"chat_jid":"`+group+`","message_ids":["MINE"]}`, "")

	if resp.Code != http.StatusOK || len(bridge.patches) != 1 {
		t.Fatalf("own group messages are supported, got %d (patches=%d)", resp.Code, len(bridge.patches))
	}
	if idx := bridge.patches[0].Mutations[0].Index; idx[3] != "1" || idx[4] != "0" {
		t.Errorf("own message needs fromMe=1 and participant 0, got %v", idx)
	}
}

func TestDeleteForMe_SendFailure_KeepsRows(t *testing.T) {
	ms := newTestMessageStore(t)
	chat := "15551234567@s.whatsapp.net"
	seedMessage(t, ms, "A", chat, false, time.Unix(1760000000, 0))
	bridge := connectedBridge()
	bridge.sendErr = errors.New("app state conflict")

	resp := postJSON(t, deleteForMeHandler(bridge.deps(), ms), http.MethodPost,
		`{"chat_jid":"`+chat+`","message_ids":["A"]}`, "")

	if resp.Code != http.StatusInternalServerError || !strings.Contains(resp.Body.String(), "app state conflict") {
		t.Fatalf("expected 500 carrying the cause, got %d (%s)", resp.Code, resp.Body.String())
	}
	if n := countMessages(t, ms, chat); n != 1 {
		t.Errorf("rows must survive a failed patch so the call can be retried, %d left", n)
	}
}

func TestDeleteForMe_ResolveFailure_Returns400(t *testing.T) {
	ms := newTestMessageStore(t)
	chat := "15551234567@s.whatsapp.net"
	seedMessage(t, ms, "A", chat, false, time.Unix(1760000000, 0))
	bridge := connectedBridge()
	bridge.resolveErr = errors.New("no such user")

	resp := postJSON(t, deleteForMeHandler(bridge.deps(), ms), http.MethodPost,
		`{"chat_jid":"`+chat+`","message_ids":["A"]}`, "")

	if resp.Code != http.StatusBadRequest || len(bridge.patches) != 0 || countMessages(t, ms, chat) != 1 {
		t.Errorf("expected 400 with nothing sent or deleted, got %d", resp.Code)
	}
}

func TestMessageStore_GetMessageRefsAndDeleteMessages(t *testing.T) {
	ms := newTestMessageStore(t)
	chat, other := "15551234567@s.whatsapp.net", "15557654321@s.whatsapp.net"
	ts := time.Unix(1760000000, 0)
	seedMessage(t, ms, "A", chat, true, ts)
	seedMessage(t, ms, "B", chat, false, ts.Add(time.Hour))
	seedMessage(t, ms, "A", other, false, ts)

	refs, err := ms.GetMessageRefs(chat, []string{"A", "B", "GHOST"})
	if err != nil {
		t.Fatalf("GetMessageRefs: %v", err)
	}
	if len(refs) != 2 || !refs["A"].FromMe || refs["B"].FromMe {
		t.Fatalf("unexpected refs: %+v", refs)
	}
	if !refs["B"].Timestamp.Equal(ts.Add(time.Hour)) {
		t.Errorf("B timestamp = %v, want %v", refs["B"].Timestamp, ts.Add(time.Hour))
	}

	if err := ms.DeleteMessages(chat, []string{"A"}); err != nil {
		t.Fatalf("DeleteMessages: %v", err)
	}
	if n := countMessages(t, ms, chat); n != 1 {
		t.Errorf("chat should keep only B, %d rows left", n)
	}
	if n := countMessages(t, ms, other); n != 1 {
		t.Errorf("same ID in another chat must be untouched, %d rows left", n)
	}
}
