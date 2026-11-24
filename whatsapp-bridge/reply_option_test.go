package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func TestFindInteractiveOption(t *testing.T) {
	opts := extractInteractiveOptions(&waProto.Message{ButtonsMessage: sampleButtonsMessage()})
	for choice, wantID := range map[string]string{
		"btn-exame": "btn-exame",
		"BTN-EXAME": "btn-exame",
		"consulta":  "btn-consulta",
		" Exame ":   "btn-exame",
		"2":         "btn-exame",
		"1":         "btn-consulta",
	} {
		got, err := findInteractiveOption(opts, choice)
		if err != nil || got.ID != wantID {
			t.Errorf("choice %q: got %+v err=%v, want id %s", choice, got, err, wantID)
		}
	}
	for _, choice := range []string{"Retorno", "0", "3"} {
		if _, err := findInteractiveOption(opts, choice); !errors.Is(err, errOptionNotFound) {
			t.Errorf("choice %q: expected errOptionNotFound, got %v", choice, err)
		}
	}
}

func TestBuildOptionReply(t *testing.T) {
	ctx := &waProto.ContextInfo{StanzaID: proto.String("orig"), Participant: proto.String("15551234567@s.whatsapp.net")}

	buttons := extractInteractiveOptions(&waProto.Message{ButtonsMessage: sampleButtonsMessage()})
	msg, err := buildOptionReply(buttons, buttons.Options[1], ctx)
	if err != nil {
		t.Fatal(err)
	}
	br := msg.GetButtonsResponseMessage()
	if br.GetSelectedButtonID() != "btn-exame" || br.GetSelectedDisplayText() != "Exame" ||
		br.GetType() != waProto.ButtonsResponseMessage_DISPLAY_TEXT || br.GetContextInfo().GetStanzaID() != "orig" {
		t.Errorf("buttons reply = %+v", br)
	}

	list := extractInteractiveOptions(&waProto.Message{ListMessage: sampleListMessage()})
	msg, err = buildOptionReply(list, list.Options[1], ctx)
	if err != nil {
		t.Fatal(err)
	}
	lr := msg.GetListResponseMessage()
	if lr.GetSingleSelectReply().GetSelectedRowID() != "row-norte" || lr.GetTitle() != "Norte" ||
		lr.GetListType() != waProto.ListResponseMessage_SINGLE_SELECT || lr.GetContextInfo().GetStanzaID() != "orig" {
		t.Errorf("list reply = %+v", lr)
	}

	interactive := extractInteractiveOptions(&waProto.Message{InteractiveMessage: sampleInteractiveMessage()})
	msg, err = buildOptionReply(interactive, interactive.Options[0], ctx)
	if err != nil {
		t.Fatal(err)
	}
	ir := msg.GetInteractiveResponseMessage()
	nf := ir.GetNativeFlowResponseMessage()
	if nf.GetName() != "quick_reply" || nf.GetParamsJSON() != `{"id":"qr-sim"}` || nf.GetVersion() != 3 ||
		ir.GetBody().GetText() != "Sim" || ir.GetContextInfo().GetStanzaID() != "orig" {
		t.Errorf("interactive reply = %+v", ir)
	}

	template := extractInteractiveOptions(&waProto.Message{TemplateMessage: sampleTemplateMessage()})
	msg, err = buildOptionReply(template, template.Options[0], ctx)
	if err != nil {
		t.Fatal(err)
	}
	tr := msg.GetTemplateButtonReplyMessage()
	if tr.GetSelectedID() != "tpl-yes" || tr.GetSelectedDisplayText() != "Confirmar" || tr.GetSelectedIndex() != 0 ||
		tr.GetContextInfo().GetStanzaID() != "orig" {
		t.Errorf("template reply = %+v", tr)
	}

	for _, opt := range interactive.Options[1:] {
		if _, err := buildOptionReply(interactive, opt, ctx); !errors.Is(err, errOptionUnsupported) {
			t.Errorf("%s option: expected errOptionUnsupported, got %v", opt.Kind, err)
		}
	}
}

