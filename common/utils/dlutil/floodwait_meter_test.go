package dlutil

import (
	"context"
	"sync"
	"testing"
	"time"
)

// testWindow constants keep the meter tests fast while preserving the
// production ratio maxSlow = window/2 and threshold < maxSlow.
const (
	testWindow    = 200 * time.Millisecond
	testThreshold = 20 * time.Millisecond
)

func newTestMeter(t *testing.T) (*FloodWaitMeter, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	m := newFloodWaitMeter(ctx, cancel, testWindow, testThreshold)
	t.Cleanup(m.Close)
	return m, ctx
}

func TestFloodWaitMeter_CancelsAfterSustainedSlowRPCs(t *testing.T) {
	m, ctx := newTestMeter(t)

	// Keep one slow RPC in flight per half-window; 100ms waits are above the
	// 20ms threshold, so they accumulate toward maxSlow (100ms per window).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.Observe(100*time.Millisecond, nil)
		time.Sleep(30 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !m.Crawled() {
				t.Fatal("Crawled must be true after the meter fired")
			}
			if m.Absorbed() <= 0 {
				t.Fatal("Absorbed should report the accumulated wait")
			}
			return
		default:
		}
	}
	t.Fatal("meter did not cancel the context despite sustained slow RPCs")
}

func TestFloodWaitMeter_FastRPCsKeepDownloadAlive(t *testing.T) {
	m, ctx := newTestMeter(t)

	deadline := time.Now().Add(3 * testWindow)
	for time.Now().Before(deadline) {
		m.Observe(testThreshold/2, nil)
		time.Sleep(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled despite fast RPCs: %v", ctx.Err())
		default:
		}
	}
	if m.Crawled() {
		t.Fatal("Crawled must stay false while RPCs stay under the threshold")
	}
}

func TestFloodWaitMeter_WindowsAreIndependent(t *testing.T) {
	m, ctx := newTestMeter(t)

	// Window 1: accumulate just below maxSlow (100ms). The window tick must
	// reset it, so a fresh 90ms in window 2 stays legal even though the raw
	// total across windows would cross the budget.
	m.Observe(50*time.Millisecond, nil)
	m.Observe(40*time.Millisecond, nil) // 90ms < maxSlow
	// Two full windows: the window tick must have fired (resetting the
	// accumulator) before the next observation, regardless of scheduler
	// delays on loaded CI runners.
	time.Sleep(2 * testWindow)
	m.Observe(90*time.Millisecond, nil) // would push the raw total over maxSlow
	time.Sleep(testWindow + 20*time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatalf("meter fired across windows despite sub-threshold totals: %v", ctx.Err())
	default:
	}
	if m.Crawled() {
		t.Fatal("Crawled must stay false when no single window reaches maxSlow")
	}
}

func TestFloodWaitMeter_FailedRPCNotCounted(t *testing.T) {
	m, ctx := newTestMeter(t)

	deadline := time.Now().Add(3 * testWindow)
	for time.Now().Before(deadline) {
		m.Observe(time.Hour, context.Canceled)
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-ctx.Done():
		t.Fatalf("context canceled despite only failed RPCs: %v", ctx.Err())
	default:
	}
	if m.Crawled() {
		t.Fatal("failed RPCs must never count toward the crawl budget")
	}
}

func TestFloodWaitMeter_ParentCancelDoesNotCrawl(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	ctx, cancel := context.WithCancel(parent)
	m := newFloodWaitMeter(ctx, cancel, testWindow, testThreshold)
	defer m.Close()

	m.Observe(time.Hour, nil)
	parentCancel()
	select {
	case <-m.done:
	case <-time.After(2 * time.Second):
		t.Fatal("meter goroutine did not exit after parent cancel")
	}
	if m.Crawled() {
		t.Fatal("Crawled must be false when the parent was canceled")
	}
}

func TestFloodWaitMeter_ConcurrentObserve(t *testing.T) {
	m, ctx := newTestMeter(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.Observe(50*time.Millisecond, nil)
				m.Observe(1*time.Millisecond, nil)
				m.Observe(50*time.Millisecond, context.DeadlineExceeded)
			}
		}()
	}
	wg.Wait()
	// The race detector is the point; just make sure Close does not deadlock.
	m.Close()
	select {
	case <-ctx.Done():
	default:
	}
}
