package main

// Outbound media-path confinement.
//
// Today /api/send accepts any absolute path in media_path and the bridge
// happily reads it via os.ReadFile. That makes the bridge a generic
// arbitrary-file-read primitive for any local caller (after auth) — and
// for prompt-injection attacks that reach an MCP-driven agent — because
// the file is then sent out as a WhatsApp document to an attacker-chosen
// recipient.
//
// We confine media_path to a configurable allow-list of root directories.
// Default: ~/.local/share/whatsapp-mcp/outbox (created on first use).
// Override: colon-separated absolute paths in WHATSAPP_MEDIA_ROOTS.
//
// Symlinks are resolved before the prefix check, so dropping a symlink
// inside outbox/ that points at /etc/passwd does not bypass the guard.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultOutboxSubpath is appended to the user's home directory when no
// override is configured. ~/.local/share is the XDG default for
// user-specific data files; using a project subdirectory keeps the bridge
// out of the way of unrelated tools.
const defaultOutboxSubpath = ".local/share/whatsapp-mcp/outbox"

// resolveMediaRoots returns the list of allowed absolute root directories
// for outbound media. Roots are resolved via filepath.EvalSymlinks where
// possible so a later prefix check is meaningful even if the user pointed
// the env var at a symlinked location.
func resolveMediaRoots() ([]string, error) {
	if env := strings.TrimSpace(os.Getenv("WHATSAPP_MEDIA_ROOTS")); env != "" {
		var roots []string
		for _, raw := range strings.Split(env, string(os.PathListSeparator)) {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if !filepath.IsAbs(raw) {
				return nil, fmt.Errorf("WHATSAPP_MEDIA_ROOTS entries must be absolute paths, got %q", raw)
			}
			resolved, err := canonicalizePath(raw)
			if err != nil {
				return nil, fmt.Errorf("resolve %q: %w", raw, err)
			}
			roots = append(roots, resolved)
		}
		if len(roots) == 0 {
			return nil, errors.New("WHATSAPP_MEDIA_ROOTS is set but contains no valid entries")
		}
		return roots, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determine home directory: %w", err)
	}
	def := filepath.Join(home, defaultOutboxSubpath)
	if mkErr := os.MkdirAll(def, 0o700); mkErr != nil {
		return nil, fmt.Errorf("create default outbox %q: %w", def, mkErr)
	}
	resolved, err := canonicalizePath(def)
	if err != nil {
		return nil, fmt.Errorf("resolve default outbox: %w", err)
	}
	return []string{resolved}, nil
}

// canonicalizePath returns an absolute, symlink-resolved path. EvalSymlinks
// requires the path to exist; for newly-created paths the caller must
// ensure existence first (see resolveMediaRoots).
func canonicalizePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Path doesn't exist yet (or is unreadable). Fall back to Abs;
		// validateMediaPath will run EvalSymlinks again at request time
		// when the file is expected to exist.
		return abs, nil
	}
	return resolved, nil
}

// validateMediaPath checks whether mediaPath points at a regular file
// inside one of the allowed roots. Returns the canonical path on success
// so callers read from the resolved file (defends against TOCTOU between
// validation and ReadFile in degenerate cases).
func validateMediaPath(mediaPath string, allowedRoots []string) (string, error) {
	if mediaPath == "" {
		return "", errors.New("media_path is empty")
	}
	if !filepath.IsAbs(mediaPath) {
		return "", fmt.Errorf("media_path must be absolute, got %q", mediaPath)
	}

	resolved, err := filepath.EvalSymlinks(mediaPath)
	if err != nil {
		return "", fmt.Errorf("resolve media_path: %w", err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat media_path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("media_path is not a regular file: %q", resolved)
	}

	for _, root := range allowedRoots {
		if pathHasPrefix(resolved, root) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf(
		"media_path %q is outside the configured media roots; "+
			"set WHATSAPP_MEDIA_ROOTS to allow additional directories",
		resolved,
	)
}

// pathHasPrefix returns true when child is the same path as parent, or a
// strict descendant. Plain string-prefix matching is unsafe ("/foo/bar"
// would match "/foo/barbaz"), so we require either exact match or an
// explicit separator after parent.
func pathHasPrefix(child, parent string) bool {
	if child == parent {
		return true
	}
	if !strings.HasPrefix(child, parent) {
		return false
	}
	rest := child[len(parent):]
	return strings.HasPrefix(rest, string(os.PathSeparator))
}

// outboundFileName returns the name a RECIPIENT should see for an outbound
// document, given a local media path.
//
// This is not cosmetic. The document filename travels to the recipient inside
// the message, so every character of the path we put here is published. The
// previous code took everything after the last forward slash:
//
//	mediaPath[strings.LastIndex(mediaPath, "/")+1:]
//
// On Windows that leaks the entire absolute path. The path handed to the send
// routine has already been through filepath.EvalSymlinks (see the /api/send
// handler), which normalises separators to backslash, so LastIndex for "/"
// returns -1, the slice starts at 0, and the recipient sees something like
// C:\Users\<name>\.local\share\whatsapp-mcp\outbox\file.zip -- username and
// home layout included.
//
// filepath.Base alone is not enough either: it only honours the separator of
// the platform it runs on, so a Windows-style path handled by a Linux or macOS
// build would still come through whole. We split on BOTH separators regardless
// of GOOS, which is what a filename crossing machines requires.
//
// If the path ends in a separator, or is otherwise nameless, we fall back to a
// neutral constant rather than sending an empty filename.
func outboundFileName(mediaPath string) string {
	name := mediaPath
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	// Drop a Windows drive prefix such as "C:" left behind by a bare
	// "C:file.zip" style path, which has no separator at all.
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "file"
	}
	return name
}