func TestOptionReplyContextQuotesOriginal(t *testing.T) {
	opts := extractInteractiveOptions(&waProto.Message{ListMessage: sampleListMessage()})
	ctx := optionReplyContext("orig", "15551234567@s.whatsapp.net", opts)
	if ctx.GetStanzaID() != "orig" || ctx.GetParticipant() != "15551234567@s.whatsapp.net" {
		t.Fatalf("context = %+v", ctx)
	}
	if ctx.GetQuotedMessage().GetListMessage().GetDescription() != "Escolha a unidade" {
		t.Fatalf("quoted message = %+v", ctx.GetQuotedMessage())
	}
}

func seedInteractiveMessage(t *testing.T, ms *MessageStore, id string, msg *waProto.Message) {
	t.Helper()
	if err := ms.StoreMessage(id, phonePN.String(), phonePN.User, extractTextContent(msg), time.Unix(1710000000, 0), false,
		"", "", "", nil, nil, nil, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := ms.StoreInteractiveOptions(id, phonePN.String(), extractInteractiveOptions(msg)); err != nil {
		t.Fatal(err)
	}
}

func TestReplyOptionHandler_Errors(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	ms := newTestMessageStore(t)
	seedInteractiveMessage(t, ms, "buttons", &waProto.Message{ButtonsMessage: sampleButtonsMessage()})
	seedInteractiveMessage(t, ms, "flow", &waProto.Message{InteractiveMessage: sampleInteractiveMessage()})
	seedInteractiveMessage(t, ms, "plain", &waProto.Message{Conversation: proto.String("oi")})
	handler := newRESTMux(newTestClient(&mockLIDStore{}), ms, 8080, token, nil)
	chat := phonePN.String()

	cases := []struct {
		name, body, wantMessage string
		wantStatus              int
	}{
		{"missing option", `{"chat_jid":"` + chat + `","message_id":"buttons"}`, "", http.StatusBadRequest},
		{"invalid chat", `{"chat_jid":"@s.whatsapp.net","message_id":"buttons","option":"1"}`, "invalid chat_jid", http.StatusBadRequest},
		{"unknown message", `{"chat_jid":"` + chat + `","message_id":"nope","option":"1"}`, "not found", http.StatusNotFound},
		{"no options", `{"chat_jid":"` + chat + `","message_id":"plain","option":"1"}`, "no interactive options", http.StatusUnprocessableEntity},
		{"option not found", `{"chat_jid":"` + chat + `","message_id":"buttons","option":"Retorno"}`, "Consulta (id=btn-consulta)", http.StatusBadRequest},
		{"form option", `{"chat_jid":"` + chat + `","message_id":"flow","option":"Agendar"}`, "cannot be answered", http.StatusUnprocessableEntity},
		{"link option", `{"chat_jid":"` + chat + `","message_id":"flow","option":"Site"}`, "cannot be answered", http.StatusUnprocessableEntity},
		{"valid option while disconnected", `{"chat_jid":"` + chat + `","message_id":"buttons","option":"Exame"}`, "Not connected", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/reply-option", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+token)
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", resp.Code, tc.wantStatus, resp.Body.String())
			}
			if tc.wantMessage == "" {
				return
			}
			var body SendMessageResponse
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body %q: %v", resp.Body.String(), err)
			}
			if body.Success || !strings.Contains(body.Message, tc.wantMessage) {
				t.Fatalf("body = %+v, want message containing %q", body, tc.wantMessage)
			}
		})
	}
}

func TestReplyOptionHandler_RequiresAuth(t *testing.T) {
	handler := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, "supersecrettoken1234567890abcdef", nil)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/reply-option",
		strings.NewReader(`{"chat_jid":"15551234567@s.whatsapp.net","message_id":"x","option":"1"}`))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.Code)
	}
}
