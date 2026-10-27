package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
)

type connectionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type connectionStatus struct {
	State       string           `json:"state"`
	Connected   bool             `json:"connected"`
	Paired      bool             `json:"paired"`
	Message     string           `json:"message"`
	QRCode      *string          `json:"qrCode"`
	QRExpiresAt *time.Time       `json:"qrExpiresAt"`
	UpdatedAt   time.Time        `json:"updatedAt"`
	LastError   *connectionError `json:"lastError"`
}

// connectionManager serializes explicit connection attempts. REST stays alive
// while the phone is unpaired; credentials and message history are never removed.
type connectionManager struct {
	client         atomic.Pointer[whatsmeow.Client]
	authTimeout    time.Duration
	mu             sync.Mutex
	status         connectionStatus
	active         bool
	stopped        bool
	nextRetry      time.Time
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	connect        func(context.Context) error
	disconnect     func()
	qrChannel      func(context.Context) (<-chan whatsmeow.QRChannelItem, error)
	preparePairing func()
	renderQR       func(string)
}

func newConnectionManager(paired bool) *connectionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &connectionManager{ctx: ctx, cancel: cancel, authTimeout: 45 * time.Second, status: connectionStatus{
		State: "disconnected", Paired: paired, Message: "WhatsApp is disconnected.", UpdatedAt: time.Now().UTC(),
	}}
}

// Recreate all device-bound internals, including the app-state processor. Changing
// only Client.Store would leave those internals attached to the revoked device.
func (c *connectionManager) replaceDeletedClient(newDevice func() *store.Device, register func(*whatsmeow.Client)) {
	old := c.client.Load()
	if old.Store.Deleted {
		next := whatsmeow.NewClient(newDevice(), old.Log)
		next.EnableAutoReconnect = false
		register(next)
		c.client.Store(next)
	}
}

