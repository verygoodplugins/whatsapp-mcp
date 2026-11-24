package main

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func sampleButtonsMessage() *waProto.ButtonsMessage {
	return &waProto.ButtonsMessage{
		ContentText: proto.String("Selecione o tipo de agendamento:"),
		Buttons: []*waProto.ButtonsMessage_Button{
			{ButtonID: proto.String("btn-consulta"), ButtonText: &waProto.ButtonsMessage_Button_ButtonText{DisplayText: proto.String("Consulta")}, Type: waProto.ButtonsMessage_Button_RESPONSE.Enum()},
			{ButtonID: proto.String("btn-exame"), ButtonText: &waProto.ButtonsMessage_Button_ButtonText{DisplayText: proto.String("Exame")}, Type: waProto.ButtonsMessage_Button_RESPONSE.Enum()},
		},
	}
}

func sampleListMessage() *waProto.ListMessage {
	return &waProto.ListMessage{
		Description: proto.String("Escolha a unidade"),
		Sections: []*waProto.ListMessage_Section{
			{Title: proto.String("Zona 1"), Rows: []*waProto.ListMessage_Row{{RowID: proto.String("row-centro"), Title: proto.String("Centro")}}},
			{Title: proto.String("Zona 2"), Rows: []*waProto.ListMessage_Row{{RowID: proto.String("row-norte"), Title: proto.String("Norte")}}},
		},
	}
}

func sampleInteractiveMessage() *waProto.InteractiveMessage {
	return &waProto.InteractiveMessage{
		Body: &waProto.InteractiveMessage_Body{Text: proto.String("Como podemos ajudar?")},
		InteractiveMessage: &waProto.InteractiveMessage_NativeFlowMessage_{NativeFlowMessage: &waProto.InteractiveMessage_NativeFlowMessage{
			Buttons: []*waProto.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
				{Name: proto.String("quick_reply"), ButtonParamsJSON: proto.String(`{"display_text":"Sim","id":"qr-sim"}`)},
				{Name: proto.String("cta_url"), ButtonParamsJSON: proto.String(`{"display_text":"Site","url":"https://example.com"}`)},
				{Name: proto.String("cta_call"), ButtonParamsJSON: proto.String(`{"display_text":"Ligar","phone_number":"+5511999990000"}`)},
				{Name: proto.String("galaxy_message"), ButtonParamsJSON: proto.String(`{"flow_cta":"Agendar","flow_id":"123"}`)},
			},
		}},
	}
}

func sampleTemplateMessage() *waProto.TemplateMessage {
	return &waProto.TemplateMessage{
		HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{
			HydratedContentText: proto.String("Confirma a consulta?"),
			HydratedButtons: []*waProto.HydratedTemplateButton{
				{Index: proto.Uint32(0), HydratedButton: &waProto.HydratedTemplateButton_QuickReplyButton{QuickReplyButton: &waProto.HydratedTemplateButton_HydratedQuickReplyButton{DisplayText: proto.String("Confirmar"), ID: proto.String("tpl-yes")}}},
				{Index: proto.Uint32(1), HydratedButton: &waProto.HydratedTemplateButton_UrlButton{UrlButton: &waProto.HydratedTemplateButton_HydratedURLButton{DisplayText: proto.String("Detalhes"), URL: proto.String("https://example.com/c")}}},
			},
		},
	}
}

