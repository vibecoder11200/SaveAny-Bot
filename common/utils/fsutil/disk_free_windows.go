//go:build windows

package fsutil

import (
	"golang.org/x/sys/windows"
)

// DiskFree reports the free bytes available on the volume containing path.
func DiskFree(path string) (uint64, error) {
	var free, total, avail uint64
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &avail); err != nil {
		return 0, err
	}
	return free, nil
}

// DiskID returns a stable identity for the volume containing path (its
// volume root, e.g. `C:\`), so callers can tell two volumes apart even when
// their free-space readouts coincide.
func DiskID(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_PATH)
	if err := windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf), nil
}
