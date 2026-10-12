package tfile

import (
	"strings"

	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
)

// SanitizeFileName reduces a filename to a single safe path component: it
// collapses directory components (traversal attempts like "../" are stripped
// down to the base name), replaces characters invalid on common filesystems
// and trims trailing dots and spaces (Windows). An input with no usable
// content yields "file".
//
// Every TG file name passes through this, both at construction and on
// later renames, so journal payloads and destination paths can never escape
// the cache or storage directories via a crafted file name.
func SanitizeFileName(name string) string {
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = fsutil.NormalizePathname(name)
	if name == "" {
		return "file"
	}
	return name
}
