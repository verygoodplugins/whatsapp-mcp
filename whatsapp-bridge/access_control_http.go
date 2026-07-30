package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

var (
	ErrMCPAccessDenied  = errors.New("MCP access denied for chat")
	ErrInvalidMCPChatID = errors.New("invalid MCP chat JID")
)

func parseMCPAccessChatJID(client *whatsmeow.Client, recipient string) (types.JID, string, error) {
	var (
		chatJID types.JID
		err     error
	)
	if strings.Contains(recipient, "@") {
		chatJID, err = types.ParseJID(recipient)
		if err != nil {
			return types.EmptyJID, "", fmt.Errorf("%w: %v", ErrInvalidMCPChatID, err)
		}
	} else {
		chatJID = types.JID{User: recipient, Server: types.DefaultUserServer}
	}
	chatJID = chatJID.ToNonAD()
	canonical := resolveUserJID(client, chatJID, types.EmptyJID)
	return chatJID, canonical.String(), nil
}

func requireMCPSendAccess(
	client *whatsmeow.Client,
	messageStore *MessageStore,
	recipient string,
	allowNewConversation bool,
) (types.JID, string, bool, error) {
	chatJID, canonicalJID, err := parseMCPAccessChatJID(client, recipient)
	if err != nil {
		return types.EmptyJID, "", false, err
	}
	allowed, err := messageStore.CanMCPSend(canonicalJID)
	if err != nil {
		return types.EmptyJID, canonicalJID, false, err
	}
	if allowed {
		return chatJID, canonicalJID, false, nil
	}
	if allowNewConversation && isMCPNewDirectRecipient(chatJID) {
		exists, err := messageStore.MCPChatExists(canonicalJID)
		if err != nil {
			return types.EmptyJID, canonicalJID, false, err
		}
		if !exists {
			allowed, err := messageStore.CanMCPStartNewConversations()
			if err != nil {
				return types.EmptyJID, canonicalJID, false, err
			}
			if allowed {
				return chatJID, canonicalJID, true, nil
			}
		}
	}
	return types.EmptyJID, canonicalJID, false, fmt.Errorf("%w: %s", ErrMCPAccessDenied, canonicalJID)
}

func isMCPNewDirectRecipient(chatJID types.JID) bool {
	if chatJID.Server != types.DefaultUserServer {
		return false
	}
	user := chatJID.User
	if len(user) < 7 || len(user) > 15 {
		return false
	}
	for _, character := range user {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func writeMCPAccessDenied(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]interface{}{
		"success": false,
		"message": "MCP access denied; review this conversation in the local bridge admin panel",
	})
}