func (c *connectionManager) snapshot() connectionStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.status
	// Never return an expired pairing secret, even between refresh events.
	if s.QRExpiresAt != nil && !time.Now().Before(*s.QRExpiresAt) {
		s.QRCode = nil
		s.QRExpiresAt = nil
	}
	return s
}
func (c *connectionManager) setLocked(state, message, code string) {
	c.status.State = state
	c.status.Connected = state == "connected"
	c.status.Message = message
	c.status.QRCode = nil
	c.status.QRExpiresAt = nil
	c.status.UpdatedAt = time.Now().UTC()
	c.status.LastError = nil
	if code != "" {
		c.status.LastError = &connectionError{Code: code, Message: message}
	}
	c.nextRetry = time.Now().Add(5 * time.Second)
}
func (c *connectionManager) handleEvent(evt interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch v := evt.(type) {
	case *events.Connected:
		c.status.Paired = true
		c.setLocked("connected", "Connected to WhatsApp.", "")
	case *events.KeepAliveTimeout:
		if c.status.Connected && time.Since(v.LastSuccess) > whatsmeow.KeepAliveMaxFailTime {
			c.setLocked("error", "WhatsApp stopped responding. Reconnecting shortly.", "keepalive_timeout")
		}
	case *events.KeepAliveRestored:
		if c.status.LastError != nil && c.status.LastError.Code == "keepalive_timeout" {
			c.setLocked("connected", "Connected to WhatsApp.", "")
		}
	case *events.LoggedOut:
		c.status.Paired = false
		c.setLocked("logged_out", "Link WhatsApp from your phone to receive messages again.", "logged_out")
	case *events.ClientOutdated:
		c.setLocked("outdated", "The WhatsApp client needs an update. Re-linking will not fix this.", "405")
	case *events.ConnectFailure:
		if v.Reason == events.ConnectFailureClientOutdated {
			c.setLocked("outdated", "The WhatsApp client needs an update.", "405")
		} else {
			c.setLocked("error", fmt.Sprintf("WhatsApp connection failed (%s).", v.Reason), fmt.Sprint(int(v.Reason)))
		}
	case *events.StreamReplaced:
		c.setLocked("disconnected", "Another session replaced this connection. Retrying shortly.", "stream_replaced")
		c.nextRetry = time.Now().Add(30 * time.Second)
	case *events.StreamError:
		c.setLocked("error", "WhatsApp reported a connection error.", v.Code)
	case *events.Disconnected:
		// A socket close following rejection must not erase the actionable cause.
		if c.status.State != "outdated" && c.status.State != "logged_out" && c.status.State != "pairing" && c.status.State != "error" {
			c.setLocked("disconnected", "Disconnected from WhatsApp. Retrying shortly.", "disconnected")
		}
	}
}
func (c *connectionManager) reconnect() {
	c.mu.Lock()
	if c.active || c.stopped || c.status.Connected {
		c.mu.Unlock()
		return
	}
	c.active = true
	paired := c.status.Paired
	c.setLocked("connecting", "Connecting to WhatsApp…", "")
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		defer func() { c.mu.Lock(); c.active = false; c.mu.Unlock() }()
		c.attempt(paired)
	}()
}
func (c *connectionManager) fail(message, code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.State == "connecting" || c.status.State == "pairing" {
		state := "error"
		if !c.status.Paired {
			state = "logged_out"
		}
		c.setLocked(state, message, code)
	}
}
func (c *connectionManager) attempt(paired bool) {
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Minute)
	defer cancel()
	c.disconnect()
	var codes <-chan whatsmeow.QRChannelItem
	if !paired {
		c.preparePairing()
		var err error
		codes, err = c.qrChannel(ctx)
		if err != nil {
			c.fail("Could not start phone linking. Try reconnecting again.", "pairing_failed")
			return
		}
	}
	if err := c.connect(c.ctx); err != nil {
		c.fail("Could not connect to WhatsApp. Check the bridge logs and retry.", "connect_failed")
		return
	}
	if paired {
		c.waitForAuthentication()
		return
	}

	for {
		select {
		case <-ctx.Done():
			c.fail("Phone linking timed out. Request a new QR code.", "qr_timeout")
			c.disconnect()
			return
		case item, ok := <-codes:
			if !ok {
				c.fail("Phone linking ended. Request a new QR code.", "qr_closed")
				return
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				expires := time.Now().Add(item.Timeout).UTC()
				c.mu.Lock()
				c.setLocked("pairing", "Scan this QR code from WhatsApp → Linked Devices → Link a Device.", "")
				c.status.QRCode = &item.Code
				c.status.QRExpiresAt = &expires
				c.mu.Unlock()
				if c.renderQR != nil {
					c.renderQR(item.Code)
				}
			case "success":
				c.mu.Lock()
				c.status.Paired = true
				if !c.status.Connected {
					c.setLocked("connecting", "Phone linked. Finishing the connection…", "")
				}
				c.mu.Unlock()
				c.waitForAuthentication()
				return
			case "err-client-outdated":
				c.handleEvent(&events.ClientOutdated{})
				return
			case "timeout":
				c.fail("QR code expired. Request a new QR code.", "qr_timeout")
				return
			default:
				c.fail("Phone linking failed. Request a new QR code.", "pairing_failed")
				return
			}
		}
	}
}

// The library may only log a failed post-pair 515 reconnect. Keep the same
// bounded authentication watchdog for both saved sessions and newly paired ones.
func (c *connectionManager) waitForAuthentication() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(c.authTimeout)
	defer timeout.Stop()
	for c.snapshot().State == "connecting" {
		select {
		case <-c.ctx.Done():
			return
		case <-timeout.C:
			c.fail("WhatsApp did not finish connecting. Retrying shortly.", "connect_timeout")
			c.disconnect()
			return
		case <-ticker.C:
		}
	}
}

func (c *connectionManager) start() {
	c.reconnect()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				c.mu.Lock()
				retry := c.status.Paired && (c.status.State == "disconnected" || c.status.State == "error") && !time.Now().Before(c.nextRetry)
				c.mu.Unlock()
				if retry {
					c.reconnect()
				}
			}
		}
	}()
}
func (c *connectionManager) close() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	c.cancel()
	c.disconnect()
	c.wg.Wait()
}
func (c *connectionManager) register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	serve := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(c.snapshot())
	}
	mux.HandleFunc("/api/connection", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		serve(w, r)
	}))
	mux.HandleFunc("/api/connection/reconnect", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c.reconnect()
		serve(w, r)
	}))
}
