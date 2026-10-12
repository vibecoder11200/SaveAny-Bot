package dlutil

import (
	"context"
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
