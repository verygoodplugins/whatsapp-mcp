package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
)

func waitConnection(t *testing.T, c *connectionManager, state string) connectionStatus {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s := c.snapshot()
		if s.State == state {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("wanted %s, got %+v", state, c.snapshot())
	return connectionStatus{}
}
func pairingFixture(t *testing.T) (*connectionManager, chan whatsmeow.QRChannelItem, *atomic.Int32) {
	t.Helper()
	c := newConnectionManager(false)
	codes := make(chan whatsmeow.QRChannelItem, 4)
	attempts := &atomic.Int32{}
	c.disconnect = func() {}
	c.preparePairing = func() {}
	c.connect = func(context.Context) error { attempts.Add(1); return nil }
	c.qrChannel = func(context.Context) (<-chan whatsmeow.QRChannelItem, error) { return codes, nil }
	t.Cleanup(c.close)
	return c, codes, attempts
}
func TestConnectionPairingRefreshSingleFlightAndExpiry(t *testing.T) {
	c, codes, attempts := pairingFixture(t)
	c.reconnect()
	codes <- whatsmeow.QRChannelItem{Event: "code", Code: "first-secret", Timeout: time.Minute}
	first := waitConnection(t, c, "pairing")
	if first.QRCode == nil || *first.QRCode != "first-secret" || first.QRExpiresAt == nil {
		t.Fatal("missing QR metadata")
	}
	for range 10 {
		c.reconnect()
	}
	if attempts.Load() != 1 {
		t.Fatal("concurrent reconnect started another attempt")
	}
	codes <- whatsmeow.QRChannelItem{Event: "code", Code: "replacement-secret", Timeout: -time.Second}
	deadline := time.Now().Add(time.Second)
	for !c.snapshot().UpdatedAt.After(first.UpdatedAt) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.snapshot().QRCode != nil {
		t.Fatal("expired QR leaked to caller")
	}
	codes <- whatsmeow.QRChannelTimeout
	s := waitConnection(t, c, "logged_out")
	if s.QRCode != nil || s.LastError.Code != "qr_timeout" {
		t.Fatal("timeout did not clear QR")
	}
}
func TestConnectionPreservesActionableErrorsAfterSocketClose(t *testing.T) {
	for _, test := range []struct {
		event interface{}
		state string
	}{
		{&events.ClientOutdated{}, "outdated"}, {&events.LoggedOut{}, "logged_out"},
		{&events.ConnectFailure{Reason: events.ConnectFailureClientOutdated}, "outdated"},
	} {
		c := newConnectionManager(true)
		c.handleEvent(test.event)
		c.handleEvent(&events.Disconnected{})
		if c.snapshot().State != test.state {
			t.Fatalf("got %s", c.snapshot().State)
		}
		c.cancel()
	}
}
func TestConnectionSuccessClearsQRCode(t *testing.T) {
	c, codes, _ := pairingFixture(t)
	c.reconnect()
	codes <- whatsmeow.QRChannelItem{Event: "code", Code: "secret", Timeout: time.Minute}
	waitConnection(t, c, "pairing")
	c.handleEvent(&events.Connected{})
	codes <- whatsmeow.QRChannelSuccess
	time.Sleep(10 * time.Millisecond)
	s := c.snapshot()
	if s.State != "connected" || s.QRCode != nil || !s.Paired {
		t.Fatalf("got %+v", s)
	}
}
func TestConnectionEndpointsRequireAuthAndStayAvailableBeforePairing(t *testing.T) {
	c, codes, _ := pairingFixture(t)
	mux := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, "test-token", nil, c)
	for _, test := range []struct {
		method, path, token string
		code                int
	}{
		{"GET", "/api/connection", "", 401}, {"GET", "/api/connection", "test-token", 200},
		{"GET", "/api/connection/reconnect", "test-token", 405}, {"POST", "/api/connection/reconnect", "test-token", 200},
	} {
		req := httptest.NewRequest(test.method, "http://127.0.0.1:8080"+test.path, nil)
		if test.token != "" {
			req.Header.Set("Authorization", "Bearer "+test.token)
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, req)
		if recorder.Code != test.code {
			t.Fatalf("%s %s got %d: %s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
		if recorder.Code == http.StatusOK && recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("pairing response may be cached")
		}
		if strings.Contains(recorder.Body.String(), "test-token") {
			t.Fatal("token leaked")
		}
	}
	codes <- whatsmeow.QRChannelTimeout
}

func TestRevokedClientReinitializesDeviceInternals(t *testing.T) {
	c := newConnectionManager(false)
	defer c.cancel()
	revoked := &store.Device{Deleted: true}
	revoked.SetAllStores(&store.NoopStore{Error: store.ErrDeviceDeleted})
	previous := whatsmeow.NewClient(revoked, nil)
	c.client.Store(previous)
	fresh := &store.Device{}
	var registered *whatsmeow.Client
	c.replaceDeletedClient(func() *store.Device { return fresh }, func(next *whatsmeow.Client) { registered = next })
	current := c.client.Load()
	if current == previous || current.Store != fresh || registered != current || current.EnableAutoReconnect {
		t.Fatal("revoked client was not replaced and configured")
	}
	if previous.Store != revoked {
		t.Fatal("old client's device-bound references were changed")
	}
	// API requests and future QR requests must follow the new client, too.
	mux := newManagedRESTMux(previous, newTestMessageStore(t), 8080, "test-token", nil, c)
	req := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/connection", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("replacement client API returned %d", response.Code)
	}
	// Poison only the obsolete public pointer: a stale REST closure would panic.
	previous.Store = nil
	req = httptest.NewRequest("POST", "http://127.0.0.1:8080/api/react", strings.NewReader(`{"recipient":"123@s.whatsapp.net","message_id":"test","emoji":"","from_me":true}`))
	req.Header.Set("Authorization", "Bearer test-token")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != 503 {
		t.Fatal("REST handler did not use new unpaired client")
	}
}
func TestKeepaliveFailureRestoresReconnectReadiness(t *testing.T) {
	c := newConnectionManager(true)
	defer c.cancel()
	c.handleEvent(&events.Connected{})
	c.handleEvent(&events.KeepAliveTimeout{LastSuccess: time.Now()})
	if !c.snapshot().Connected {
		t.Fatal("single failed ping disconnected healthy client")
	}
	c.handleEvent(&events.KeepAliveTimeout{LastSuccess: time.Now().Add(-whatsmeow.KeepAliveMaxFailTime - time.Second)})
	if c.snapshot().Connected || c.snapshot().LastError.Code != "keepalive_timeout" {
		t.Fatal("stalled socket still marked healthy")
	}
	c.handleEvent(&events.KeepAliveRestored{})
	if !c.snapshot().Connected {
		t.Fatal("keepalive recovery did not restore readiness")
	}
}
func TestPairSuccessStillTimesOutAndCanRetryAuthentication(t *testing.T) {
	c, codes, attempts := pairingFixture(t)
	c.authTimeout = 10 * time.Millisecond
	c.connect = func(context.Context) error {
		if attempts.Add(1) > 1 {
			c.handleEvent(&events.Connected{})
		}
		return nil
	}
	c.reconnect()
	codes <- whatsmeow.QRChannelSuccess
	state := waitConnection(t, c, "error")
	if state.LastError.Code != "connect_timeout" || !state.Paired {
		t.Fatal("post-pair authentication stalled")
	}
	c.wg.Wait()
	c.reconnect()
	waitConnection(t, c, "connected")
}
