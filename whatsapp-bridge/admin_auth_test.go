package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateAdminTokenCreatesOwnerOnlyFile(t *testing.T) {
	t.Setenv("WHATSAPP_ADMIN_TOKEN", "")
	path := filepath.Join(t.TempDir(), ".admin-token")

	token, fresh, err := loadOrCreateAdminTokenAtPath(path)
	if err != nil {
		t.Fatalf("create admin token: %v", err)
	}
	if !fresh {
		t.Fatal("new admin token should be reported as freshly generated")
	}
	if len(token) != tokenByteLen*2 {
		t.Fatalf("generated token length=%d, want %d", len(token), tokenByteLen*2)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat generated admin token: %v", err)
	}
	if info.Mode().Perm() != tokenFileMode {
		t.Fatalf("generated admin token mode=%04o, want %04o", info.Mode().Perm(), tokenFileMode)
	}

	reloaded, fresh, err := loadOrCreateAdminTokenAtPath(path)
	if err != nil {
		t.Fatalf("reload admin token: %v", err)
	}
	if fresh || reloaded != token {
		t.Fatalf("reloaded token=(%q, fresh=%v), want original token and fresh=false", reloaded, fresh)
	}
}

func TestLoadOrCreateAdminTokenRejectsUnsafeExistingFiles(t *testing.T) {
	t.Setenv("WHATSAPP_ADMIN_TOKEN", "")
	validToken := strings.Repeat("a", adminTokenMinLength)

	t.Run("short token", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".admin-token")
		if err := os.WriteFile(path, []byte("too-short\n"), tokenFileMode); err != nil {
			t.Fatalf("write short token: %v", err)
		}
		if _, _, err := loadOrCreateAdminTokenAtPath(path); err == nil {
			t.Fatal("expected short admin token file to be rejected")
		}
	})

	t.Run("insecure permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".admin-token")
		if err := os.WriteFile(path, []byte(validToken+"\n"), tokenFileMode); err != nil {
			t.Fatalf("write admin token: %v", err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("make admin token permissions insecure: %v", err)
		}
		if _, _, err := loadOrCreateAdminTokenAtPath(path); err == nil {
			t.Fatal("expected group/world-readable admin token file to be rejected")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		path := filepath.Join(dir, ".admin-token")
		if err := os.WriteFile(target, []byte(validToken+"\n"), tokenFileMode); err != nil {
			t.Fatalf("write symlink target: %v", err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, _, err := loadOrCreateAdminTokenAtPath(path); err == nil {
			t.Fatal("expected symlink admin token file to be rejected")
		}
	})
}

func TestLoadOrCreateAdminTokenRejectsShortEnvironmentValue(t *testing.T) {
	t.Setenv("WHATSAPP_ADMIN_TOKEN", "too-short")
	if _, _, err := loadOrCreateAdminTokenAtPath(filepath.Join(t.TempDir(), ".admin-token")); err == nil {
		t.Fatal("expected short WHATSAPP_ADMIN_TOKEN to be rejected")
	}
}

func TestValidateAdminTokenSeparation(t *testing.T) {
	token := strings.Repeat("b", adminTokenMinLength)
	if err := validateAdminTokenSeparation(token, token); err == nil {
		t.Fatal("expected matching bridge and admin tokens to be rejected")
	}
	if err := validateAdminTokenSeparation(token, strings.Repeat("c", adminTokenMinLength)); err != nil {
		t.Fatalf("distinct bridge and admin tokens were rejected: %v", err)
	}
}
