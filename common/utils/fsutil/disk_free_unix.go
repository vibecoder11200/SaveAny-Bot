//go:build !windows

package fsutil

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// DiskFree reports the free bytes available to unprivileged writes on the
// filesystem containing path.
func DiskFree(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

// DiskID returns a stable identity for the filesystem containing path (the
// stat device number), so callers can tell two filesystems apart even when
// their free-space readouts coincide.
func DiskID(path string) (string, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(st.Dev), 10), nil
}
