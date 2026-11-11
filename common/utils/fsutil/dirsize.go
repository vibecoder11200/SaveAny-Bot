package fsutil

import (
	"io/fs"
	"path/filepath"
	"sync"
	"time"
)

// dirSizeCache memoizes DirSize per path: a /status burst or a few users
// must not stack concurrent full-tree walks on a 100k-file cache dir.
// WalkDir is uncancellable — a naive timeout would leak the goroutine, so
// instead of timing out, in-flight paths share one walk (single flight) and
// finished results live for dirSizeTTL.
var (
	dirSizeMu    sync.Mutex
	dirSizeInFlt = make(map[string]*dirSizeCall)
	dirSizeRes   = make(map[string]dirSizeResult)
)

const dirSizeTTL = 60 * time.Second

type dirSizeCall struct {
	wg   sync.WaitGroup
	size int64
	err  error
}

type dirSizeResult struct {
	size int64
	err  error
	at   time.Time
}

// DirSize sums the file sizes under path (not following symlinks, skipping
// unreadable entries — an ops readout must never fail hard on one locked
// file). Concurrent calls for the same path share one walk; results are
// cached for one minute.
func DirSize(path string) (int64, error) {
	dirSizeMu.Lock()
	if res, ok := dirSizeRes[path]; ok && time.Since(res.at) < dirSizeTTL {
		dirSizeMu.Unlock()
		return res.size, res.err
	}
	if call, ok := dirSizeInFlt[path]; ok {
		dirSizeMu.Unlock()
		call.wg.Wait()
		return call.size, call.err
	}
	call := &dirSizeCall{}
	call.wg.Add(1)
	dirSizeInFlt[path] = call
	dirSizeMu.Unlock()

	var size int64
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == path {
				// The root itself failed: there is no readout at all.
				return err
			}
			// Skip unreadable entries: a size readout is best-effort.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		size += info.Size()
		return nil
	})

	call.size, call.err = size, err
	call.wg.Done()

	dirSizeMu.Lock()
	delete(dirSizeInFlt, path)
	if err == nil {
		dirSizeRes[path] = dirSizeResult{size: size, at: time.Now()}
	}
	dirSizeMu.Unlock()
	return size, err
}
