package main

import (
	"net"
	"testing"
)

func TestResolveBridgePort(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    int
		wantErr bool
	}{
		{name: "unset falls back to default", env: "", want: defaultBridgePort},
		{name: "valid override", env: "18080", want: 18080},
		{name: "non-numeric", env: "eighty-eighty", wantErr: true},
		{name: "above range", env: "70000", wantErr: true},
		{name: "zero is not a port", env: "0", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WHATSAPP_BRIDGE_PORT", tt.env)

			got, err := resolveBridgePort()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveBridgePort() = %d, want error for %q", got, tt.env)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBridgePort() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveBridgePort() = %d, want %d", got, tt.want)
			}
		})
	}
}

// A second bridge on the same store would share the WhatsApp device session and
// evict the running one from it, so the lock has to be refused, not waited on.
func TestAcquireBridgeLockRefusesSecondHolder(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireBridgeLock(dir)
	if err != nil {
		t.Fatalf("first acquireBridgeLock() error = %v", err)
	}
	defer func() { _ = first.Close() }()

	second, err := acquireBridgeLock(dir)
	if err == nil {
		_ = second.Close()
		t.Fatal("second acquireBridgeLock() succeeded; a duplicate bridge would evict the running one from the WhatsApp session")
	}
}

// The lock is held by the open file description, so the kernel drops it when the
// process dies. A bridge that crashed must not lock its own restart out.
func TestAcquireBridgeLockReleasedOnClose(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireBridgeLock(dir)
	if err != nil {
		t.Fatalf("first acquireBridgeLock() error = %v", err)
	}
	_ = first.Close()

	second, err := acquireBridgeLock(dir)
	if err != nil {
		t.Fatalf("acquireBridgeLock() after release error = %v; a crashed bridge would lock out its restart", err)
	}
	_ = second.Close()
}

func TestBindBridgePortRefusesPortInUse(t *testing.T) {
	first, err := bindBridgePort(0) // 0 → kernel picks a free port
	if err != nil {
		t.Fatalf("bindBridgePort(0) error = %v", err)
	}
	defer func() { _ = first.Close() }()

	port := first.Addr().(*net.TCPAddr).Port

	second, err := bindBridgePort(port)
	if err == nil {
		_ = second.Close()
		t.Fatalf("bindBridgePort(%d) succeeded while the port was already bound", port)
	}
}

// The REST API must stay on loopback: it is authenticated by a bearer token
// written to the store, not by anything that would survive LAN exposure.
func TestBindBridgePortBindsLoopbackOnly(t *testing.T) {
	ln, err := bindBridgePort(0)
	if err != nil {
		t.Fatalf("bindBridgePort(0) error = %v", err)
	}
	defer func() { _ = ln.Close() }()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T, want *net.TCPAddr", ln.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Errorf("bound to %s, want a loopback address", addr.IP)
	}
}
