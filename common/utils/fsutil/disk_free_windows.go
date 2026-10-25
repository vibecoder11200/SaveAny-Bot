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
