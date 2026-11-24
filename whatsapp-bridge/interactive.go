package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const (
	interactiveTypeButtons     = "buttons"
	interactiveTypeList        = "list"
	interactiveTypeInteractive = "interactive"
	interactiveTypeTemplate    = "template"

	optionKindButton     = "button"
	optionKindListRow    = "list_row"
	optionKindQuickReply = "quick_reply"
	optionKindURL        = "url"
	optionKindCall       = "call"
	optionKindForm       = "form"
	optionKindNativeFlow = "native_flow"

	nativeFlowQuickReply = "quick_reply"
	nativeFlowURL        = "cta_url"
	nativeFlowCall       = "cta_call"
	nativeFlowForm       = "galaxy_message"
)

type interactiveOption struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Label  string `json:"label"`
	Target string `json:"target,omitempty"`
	Index  uint32 `json:"index,omitempty"`
}

type interactiveOptions struct {
	MessageType string              `json:"message_type"`
	Options     []interactiveOption `json:"options"`
	Original    []byte              `json:"original,omitempty"`
}

type nativeFlowParams struct {
	DisplayText string `json:"display_text"`
	ID          string `json:"id"`
	URL         string `json:"url"`
	PhoneNumber string `json:"phone_number"`
	FlowCTA     string `json:"flow_cta"`
	Title       string `json:"title"`
}

func nativeFlowOption(name, paramsJSON string) interactiveOption {
	var p nativeFlowParams
	_ = json.Unmarshal([]byte(paramsJSON), &p)
	label := p.DisplayText
	if label == "" {
		label = p.FlowCTA
	}
	if label == "" {
		label = p.Title
	}
	switch name {
	case nativeFlowQuickReply:
		return interactiveOption{Kind: optionKindQuickReply, ID: p.ID, Label: label}
	case nativeFlowURL:
		return interactiveOption{Kind: optionKindURL, Label: label, Target: p.URL}
	case nativeFlowCall:
		return interactiveOption{Kind: optionKindCall, Label: label, Target: p.PhoneNumber}
	case nativeFlowForm:
		return interactiveOption{Kind: optionKindForm, Label: label}
	}
	return interactiveOption{Kind: optionKindNativeFlow, Label: label, Target: name}
}

func hydratedTemplateOptions(buttons []*waProto.HydratedTemplateButton) []interactiveOption {
	var opts []interactiveOption
	for _, b := range buttons {
		switch {
		case b.GetQuickReplyButton() != nil:
			q := b.GetQuickReplyButton()
			opts = append(opts, interactiveOption{Kind: optionKindQuickReply, ID: q.GetID(), Label: q.GetDisplayText(), Index: b.GetIndex()})
		case b.GetUrlButton() != nil:
			u := b.GetUrlButton()
			opts = append(opts, interactiveOption{Kind: optionKindURL, Label: u.GetDisplayText(), Target: u.GetURL(), Index: b.GetIndex()})
		case b.GetCallButton() != nil:
			c := b.GetCallButton()
			opts = append(opts, interactiveOption{Kind: optionKindCall, Label: c.GetDisplayText(), Target: c.GetPhoneNumber(), Index: b.GetIndex()})
		}
	}
	return opts
}

func extractInteractiveOptions(msg *waProto.Message) *interactiveOptions {
	msg = unwrapMessageContent(msg)
	if msg == nil {
		return nil
	}
	var result interactiveOptions
	var original *waProto.Message
	switch {
	case msg.GetButtonsMessage() != nil:
		bm := msg.GetButtonsMessage()
		result.MessageType = interactiveTypeButtons
		original = &waProto.Message{ButtonsMessage: bm}
		for _, b := range bm.GetButtons() {
			label := b.GetButtonText().GetDisplayText()
			if b.GetType() == waProto.ButtonsMessage_Button_NATIVE_FLOW {
				nf := nativeFlowOption(b.GetNativeFlowInfo().GetName(), b.GetNativeFlowInfo().GetParamsJSON())
				if label != "" {
					nf.Label = label
				}
				if nf.Kind == optionKindQuickReply {
					nf.Kind = optionKindNativeFlow
					nf.Target = nativeFlowQuickReply
				}
				result.Options = append(result.Options, nf)
				continue
			}
			result.Options = append(result.Options, interactiveOption{Kind: optionKindButton, ID: b.GetButtonID(), Label: label})
		}
	case msg.GetListMessage() != nil:
		lm := msg.GetListMessage()
		result.MessageType = interactiveTypeList
		original = &waProto.Message{ListMessage: lm}
		for _, s := range lm.GetSections() {
			for _, r := range s.GetRows() {
				result.Options = append(result.Options, interactiveOption{Kind: optionKindListRow, ID: r.GetRowID(), Label: r.GetTitle()})
			}
		}
	case msg.GetInteractiveMessage() != nil:
		im := msg.GetInteractiveMessage()
		result.MessageType = interactiveTypeInteractive
		original = &waProto.Message{InteractiveMessage: im}
		for _, b := range im.GetNativeFlowMessage().GetButtons() {
			result.Options = append(result.Options, nativeFlowOption(b.GetName(), b.GetButtonParamsJSON()))
		}
	case msg.GetTemplateMessage() != nil:
		tm := msg.GetTemplateMessage()
		result.MessageType = interactiveTypeTemplate
		original = &waProto.Message{TemplateMessage: tm}
		h := tm.GetHydratedTemplate()
		if h == nil {
			h = tm.GetHydratedFourRowTemplate()
		}
		result.Options = hydratedTemplateOptions(h.GetHydratedButtons())
	}
	if len(result.Options) == 0 {
		return nil
	}
	if raw, err := proto.Marshal(original); err == nil {
		result.Original = raw
	}
	return &result
}

