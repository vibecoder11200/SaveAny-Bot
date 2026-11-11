package dirutil

import (
	"errors"
	"path"
	"strings"
)

// MaxRelDirPathLen caps a stored per-watch dir path. It bounds both the
// stored column and every downstream path join on any storage backend.
const MaxRelDirPathLen = 512

// ErrRelDirPathAbsolute is returned for paths that would escape the storage
// root: a leading slash/backslash, a Windows drive prefix, or a home shortcut.
var ErrRelDirPathAbsolute = errors.New("dir path must be relative to the storage root")

// SanitizeRelDirPath validates a user-supplied relative directory path (the
// dir: flag of /watch) for use on any storage backend. WebDAV and Alist join
// these paths unguarded, so the sanitizer is the single gate against
// traversal: it rejects absolute forms outright, drops any ".." segment
// instead of resolving it, normalizes Windows separators, collapses "." and
// duplicate separators, keeps interior structure (a/b/c stays a/b/c), and
// caps the length. An empty input is valid and means the storage root.
func SanitizeRelDirPath(in string) (string, error) {
	if len(in) > MaxRelDirPathLen {
		return "", errors.New("dir path too long (max 512 characters)")
	}
	trimmed := strings.TrimSpace(in)
	if trimmed == "" {
		return "", nil
	}
	// Absolute forms: POSIX root, Windows root, drive prefix (C:\, C:/),
	// UNC (\\server), and home shortcuts that resolve outside the root.
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "\\") ||
		strings.HasPrefix(trimmed, "~") {
		return "", ErrRelDirPathAbsolute
	}
	if len(trimmed) >= 2 && trimmed[1] == ':' &&
		((trimmed[0] >= 'A' && trimmed[0] <= 'Z') || (trimmed[0] >= 'a' && trimmed[0] <= 'z')) {
		return "", ErrRelDirPathAbsolute
	}
	normalized := strings.ReplaceAll(trimmed, "\\", "/")
	parts := make([]string, 0, strings.Count(normalized, "/")+1)
	for _, seg := range strings.Split(normalized, "/") {
		switch seg {
		case "", ".":
			continue
		case "..":
			// Dropped, not resolved: "a/../.." must never climb out, and a
			// leading ".." must not survive as a traversal prefix.
			continue
		default:
			parts = append(parts, seg)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return path.Join(parts...), nil
}
