//go:build !windows

package fsutil

import (
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
