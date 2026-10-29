package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOwnMessageEndpoints_RefuseBeforeSending(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	const chat = "15551234567@s.whatsapp.net"
	store := newTestMessageStore(t)
	now := time.Now()
	rows := []struct {
		id      string
		at      time.Time
		fromMe  bool
		media   string
		deleted bool
	}{
		{"fresh", now.Add(-time.Minute), true, "", false},
		{"photo", now.Add(-time.Minute), true, "image", false},
		{"old", now.Add(-time.Hour), true, "", false},
		{"ancient", now.Add(-72 * time.Hour), true, "", false},
		{"theirs", now.Add(-time.Minute), false, "", false},
		{"gone", now.Add(-time.Minute), true, "", true},
	}
	for _, m := range rows {
		if err := store.StoreMessage(m.id, chat, "15551234567", "hi", m.at, m.fromMe, m.media, "", "", nil, nil, nil, 0, ""); err != nil {
			t.Fatal(err)
		}
		if m.deleted {
			if err := store.MarkMessageDeleted(m.id, chat, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	handler := newRESTMux(newTestClient(&mockLIDStore{}), store, 8080, token, nil)

	cases := []struct {
		path, body string
		want       int
	}{
		{"/api/edit", `{"chat_jid":"` + chat + `","message_id":"fresh"}`, http.StatusBadRequest},
		{"/api/edit", `{"chat_jid":"` + chat + `","message_id":"photo","text":"x"}`, http.StatusBadRequest},
		{"/api/edit", `{"chat_jid":"` + chat + `","message_id":"old","text":"x"}`, http.StatusConflict},
		{"/api/edit", `{"chat_jid":"` + chat + `","message_id":"theirs","text":"x"}`, http.StatusNotFound},
		{"/api/revoke", `{"chat_jid":"` + chat + `","message_id":"ancient"}`, http.StatusConflict},
		{"/api/revoke", `{"chat_jid":"` + chat + `","message_id":"gone"}`, http.StatusNotFound},
		{"/api/revoke", `{"chat_jid":"` + chat + `","message_id":"missing"}`, http.StatusNotFound},
		{"/api/revoke", `{"message_id":"fresh"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != tc.want {
			t.Errorf("%s %s: want %d, got %d (%s)", tc.path, tc.body, tc.want, resp.Code, strings.TrimSpace(resp.Body.String()))
		}
	}
}

func TestOwnMessage_FoundOnlyForOurLiveMessages(t *testing.T) {
	const chat = "15551234567@s.whatsapp.net"
	store := newTestMessageStore(t)
	at := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	if err := store.StoreMessage("m1", chat, "15551234567", "hi", at, true, "", "", "", nil, nil, nil, 0, ""); err != nil {
		t.Fatal(err)
	}
	sent, media, found, err := store.OwnMessage("m1", chat)
	if err != nil || !found || media != "" || !sent.Equal(at) {
		t.Fatalf("got sent=%v media=%q found=%v err=%v", sent, media, found, err)
	}
	if _, _, found, _ := store.OwnMessage("m1", "other@s.whatsapp.net"); found {
		t.Fatal("found in another chat")
	}
}
