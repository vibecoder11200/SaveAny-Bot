package dlutil

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// DownloadStallTimeout is how long a download may make zero progress before
// the watchdog cancels it. It must stay comfortably above the connection
// recovery window (see client/middleware recovery timeout) so that a
// reconnect that would have succeeded is not cut short.
const DownloadStallTimeout = 15 * time.Minute

// StallWatchdog cancels its context when no progress has been observed for
// the whole stall timeout. Total download time is unlimited as long as bytes
// keep flowing, which keeps large files on slow links working while silent
// stalls (an RPC parked in a long flood wait, a half-open connection) fail
// with a visible error instead of hanging a task forever.
type StallWatchdog struct {
	kick     chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
	stalled  atomic.Bool
	closeOne sync.Once
}

// NewStallWatchdog returns a watchdog and a derived context that is canceled
// on stall (or when the parent is canceled). Callers must call Close once the
// guarded operation has finished.
func NewStallWatchdog(parent context.Context, stallAfter time.Duration) (*StallWatchdog, context.Context) {
	innerCtx, innerCancel := context.WithCancel(parent)
	w := &StallWatchdog{
		kick:   make(chan struct{}, 1),
		cancel: innerCancel,
		done:   make(chan struct{}),
	}
	go func() {
		defer close(w.done)
		timer := time.NewTimer(stallAfter)
		defer timer.Stop()
		for {
			select {
			case <-innerCtx.Done():
				return
			case <-w.kick:
				if !timer.Stop() {
					// Drain the timer channel if it already fired.
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(stallAfter)
			case <-timer.C:
				w.stalled.Store(true)
				w.cancel()
				return
			}
		}
	}()
	return w, innerCtx
}

// Kick records progress and resets the stall timer. Cheap and safe for
// concurrent use; never blocks.
func (w *StallWatchdog) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
		// A kick is already pending; the timer will be reset anyway.
	}
}

// Stalled reports whether the watchdog fired.
func (w *StallWatchdog) Stalled() bool {
	return w.stalled.Load()
}

// Close releases the watchdog goroutine. Canceling after a finished download
// is harmless; the wait prevents goroutine leaks.
func (w *StallWatchdog) Close() {
	w.closeOne.Do(func() {
		w.cancel()
		<-w.done
	})
}

// DownloadFailure is the shared post-mortem for a download guarded by a
// StallWatchdog and a FloodWaitMeter: it reports the most specific cause —
// a silent stall, a flood-wait crawl — and falls back to wrapping the raw
// error.
func DownloadFailure(err error, wdog *StallWatchdog, meter *FloodWaitMeter) error {
	if wdog.Stalled() {
		return fmt.Errorf("download stalled: no data received for %s", DownloadStallTimeout)
	}
	if meter.Crawled() {
		return CrawlError(meter.Absorbed())
	}
	return fmt.Errorf("failed to download file: %w", err)
}
