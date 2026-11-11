package tfile

import (
	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
)

// SanitizeFileName reduces a filename to a single safe path component: it
// collapses directory components (traversal attempts like "../" are stripped
// down to the base name), replaces characters invalid on common filesystems,
// shifts Windows reserved device stems and trims trailing dots and spaces.
// An input with no usable content yields "file" — except an empty input
// stays empty, so optional names (WithNameIfEmpty) keep working.
//
// Every TG file name passes through this, both at construction and on
// later renames, so cache paths, journal payloads and destination paths can
// never escape the cache or storage directories via a crafted file name.
func SanitizeFileName(name string) string {
	if name == "" {
		return ""
	}
	return fsutil.SafeBaseName(name)
}
