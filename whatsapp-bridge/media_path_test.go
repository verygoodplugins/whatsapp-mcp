package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// helper: create a temporary file inside dir and return its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestPathHasPrefix(t *testing.T) {
	sep := string(os.PathSeparator)
	cases := []struct {
		child, parent string
		want          bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b" + sep + "c", "/a/b", true},
		{"/a/bbb", "/a/b", false}, // sibling-prefix attack
		{"/a", "/a/b", false},     // shorter
		{"", "/a", false},
	}
	for _, tc := range cases {
		t.Run(tc.child+"|"+tc.parent, func(t *testing.T) {
			if got := pathHasPrefix(tc.child, tc.parent); got != tc.want {
				t.Fatalf("pathHasPrefix(%q,%q) = %v, want %v", tc.child, tc.parent, got, tc.want)
			}
		})
	}
}

func TestValidateMediaPathAcceptsFileInsideRoot(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval root: %v", err)
	}
	f := writeFile(t, root, "ok.txt", "hello")

	got, err := validateMediaPath(f, []string{resolvedRoot})
	if err != nil {
		t.Fatalf("expected accept, got error: %v", err)
	}
	resolvedFile, _ := filepath.EvalSymlinks(f)
	if got != resolvedFile {
		t.Fatalf("expected resolved path %q, got %q", resolvedFile, got)
	}
}

func TestValidateMediaPathRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, _ := filepath.EvalSymlinks(root)
	other := t.TempDir()
	f := writeFile(t, other, "elsewhere.txt", "nope")

	if _, err := validateMediaPath(f, []string{resolvedRoot}); err == nil {
		t.Fatal("expected error for path outside root, got nil")
	}
}

func TestValidateMediaPathRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows; covered by Unix CI")
	}
	root := t.TempDir()
	resolvedRoot, _ := filepath.EvalSymlinks(root)
	secrets := t.TempDir()
	target := writeFile(t, secrets, "id_rsa", "PRETEND-PRIVATE-KEY")

	// Drop a symlink inside the allowed root that points at a file outside.
	link := filepath.Join(root, "stolen")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := validateMediaPath(link, []string{resolvedRoot}); err == nil {
		t.Fatal("expected symlink escape to be rejected, got nil error")
	} else if !strings.Contains(err.Error(), "outside the configured media roots") {
		t.Fatalf("expected confinement error, got: %v", err)
	}
}

func TestValidateMediaPathRejectsRelative(t *testing.T) {
	if _, err := validateMediaPath("not/absolute.txt", []string{"/tmp"}); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestValidateMediaPathRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, _ := filepath.EvalSymlinks(root)
	subdir := filepath.Join(root, "sub")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := validateMediaPath(subdir, []string{resolvedRoot}); err == nil {
		t.Fatal("expected error for directory, got nil")
	}
}

func TestResolveMediaRootsRejectsRelativeEnv(t *testing.T) {
	t.Setenv("WHATSAPP_MEDIA_ROOTS", "relative/path")
	if _, err := resolveMediaRoots(); err == nil {
		t.Fatal("expected error for relative path in env, got nil")
	}
}

func TestResolveMediaRootsAcceptsEnvList(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	t.Setenv("WHATSAPP_MEDIA_ROOTS", a+string(os.PathListSeparator)+b)

	roots, err := resolveMediaRoots()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(roots))
	}
}
