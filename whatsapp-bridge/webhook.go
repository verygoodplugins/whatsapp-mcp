package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// WebhookPayload represents the data sent to the webhook
type WebhookPayload struct {
	Sender   string `json:"sender"`
	Content  string `json:"content"`
	ChatJID  string `json:"chatJID"`
	IsFromMe bool   `json:"isFromMe"`
}

// SendWebhook sends a message to the webhook endpoint
func SendWebhook(sender, content, chatJID string, isFromMe bool) {
	webhookURL := os.Getenv("WEBHOOK_URL")
	if webhookURL == "" {
		webhookURL = "http://localhost:8769/whatsapp/webhook"
	}

	payload := WebhookPayload{
		Sender:   sender,
		Content:  content,
		ChatJID:  chatJID,
		IsFromMe: isFromMe,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("Error marshaling webhook payload: %v\n", err)
		return
	}

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("Error sending webhook: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		fmt.Printf("✓ Webhook sent for message from %s\n", sender)
	} else {
		fmt.Printf("⚠ Webhook failed with status %d\n", resp.StatusCode)
	}
}

// In main.go, handleMessage forwards webhooks for messages with text content.
// It will forward self-sent messages when the env var FORWARD_SELF=true.