func formatOption(position int, o interactiveOption) string {
	s := fmt.Sprintf("%d) %s", position, o.Label)
	switch o.Kind {
	case optionKindButton, optionKindListRow, optionKindQuickReply:
		if o.ID != "" {
			s += fmt.Sprintf(" (id=%s)", o.ID)
		}
	case optionKindURL:
		s += fmt.Sprintf(" (link: %s)", o.Target)
	case optionKindCall:
		s += fmt.Sprintf(" (call: %s)", o.Target)
	case optionKindForm:
		s += " (form, cannot be answered)"
	default:
		s += fmt.Sprintf(" (%s, cannot be answered)", o.Target)
	}
	return s
}

func appendOptionsText(content string, opts *interactiveOptions) string {
	if opts == nil {
		return content
	}
	parts := make([]string, len(opts.Options))
	for i, o := range opts.Options {
		parts[i] = formatOption(i+1, o)
	}
	text := "[Options: " + strings.Join(parts, " | ") + "]"
	if content == "" {
		return text
	}
	return content + "\n" + text
}

func (store *MessageStore) StoreInteractiveOptions(id, chatJID string, opts *interactiveOptions) error {
	if opts == nil {
		return nil
	}
	data, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	_, err = store.db.Exec("UPDATE messages SET interactive_options = ? WHERE id = ? AND chat_jid = ?", string(data), id, chatJID)
	return err
}

const nativeFlowResponseVersion = 3

var (
	errOptionNotFound    = errors.New("option not found")
	errOptionUnsupported = errors.New("option cannot be answered")
)

func (o interactiveOption) answerable() bool {
	switch o.Kind {
	case optionKindButton, optionKindListRow, optionKindQuickReply:
		return true
	}
	return false
}

func findInteractiveOption(opts *interactiveOptions, choice string) (interactiveOption, error) {
	choice = strings.TrimSpace(choice)
	for _, o := range opts.Options {
		if o.ID != "" && strings.EqualFold(o.ID, choice) {
			return o, nil
		}
	}
	for _, o := range opts.Options {
		if strings.EqualFold(strings.TrimSpace(o.Label), choice) {
			return o, nil
		}
	}
	if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(opts.Options) {
		return opts.Options[n-1], nil
	}
	return interactiveOption{}, fmt.Errorf("%w: %q; available: %s", errOptionNotFound, choice, appendOptionsText("", opts))
}

func buildOptionReply(opts *interactiveOptions, opt interactiveOption, ctx *waProto.ContextInfo) (*waProto.Message, error) {
	switch {
	case opts.MessageType == interactiveTypeButtons && opt.Kind == optionKindButton:
		return &waProto.Message{ButtonsResponseMessage: &waProto.ButtonsResponseMessage{
			SelectedButtonID: proto.String(opt.ID),
			Response:         &waProto.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: opt.Label},
			Type:             waProto.ButtonsResponseMessage_DISPLAY_TEXT.Enum(),
			ContextInfo:      ctx,
		}}, nil
	case opts.MessageType == interactiveTypeList && opt.Kind == optionKindListRow:
		return &waProto.Message{ListResponseMessage: &waProto.ListResponseMessage{
			Title:             proto.String(opt.Label),
			ListType:          waProto.ListResponseMessage_SINGLE_SELECT.Enum(),
			SingleSelectReply: &waProto.ListResponseMessage_SingleSelectReply{SelectedRowID: proto.String(opt.ID)},
			ContextInfo:       ctx,
		}}, nil
	case opts.MessageType == interactiveTypeInteractive && opt.Kind == optionKindQuickReply:
		params, err := json.Marshal(map[string]string{"id": opt.ID})
		if err != nil {
			return nil, err
		}
		return &waProto.Message{InteractiveResponseMessage: &waProto.InteractiveResponseMessage{
			Body: &waProto.InteractiveResponseMessage_Body{
				Text:   proto.String(opt.Label),
				Format: waProto.InteractiveResponseMessage_Body_DEFAULT.Enum(),
			},
			InteractiveResponseMessage: &waProto.InteractiveResponseMessage_NativeFlowResponseMessage_{
				NativeFlowResponseMessage: &waProto.InteractiveResponseMessage_NativeFlowResponseMessage{
					Name:       proto.String(nativeFlowQuickReply),
					ParamsJSON: proto.String(string(params)),
					Version:    proto.Int32(nativeFlowResponseVersion),
				},
			},
			ContextInfo: ctx,
		}}, nil
	case opts.MessageType == interactiveTypeTemplate && opt.Kind == optionKindQuickReply:
		return &waProto.Message{TemplateButtonReplyMessage: &waProto.TemplateButtonReplyMessage{
			SelectedID:          proto.String(opt.ID),
			SelectedDisplayText: proto.String(opt.Label),
			SelectedIndex:       proto.Uint32(opt.Index),
			ContextInfo:         ctx,
		}}, nil
	}
	return nil, fmt.Errorf("%w: %s option %q in a %s message", errOptionUnsupported, opt.Kind, opt.Label, opts.MessageType)
}

