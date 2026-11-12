package dlutil

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStallWatchdog_CancelsWithoutProgress(t *testing.T) {
	wdog, ctx := NewStallWatchdog(context.Background(), 50*time.Millisecond)
	defer wdog.Close()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not cancel the context on stall")
	}
	if !wdog.Stalled() {
		t.Fatal("Stalled should report true after the watchdog fired")
	}
}

func TestStallWatchdog_KickKeepsContextAlive(t *testing.T) {
	wdog, ctx := NewStallWatchdog(context.Background(), 80*time.Millisecond)
	defer wdog.Close()

	// Kick more often than the stall timeout; the context must stay alive.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		wdog.Kick()
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled while progress was flowing: %v", ctx.Err())
		case <-time.After(30 * time.Millisecond):
		}
	}
	if wdog.Stalled() {
		t.Fatal("Stalled should be false while kicks keep arriving")
	}
}

func TestStallWatchdog_StopsKickingThenCancels(t *testing.T) {
	wdog, ctx := NewStallWatchdog(context.Background(), 60*time.Millisecond)
	defer wdog.Close()

	wdog.Kick()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not cancel after kicks stopped")
	}
	if !wdog.Stalled() {
		t.Fatal("Stalled should report true once the timer expired")
	}
}

func TestStallWatchdog_ParentCancelPropagates(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	wdog, ctx := NewStallWatchdog(parent, time.Hour)
	defer wdog.Close()

	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("parent cancellation did not propagate")
	}
	if wdog.Stalled() {
		t.Fatal("Stalled must be false when the parent was canceled, not a stall")
	}
}

// A sustained floodwait that also starves byte progress past the stall
// window fires BOTH guards: the meter's verdict (crawl) must win — it is
// the specific cause; the watchdog only fired alongside it.
func TestDownloadFailure_MeterBeatsWatchdog(t *testing.T) {
	// Independent guards: in production both watch the same download but
	// neither derives from the other, so the watchdog canceling must not
	// stop the meter's own window tick.
	wdog, _ := NewStallWatchdog(context.Background(), 10*time.Millisecond)
	defer wdog.Close()
	mctx, mcancel := context.WithCancel(context.Background())
	meter := newFloodWaitMeter(mctx, mcancel, 50*time.Millisecond, time.Nanosecond)
	defer meter.Close()
	// Absorb enough slow-RPC time to trip the meter at its window tick
	// (maxSlow = 25ms) while the 10ms watchdog fires in parallel.
	meter.Observe(5*time.Millisecond, nil)
	meter.Observe(5*time.Millisecond, nil)
	meter.Observe(5*time.Millisecond, nil)
	meter.Observe(5*time.Millisecond, nil)
	meter.Observe(5*time.Millisecond, nil)
	meter.Observe(5*time.Millisecond, nil)
	time.Sleep(120 * time.Millisecond) // let both fire
	err := DownloadFailure(context.Canceled, wdog, meter)
	if !strings.Contains(err.Error(), "flood-wait absorbed") {
		t.Fatalf("DownloadFailure with both guards fired = %v; want the crawl verdict", err)
	}
}

// The auto-sleep cap must sit strictly inside the stall window: a
// max-length sleep has to finish before the watchdog can fire.
func TestMaxAutoFloodWait_InsideStallWindow(t *testing.T) {
	if MaxAutoFloodWait >= DownloadStallTimeout {
		t.Fatalf("MaxAutoFloodWait (%v) must be below DownloadStallTimeout (%v)", MaxAutoFloodWait, DownloadStallTimeout)
	}
}
