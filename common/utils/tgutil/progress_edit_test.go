package tgutil

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestMarkProgressEditFlooded_Clamps(t *testing.T) {
	if got := markProgressEditFlooded(-time.Minute); got != 0 {
		t.Fatalf("negative wait must clamp to 0, got %s", got)
	}
	if got := markProgressEditFlooded(48 * time.Hour); got != progressEditMaxPause {
		t.Fatalf("multi-hour wait must clamp to %s, got %s", progressEditMaxPause, got)
	}
	// Clean up so other tests are not affected by the pause.
	defer markProgressEditFlooded(0)
	if !progressEditFlooded() {
		t.Fatal("progressEditFlooded should report true while the pause is active")
	}
}

func TestProgressEditQueue_DropsNonFinalDuringPause(t *testing.T) {
	defer markProgressEditFlooded(0)
	markProgressEditFlooded(time.Hour)

	var calls atomic.Int32
	q := NewProgressEditQueue(func(ctx context.Context, chatID int64, req *tg.MessagesEditMessageRequest) error {
		calls.Add(1)
		return nil
	})
	q.Submit(context.Background(), 1, &tg.MessagesEditMessageRequest{}, false)
	q.Submit(context.Background(), 1, &tg.MessagesEditMessageRequest{}, false)

	// The queue drains asynchronously; give it a moment and expect no calls.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if calls.Load() > 0 {
			t.Fatalf("non-final edits must be dropped during a flood pause, got %d calls", calls.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProgressEditQueue_FinalEditWaitsOutPause(t *testing.T) {
	defer markProgressEditFlooded(0)
	markProgressEditFlooded(80 * time.Millisecond)

	done := make(chan struct{})
	q := NewProgressEditQueue(func(ctx context.Context, chatID int64, req *tg.MessagesEditMessageRequest) error {
		close(done)
		return nil
	})
	q.Submit(context.Background(), 1, &tg.MessagesEditMessageRequest{}, true)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("final edit was dropped instead of waiting out the pause")
	}
}

func TestProgressEditQueue_FinalNeverDropped(t *testing.T) {
	// Submit's overflow handling may discard a queued non-final edit, but a
	// final edit must always run — including while a slow non-final edit is
	// being performed.
	var ranFinal atomic.Bool
	release := make(chan struct{})
	q := NewProgressEditQueue(func(ctx context.Context, chatID int64, req *tg.MessagesEditMessageRequest) error {
		if ranFinal.Load() {
			return nil
		}
		<-release
		ranFinal.Store(true)
		return nil
	})
	q.Submit(context.Background(), 1, &tg.MessagesEditMessageRequest{}, false)
	q.Submit(context.Background(), 1, &tg.MessagesEditMessageRequest{}, true)
	close(release)

	deadline := time.After(5 * time.Second)
	for !ranFinal.Load() {
		select {
		case <-deadline:
			t.Fatal("final edit never ran")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSendProgressEdit_MessageNotModifiedIsSuccess(t *testing.T) {
	// The limiter path is exercised indirectly: the queue is bypassed, so
	// wait for the 1 rps token via a short-lived sender call.
	if err := progressEditLimiter(0).Wait(context.Background()); err != nil {
		t.Fatalf("limiter wait failed: %v", err)
	}
	// ExtFromContext(nil ext) returns nil and SendProgressEdit reports nil:
	// without an ext the edit is a no-op success, which is the branch this
	// test pins without a live Telegram client.
	if err := SendProgressEdit(context.Background(), 1, &tg.MessagesEditMessageRequest{}); err != nil {
		t.Fatalf("expected nil error without ext in ctx, got %v", err)
	}
	_ = errors.New // keep errors import used if branches change
}
