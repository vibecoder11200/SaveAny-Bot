package tgutil

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/time/rate"
)

// ProgressSender performs one progress message edit. It may block for minutes
// when Telegram asks the client to wait (FLOOD_WAIT).
type ProgressSender func(ctx context.Context, chatID int64, req *tg.MessagesEditMessageRequest) error

const progressEditInterval = time.Second

// progressEditMaxPause bounds how long one flood wait may pause progress
// editing. Telegram hands multi-hour penalties to busy accounts; the cap
// keeps a task's final edit from waiting behind one for its full length —
// after the pause the next attempt re-detects any remaining penalty.
const progressEditMaxPause = time.Hour

var progressEditLimiter = rate.NewLimiter(rate.Every(progressEditInterval), 1)

// progressEditFloodedUntil is the instant until which progress edits are
// paused after a FLOOD_WAIT. Progress is cosmetic: hammering a penalized
// account once per second neither updates the message nor shortens the
// penalty, and a rejected edit storm is what escalates short waits into
// account-level ones.
var progressEditFloodedUntil atomic.Int64 // unix nanoseconds

func progressEditFlooded() bool {
	return time.Now().UnixNano() < progressEditFloodedUntil.Load()
}

// markProgressEditFlooded pauses progress edits for wait (clamped to
// [0, progressEditMaxPause]) and returns the clamped duration.
func markProgressEditFlooded(wait time.Duration) time.Duration {
	if wait > progressEditMaxPause {
		wait = progressEditMaxPause
	}
	if wait < 0 {
		wait = 0
	}
	progressEditFloodedUntil.Store(time.Now().Add(wait).UnixNano())
	return wait
}

func SendProgressEdit(ctx context.Context, chatID int64, req *tg.MessagesEditMessageRequest) error {
	if err := progressEditLimiter.Wait(context.Background()); err != nil {
		return err
	}
	ext := ExtFromContext(ctx)
	if ext == nil {
		return nil
	}
	if _, err := ext.EditMessage(chatID, req); err != nil {
		if tgerr.Is(err, "MESSAGE_NOT_MODIFIED") {
			// The message already shows exactly this content — the coalescing
			// queue raced a render that settled. Idempotent success.
			return nil
		}
		if wait, ok := tgerr.AsFloodWait(err); ok {
			pause := markProgressEditFlooded(wait)
			log.FromContext(ctx).Warnf("Progress edit flood-limited, pausing progress edits for %s", pause)
		}
		return err
	}
	return nil
}

type progressEditJob struct {
	ctx    context.Context
	chatID int64
	req    *tg.MessagesEditMessageRequest
	final  bool
}

type ProgressEditQueue struct {
	sender ProgressSender
	slot   chan progressEditJob
	start  sync.Once
}

func NewProgressEditQueue(sender ProgressSender) *ProgressEditQueue {
	return &ProgressEditQueue{
		sender: sender,
		slot:   make(chan progressEditJob, 1),
	}
}

func (q *ProgressEditQueue) Submit(ctx context.Context, chatID int64, req *tg.MessagesEditMessageRequest, final bool) {
	job := progressEditJob{ctx: ctx, chatID: chatID, req: req, final: final}
	q.start.Do(func() { go q.run() })
	for {
		select {
		case q.slot <- job:
			return
		default:
		}
		select {
		case pending := <-q.slot:
			if pending.final {
				// The final edit must stay last; nothing may be queued after it.
				q.slot <- pending
				return
			}
		default:
		}
	}
}

func (q *ProgressEditQueue) run() {
	for job := range q.slot {
		q.perform(job)
		if job.final {
			return
		}
	}
}

func (q *ProgressEditQueue) perform(job progressEditJob) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.FromContext(job.ctx).Errorf("Recovered from panic while editing progress message: %v", recovered)
		}
	}()
	if progressEditFlooded() {
		if !job.final {
			// A cosmetic update during a flood penalty: drop it instead of
			// feeding the fire once per second.
			return
		}
		// The final edit is the task's closing state — wait out the pause
		// (bounded by the cap, and by the task's context) rather than lose it.
		wait := time.Until(time.Unix(0, progressEditFloodedUntil.Load()))
		select {
		case <-time.After(wait):
		case <-job.ctx.Done():
			return
		}
	}
	if err := q.sender(job.ctx, job.chatID, job.req); err != nil {
		log.FromContext(job.ctx).Errorf("Failed to edit progress message: %v", err)
	}
}
