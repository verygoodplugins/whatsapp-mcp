package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// restrictedMode is set before startup and never disabled by an event or caller.
var restrictedMode bool
var checkPolicyFlag = flag.Bool("check-policy", false, "Validate owner policy without creating credentials, storage or network connections")
var privatePairFlag = flag.Bool("pair-private", false, "Pair only in a private owner-operated terminal after explicit owner approval")

type ownerPolicy struct {
	Version       int             `json:"version"`
	AccessEnabled bool            `json:"access_enabled"`
	AllowedGroups []string        `json:"allowed_groups"`
	Capabilities  map[string]bool `json:"capabilities"`
}

var groupPattern = regexp.MustCompile(`^[0-9]+(-[0-9]+)?@g\.us$`)
var capabilityNames = []string{"read_messages", "cache_messages", "send_messages", "media", "webhooks", "history_requests", "read_receipts", "call_capture"}

func privatePath(path string, directory bool) error {
	if !filepath.IsAbs(path) {
		return errors.New("private path must be absolute")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("private path missing or contains a symlink")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	want := os.FileMode(0o600)
	if directory {
		want = 0o700
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm() != want {
		return errors.New("private path permissions invalid")
	}
	return nil
}

// Reject duplicate JSON keys as well as unknown fields, matching the owner CLI.
func uniqueJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON key")
			}
			seen[name] = true
		}
		if err := uniqueJSON(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func loadOwnerPolicy() (*ownerPolicy, error) {
	path := os.Getenv("WHATSAPP_POLICY_FILE")
	if err := privatePath(filepath.Dir(path), true); err != nil {
		return nil, err
	}
	if err := privatePath(path, false); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("policy file permissions invalid")
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return nil, errors.New("policy unreadable or too large")
	}
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var policy ownerPolicy
	if err := decoder.Decode(&policy); err != nil {
		return nil, errors.New("invalid policy JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing policy JSON")
	}
	// Also enforce presence and types: Go's zero values would accept missing/null booleans.
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || len(raw) != 4 {
		return nil, errors.New("incomplete policy")
	}
	for _, key := range []string{"version", "access_enabled", "allowed_groups", "capabilities"} {
		if _, ok := raw[key]; !ok {
			return nil, errors.New("incomplete policy")
		}
	}
	if policy.Version != 1 || (string(raw["access_enabled"]) != "true" && string(raw["access_enabled"]) != "false") || len(policy.AllowedGroups) > 100 || policy.AllowedGroups == nil || len(policy.Capabilities) != len(capabilityNames) {
		return nil, errors.New("invalid policy schema")
	}
	var caps map[string]json.RawMessage
	_ = json.Unmarshal(raw["capabilities"], &caps)
	for _, key := range capabilityNames {
		value, ok := caps[key]
		if !ok || (string(value) != "true" && string(value) != "false") {
			return nil, errors.New("invalid capability")
		}
		if key != "read_messages" && key != "cache_messages" && policy.Capabilities[key] {
			return nil, errors.New("capability unsupported by read-only profile")
		}
	}
	seen := map[string]bool{}
	for _, group := range policy.AllowedGroups {
		if !groupPattern.MatchString(group) || seen[group] {
			return nil, errors.New("invalid or duplicate group JID")
		}
		seen[group] = true
	}
	if policy.AccessEnabled && len(seen) == 0 {
		return nil, errors.New("enabled policy needs groups")
	}
	return &policy, nil
}

func restricted() bool { return restrictedMode || os.Getenv("WHATSAPP_POLICY_FILE") != "" }

func cacheAllowed(jid string) bool {
	if !restricted() {
		return true
	} // Library helpers retain legacy semantics; executable always restricts.
	policy, err := loadOwnerPolicy()
	if err != nil || !policy.AccessEnabled || !policy.Capabilities["cache_messages"] {
		return false
	}
	for _, group := range policy.AllowedGroups {
		if group == jid {
			return true
		}
	}
	return false
}

func validateStoreFiles() error {
	for _, name := range []string{"whatsapp.db", "messages.db", ".bridge-token", "whatsapp.db-wal", "whatsapp.db-shm", "whatsapp.db-journal", "messages.db-wal", "messages.db-shm", "messages.db-journal"} {
		path, err := filepath.Abs(filepath.Join("store", name))
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		if err := privatePath(path, false); err != nil {
			return errors.New("private store file invalid")
		}
	}
	return nil
}
