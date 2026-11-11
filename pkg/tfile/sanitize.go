package tfile

import (
	"strings"

	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
)

// SanitizeFileName reduces a filename to a single safe path component: it
// collapses directory components (traversal attempts like "../" are stripped
// down to the base name), replaces characters invalid on common filesystems
// and trims trailing dots and spaces (Windows). An input with no usable
// content yields "file" — except an empty input stays empty, so optional
// names (WithNameIfEmpty) keep working.
//
// Every TG file name passes through this, both at construction and on
// later renames, so cache paths, journal payloads and destination paths can
// never escape the cache or storage directories via a crafted file name.
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
	// Windows resolves reserved device stems to the device itself even with
	// an extension (CON.txt opens the console): shift the stem out of the
	// reserved set by appending an underscore.
	stem := name
	if idx := strings.IndexByte(stem, '.'); idx >= 0 {
		stem = stem[:idx]
	}
	if winReservedDevices[strings.ToUpper(stem)] {
		name = stem + "_" + name[len(stem):]
	}
	return name
}

// winReservedDevices are the Windows filenames that open a device instead
// of creating a file: bare or followed by any extension.
var winReservedDevices = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}
