package dlutil

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// MeterWindow is the rolling window over which slow RPC time is
	// accumulated. It matches DownloadStallTimeout so both watchdogs cut a
	// misbehaving download within the same order of magnitude.
	MeterWindow = 15 * time.Minute

	// SlowRPCThreshold is the per-RPC duration above which a *successful*
	// upload.getFile call is considered flood-wait absorbed. A healthy part
	// request completes in well under a second; a Telegram FLOOD_WAIT sleep
	// lasts minutes. A short flood wait (below the threshold) is absorbed
	// silently and shows up only as reduced throughput, which is fine: with
	// 1MiB parts, waiting 2 minutes per RPC would still mean ~8MiB/min per
	// thread, which is not a "crawling" download.
	SlowRPCThreshold = 2 * time.Minute

	// maxSlowPerWindow is how much absorbed wait within one window the
	// download may accumulate before being judged as crawling: half of the
	// window. With N threads each parking on flood waits, absorbed time adds
	// up across threads, so a genuine crawl reaches this within the first
	// window while a slow-but-legitimate link (RPCs under the threshold)
	// never accumulates any.
	maxSlowPerWindow = MeterWindow / 2
)

// CrawlError builds the user-visible error for a download that was canceled
// by the flood-wait meter.
func CrawlError(absorbed time.Duration) error {
	return fmt.Errorf("download crawling: flood-wait absorbed %s of last %s (account rate-limited)",
		absorbed.Round(time.Second), MeterWindow)
}

// FloodWaitMeter watches a download for "crawling" caused by absorbed
// FLOOD_WAITs: RPCs that succeed but each burn minutes inside Telegram's
// rate-limit sleep. Bytes keep trickling in during those sleeps, so the
// stall watchdog stays quiet while the transfer drags for hours.
//
// The meter accumulates the duration of successful RPCs slower than
// SlowRPCThreshold over a rolling MeterWindow; when the accumulated wait
// reaches maxSlowPerWindow it cancels the download context and Crawled
// reports true. Each window is independent: surviving one bad window does
// not taint the next.
type FloodWaitMeter struct {
	cancel   context.CancelFunc
	done     chan struct{}
	closeOne sync.Once

	slowNanos atomic.Int64
	crawled   atomic.Bool

	window    time.Duration
	threshold time.Duration
	maxSlow   time.Duration
}

// NewFloodWaitMeter starts the meter over the given context. The cancel
// function must cancel ctx (pass the pair obtained from
// context.WithCancel); it is invoked when the meter judges the download a
// crawl. Callers must call Close once the guarded operation has finished.
func NewFloodWaitMeter(ctx context.Context, cancel context.CancelFunc) *FloodWaitMeter {
	return newFloodWaitMeter(ctx, cancel, MeterWindow, SlowRPCThreshold)
}

func newFloodWaitMeter(ctx context.Context, cancel context.CancelFunc, window, threshold time.Duration) *FloodWaitMeter {
	m := &FloodWaitMeter{
		cancel:    cancel,
		done:      make(chan struct{}),
		window:    window,
		threshold: threshold,
		maxSlow:   window / 2,
	}
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(m.window)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if m.slowNanos.Load() >= int64(m.maxSlow) {
					m.crawled.Store(true)
					m.cancel()
					return
				}
				m.slowNanos.Store(0)
			}
		}
	}()
	return m
}

// Observe records one RPC outcome. Only successful calls at or above the
// slow threshold count; failed calls are the download's own business (and
// its error, not a silent wait).
func (m *FloodWaitMeter) Observe(dur time.Duration, err error) {
	if err != nil || dur < m.threshold {
		return
	}
	if m.crawled.Load() {
		return
	}
	m.slowNanos.Add(int64(dur))
}

// Crawled reports whether the meter canceled the download as a crawl.
func (m *FloodWaitMeter) Crawled() bool {
	return m.crawled.Load()
}

// Absorbed returns how much flood-wait time had accumulated when the meter
// fired; zero unless Crawled is true.
func (m *FloodWaitMeter) Absorbed() time.Duration {
	if !m.crawled.Load() {
		return 0
	}
	return time.Duration(m.slowNanos.Load())
}

// Close releases the meter goroutine. Canceling after a finished download
// is harmless; the wait prevents goroutine leaks.
func (m *FloodWaitMeter) Close() {
	m.closeOne.Do(func() {
		m.cancel()
		<-m.done
	})
}