func optionReplyContext(messageID, participant string, opts *interactiveOptions) *waProto.ContextInfo {
	ctx := &waProto.ContextInfo{
		StanzaID:    proto.String(messageID),
		Participant: proto.String(participant),
	}
	var original waProto.Message
	if len(opts.Original) > 0 && proto.Unmarshal(opts.Original, &original) == nil {
		ctx.QuotedMessage = &original
	}
	return ctx
}

func (store *MessageStore) GetInteractiveMessage(id, chatJID string) (string, bool, *interactiveOptions, error) {
	var sender string
	var isFromMe bool
	var raw sql.NullString
	err := store.db.QueryRow(
		"SELECT sender, is_from_me, interactive_options FROM messages WHERE id = ? AND chat_jid = ?", id, chatJID,
	).Scan(&sender, &isFromMe, &raw)
	if err != nil || !raw.Valid || raw.String == "" {
		return sender, isFromMe, nil, err
	}
	var opts interactiveOptions
	if err := json.Unmarshal([]byte(raw.String), &opts); err != nil {
		return sender, isFromMe, nil, err
	}
	return sender, isFromMe, &opts, nil
}

type ReplyOptionRequest struct {
	ChatJID   string `json:"chat_jid"`
	MessageID string `json:"message_id"`
	Option    string `json:"option"`
}

func replyWithOption(client *whatsmeow.Client, messageStore *MessageStore, req ReplyOptionRequest) (int, string) {
	parsed, err := types.ParseJID(req.ChatJID)
	if err != nil || parsed.User == "" {
		return http.StatusBadRequest, fmt.Sprintf("invalid chat_jid %q", req.ChatJID)
	}
	chat := resolveUserJID(client, parsed, types.EmptyJID)
	chatJID := chat.String()

	sender, isFromMe, opts, err := messageStore.GetInteractiveMessage(req.MessageID, chatJID)
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound, fmt.Sprintf("message %s not found in %s", req.MessageID, chatJID)
	}
	if err != nil {
		return http.StatusInternalServerError, fmt.Sprintf("failed to load message: %v", err)
	}
	if opts == nil {
		return http.StatusUnprocessableEntity, fmt.Sprintf("message %s has no interactive options", req.MessageID)
	}
	opt, err := findInteractiveOption(opts, req.Option)
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	if !opt.answerable() {
		return http.StatusUnprocessableEntity, fmt.Sprintf("%s option %q cannot be answered by the bridge", opt.Kind, opt.Label)
	}

	participant := chatJID
	switch {
	case isFromMe && client.Store.ID != nil:
		participant = client.Store.ID.ToNonAD().String()
	case chat.Server == types.GroupServer:
		participant = types.NewJID(sender, types.DefaultUserServer).String()
	}
	ctx := optionReplyContext(req.MessageID, participant, opts)
	settings, err := messageStore.GetChatEphemeralSettings(chatJID)
	if err == nil && settings.Expiration != 0 && settings.SettingTimestamp != 0 {
		ctx = mergeEphemeralContextInfo(ctx, settings)
	}
	reply, err := buildOptionReply(opts, opt, ctx)
	if err != nil {
		return http.StatusUnprocessableEntity, err.Error()
	}

	if !client.IsConnected() {
		return http.StatusServiceUnavailable, "Not connected to WhatsApp"
	}
	recipient, err := resolveRecipientJID(client, chatJID)
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	resp, err := client.SendMessage(context.Background(), recipient, reply)
	if err != nil {
		return http.StatusInternalServerError, fmt.Sprintf("Error sending reply: %v", err)
	}

	if client.Store.ID != nil {
		timestamp := resp.Timestamp
		if timestamp.IsZero() {
			timestamp = time.Now()
		}
		if chatErr := messageStore.StoreChat(chatJID, "", timestamp); chatErr != nil {
			fmt.Printf("Warning: failed to store outbound chat metadata: %v\n", chatErr)
		}
		if storeErr := messageStore.StoreMessage(
			resp.ID, chatJID, client.Store.ID.User, opt.Label, timestamp, true,
			"", "", "", nil, nil, nil, 0, req.MessageID,
		); storeErr != nil {
			fmt.Printf("Warning: failed to persist option reply: %v\n", storeErr)
		}
	}
	return http.StatusOK, fmt.Sprintf("Selected %q in reply to %s", opt.Label, req.MessageID)
}
