package main

import (
	"encoding/json"
	"fmt"
	"strings"

	waProto "go.mau.fi/whatsmeow/binary/proto"
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
