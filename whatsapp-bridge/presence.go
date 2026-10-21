package main

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// A companion that never sends "available" presence looks unused to WhatsApp:
// its receipts go out as type="inactive" and the phone eventually shows
// "Logging out in N days. Open WhatsApp on this device to stay logged in."
// pulsePresence marks the bridge online for a moment, the way opening
// WhatsApp Web would, then offline again so the phone keeps its push
// notifications (WhatsApp mutes them while a companion is available).
const (
	presenceOnlineFor = 30 * time.Second
	presenceEvery     = 6 * time.Hour
)

func pulsePresence(client *whatsmeow.Client, logger waLog.Logger) {
	if !client.IsConnected() || !client.IsLoggedIn() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), presenceOnlineFor+30*time.Second)
	defer cancel()
	if err := client.SendPresence(ctx, types.PresenceAvailable); err != nil {
		logger.Warnf("Presence heartbeat: available failed: %v", err)
		return
	}
	time.Sleep(presenceOnlineFor)
	if err := client.SendPresence(ctx, types.PresenceUnavailable); err != nil {
		logger.Warnf("Presence heartbeat: unavailable failed: %v", err)
		return
	}
	logger.Infof("Presence heartbeat sent")
}

// startPresenceHeartbeat pulses on every signal from connectedChan and on a
// timer, one pulse at a time.
func startPresenceHeartbeat(client *whatsmeow.Client, connectedChan <-chan struct{}, logger waLog.Logger) {
	go func() {
		ticker := time.NewTicker(presenceEvery)
		defer ticker.Stop()
		for {
			select {
			case <-connectedChan:
			case <-ticker.C:
			}
			pulsePresence(client, logger)
		}
	}()
}
