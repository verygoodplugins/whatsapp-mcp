package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const defaultBridgePort = 8080

// resolveBridgePort reads WHATSAPP_BRIDGE_PORT, falling back to
// defaultBridgePort. Hosts that also run local dev servers should set it —
// 8080 is a busy port to squat on.
func resolveBridgePort() (int, error) {
	raw := os.Getenv("WHATSAPP_BRIDGE_PORT")
	if raw == "" {
		return defaultBridgePort, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > 65535 {
		return 0, fmt.Errorf("invalid WHATSAPP_BRIDGE_PORT=%q, must be 1-65535", raw)
	}
	return v, nil
}

// acquireBridgeLock takes an exclusive lock on the bridge's store directory.
//
// Two bridges sharing a store share one WhatsApp device session, and WhatsApp
// permits a single live connection per device: the newcomer's login kicks the
// incumbent with <stream:error><conflict type="replaced"/>. The incumbent then
// reconnects, kicking the newcomer, and the two flap indefinitely — during
// which neither reliably persists incoming messages, while /api/health on the
// surviving socket still reports connected:true. The failure is therefore
// silent, which is what makes it worth refusing outright.
//
// The lock is released by the kernel when the process exits, so a crashed
// bridge does not leave a stale lock behind.
func acquireBridgeLock(storeDir string) (*os.File, error) {
	path := filepath.Join(storeDir, ".bridge.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open bridge lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := "unknown"
		if b, rerr := os.ReadFile(path); rerr == nil && len(b) > 0 {
			holder = string(b)
		}
		_ = f.Close()
		return nil, fmt.Errorf(
			"another whatsapp-bridge already holds %s (pid %s) — refusing to start, "+
				"because a second bridge would kick the running one off the WhatsApp session",
			path, holder)
	}
	// Leave our pid behind so a refused start can name the incumbent.
	if err := f.Truncate(0); err == nil {
		if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
			// Non-fatal: the lock is held, the pid hint is a convenience.
			fmt.Printf("warning: could not record pid in %s: %v\n", path, err)
		}
	}
	return f, nil
}

// bindBridgePort claims the REST port up front.
//
// Binding must happen before the WhatsApp client connects. The bridge used to
// bind after connecting and merely log a failure, so a second instance would
// take over the session, fail to bind, and keep running anyway — inflicting the
// conflict loop above while serving nothing.
func bindBridgePort(port int) (net.Listener, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("cannot bind REST API to %s (port already in use?): %w", addr, err)
	}
	return ln, nil
}