func TestExtractInteractiveOptions(t *testing.T) {
	cases := []struct {
		name     string
		msg      *waProto.Message
		wantType string
		want     []interactiveOption
	}{
		{
			name:     "buttons",
			msg:      &waProto.Message{ButtonsMessage: sampleButtonsMessage()},
			wantType: interactiveTypeButtons,
			want: []interactiveOption{
				{Kind: optionKindButton, ID: "btn-consulta", Label: "Consulta"},
				{Kind: optionKindButton, ID: "btn-exame", Label: "Exame"},
			},
		},
		{
			name: "buttons with a native flow button",
			msg: &waProto.Message{ButtonsMessage: &waProto.ButtonsMessage{Buttons: []*waProto.ButtonsMessage_Button{{
				ButtonText:     &waProto.ButtonsMessage_Button_ButtonText{DisplayText: proto.String("Preencher")},
				Type:           waProto.ButtonsMessage_Button_NATIVE_FLOW.Enum(),
				NativeFlowInfo: &waProto.ButtonsMessage_Button_NativeFlowInfo{Name: proto.String("galaxy_message"), ParamsJSON: proto.String(`{"flow_cta":"Abrir"}`)},
			}}}},
			wantType: interactiveTypeButtons,
			want:     []interactiveOption{{Kind: optionKindForm, Label: "Preencher"}},
		},
		{
			name:     "list rows across sections",
			msg:      &waProto.Message{ListMessage: sampleListMessage()},
			wantType: interactiveTypeList,
			want: []interactiveOption{
				{Kind: optionKindListRow, ID: "row-centro", Label: "Centro"},
				{Kind: optionKindListRow, ID: "row-norte", Label: "Norte"},
			},
		},
		{
			name:     "interactive native flow",
			msg:      &waProto.Message{InteractiveMessage: sampleInteractiveMessage()},
			wantType: interactiveTypeInteractive,
			want: []interactiveOption{
				{Kind: optionKindQuickReply, ID: "qr-sim", Label: "Sim"},
				{Kind: optionKindURL, Label: "Site", Target: "https://example.com"},
				{Kind: optionKindCall, Label: "Ligar", Target: "+5511999990000"},
				{Kind: optionKindForm, Label: "Agendar"},
			},
		},
		{
			name:     "template hydrated buttons",
			msg:      &waProto.Message{TemplateMessage: sampleTemplateMessage()},
			wantType: interactiveTypeTemplate,
			want: []interactiveOption{
				{Kind: optionKindQuickReply, ID: "tpl-yes", Label: "Confirmar", Index: 0},
				{Kind: optionKindURL, Label: "Detalhes", Target: "https://example.com/c", Index: 1},
			},
		},
		{
			name:     "wrapped in an ephemeral envelope",
			msg:      &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{ListMessage: sampleListMessage()}}},
			wantType: interactiveTypeList,
			want: []interactiveOption{
				{Kind: optionKindListRow, ID: "row-centro", Label: "Centro"},
				{Kind: optionKindListRow, ID: "row-norte", Label: "Norte"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractInteractiveOptions(tc.msg)
			if got == nil {
				t.Fatal("expected options, got nil")
			}
			if got.MessageType != tc.wantType || !reflect.DeepEqual(got.Options, tc.want) {
				t.Fatalf("got type=%q options=%+v, want type=%q options=%+v", got.MessageType, got.Options, tc.wantType, tc.want)
			}
			var original waProto.Message
			if err := proto.Unmarshal(got.Original, &original); err != nil {
				t.Fatalf("original does not decode: %v", err)
			}
			if extractInteractiveOptions(&original).MessageType != tc.wantType {
				t.Fatalf("original does not round-trip to a %s message", tc.wantType)
			}
		})
	}
}

func TestExtractInteractiveOptions_NoOptions(t *testing.T) {
	for name, msg := range map[string]*waProto.Message{
		"plain text":           {Conversation: proto.String("oi")},
		"buttons without any":  {ButtonsMessage: &waProto.ButtonsMessage{ContentText: proto.String("body")}},
		"list without rows":    {ListMessage: &waProto.ListMessage{Description: proto.String("body")}},
		"interactive no flow":  {InteractiveMessage: &waProto.InteractiveMessage{Body: &waProto.InteractiveMessage_Body{Text: proto.String("body")}}},
		"template no buttons":  {TemplateMessage: &waProto.TemplateMessage{HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{HydratedContentText: proto.String("body")}}},
		"nil message wrappers": nil,
	} {
		if got := extractInteractiveOptions(msg); got != nil {
			t.Errorf("%s: expected nil, got %+v", name, got)
		}
	}
}

func storedInteractiveOptions(t *testing.T, ms *MessageStore, id string) (string, *interactiveOptions) {
	t.Helper()
	var content string
	var raw *string
	if err := ms.db.QueryRow("SELECT content, interactive_options FROM messages WHERE id = ?", id).Scan(&content, &raw); err != nil {
		t.Fatalf("read message %s: %v", id, err)
	}
	if raw == nil {
		return content, nil
	}
	var opts interactiveOptions
	if err := json.Unmarshal([]byte(*raw), &opts); err != nil {
		t.Fatalf("decode interactive_options: %v", err)
	}
	return content, &opts
}

func TestHandleMessage_StoresInteractiveOptions(t *testing.T) {
	t.Setenv("WEBHOOK_ENABLED", "false")
	ms := newTestMessageStore(t)
	msg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: phonePN, Sender: phonePN},
			ID:            "bot-buttons",
			Timestamp:     time.Unix(1710000000, 0),
		},
		Message: &waProto.Message{ButtonsMessage: sampleButtonsMessage()},
	}
	handleMessage(newTestClient(&mockLIDStore{}), ms, msg, testLogger())

	content, opts := storedInteractiveOptions(t, ms, "bot-buttons")
	if content != "Selecione o tipo de agendamento:\n[Options: 1) Consulta (id=btn-consulta) | 2) Exame (id=btn-exame)]" {
		t.Fatalf("stored content = %q", content)
	}
	if opts == nil || opts.MessageType != interactiveTypeButtons || len(opts.Options) != 2 || opts.Options[1].ID != "btn-exame" {
		t.Fatalf("stored options = %+v", opts)
	}
}

func TestHandleMessage_PlainTextLeavesInteractiveOptionsNull(t *testing.T) {
	t.Setenv("WEBHOOK_ENABLED", "false")
	ms := newTestMessageStore(t)
	msg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: phonePN, Sender: phonePN},
			ID:            "plain",
			Timestamp:     time.Unix(1710000000, 0),
		},
		Message: &waProto.Message{Conversation: proto.String("oi")},
	}
	handleMessage(newTestClient(&mockLIDStore{}), ms, msg, testLogger())

	if _, opts := storedInteractiveOptions(t, ms, "plain"); opts != nil {
		t.Fatalf("expected NULL interactive_options, got %+v", opts)
	}
}

func TestHandleHistorySync_StoresInteractiveOptions(t *testing.T) {
	ms := newTestMessageStore(t)
	row := &waProto.HistorySyncMsg{Message: &waProto.WebMessageInfo{
		Key:              &waCommon.MessageKey{ID: proto.String("hist-list"), FromMe: proto.Bool(false)},
		MessageTimestamp: proto.Uint64(1710000000),
		Message:          &waProto.Message{ListMessage: sampleListMessage()},
	}}
	handleHistorySync(newTestClientWithSelf(&mockLIDStore{}, selfPhone), ms, historySyncFor(phonePN.String(), row), testLogger())

	content, opts := storedInteractiveOptions(t, ms, "hist-list")
	if content != "Escolha a unidade\n[Options: 1) Centro (id=row-centro) | 2) Norte (id=row-norte)]" {
		t.Fatalf("stored content = %q", content)
	}
	if opts == nil || opts.MessageType != interactiveTypeList || len(opts.Options) != 2 {
		t.Fatalf("stored options = %+v", opts)
	}
}

func TestEnsureMessageStoreSchema_AddsInteractiveOptionsColumn(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE chats (jid TEXT PRIMARY KEY); CREATE TABLE messages (id TEXT, chat_jid TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureMessageStoreSchema(db); err != nil {
		t.Fatalf("ensureMessageStoreSchema: %v", err)
	}
	if _, err := db.Exec(`UPDATE messages SET interactive_options = '{}'`); err != nil {
		t.Fatalf("interactive_options column missing after migration: %v", err)
	}
}
