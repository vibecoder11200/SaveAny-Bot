package handlers

import (
	"context"
	"math"
	"sync"

	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// watchPendingSet tracks, per watch, the message IDs that were observed
// but have NOT reached a resolved outcome yet: enqueue failures, user
// fetch errors, album members still buffered (or whose flush failed). The
// live/flush cursor advances are clamped below the smallest pending ID, so
// a later resolution can never leap the monotonic never-lowering cursor
// PAST an unresolved message — without the clamp, a resolved single at
// N+1 would permanently shadow a failed single at N, and the promised
// next-boot re-fetch would never see it.
//
// In-memory only by design: a restart drops the set, but the DB cursor was
// never advanced past any unresolved ID, so the next boot's catch-up
// re-fetches exactly those messages (cap-bounded).
type watchPendingSet struct {
	mu  sync.Mutex
	ids map[int64]struct{}
}

// watchPendingCap bounds the per-watch set. Beyond it, tracking stops
// clamping (fail-open): the alternative is unbounded memory growth from a
// persistently failing watch, the worse trade for a single-user bot.
const watchPendingCap = 4096

var watchPending sync.Map // watch ID (uint) -> *watchPendingSet

func watchPendingFor(watchID uint) *watchPendingSet {
	v, _ := watchPending.LoadOrStore(watchID, &watchPendingSet{ids: make(map[int64]struct{})})
	return v.(*watchPendingSet)
}

func (p *watchPendingSet) add(msgID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.ids) >= watchPendingCap {
		return
	}
	p.ids[msgID] = struct{}{}
}

func (p *watchPendingSet) remove(msgIDs ...int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range msgIDs {
		delete(p.ids, id)
	}
}

func (p *watchPendingSet) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.ids)
}

// ceiling returns the largest cursor value that is still safe to advance
// to: one below the smallest pending message ID, or the max int64 when
// nothing is pending.
func (p *watchPendingSet) ceiling() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	lowest := int64(math.MaxInt64)
	for id := range p.ids {
		lowest = min(lowest, id)
	}
	return lowest - 1
}

// clampWatchCursor bounds a candidate cursor value below the smallest
// pending unresolved message of that watch.
func clampWatchCursor(watchID uint, id int64) int64 {
	return min(id, watchPendingFor(watchID).ceiling())
}

// advanceWatchCursor moves the watch cursor to msgID, clamped below the
// smallest pending unresolved message of that watch. The error is returned,
// not logged: the live intake warns, the flush paths rely on the next
// boot's re-fetch instead.
func advanceWatchCursor(ctx context.Context, watchID uint, msgID int64) error {
	return database.TouchWatchCursor(ctx, watchID, clampWatchCursor(watchID, msgID))
}

// resolveWatchFile records one single's resolved outcome: unpin its message
// from the pending set and advance the cursor past it (clamped below
// anything still pending for the watch).
func resolveWatchFile(ctx context.Context, watch *database.WatchChat, file tfile.TGFileMessage) {
	if file.Message() == nil {
		return
	}
	watchPendingFor(watch.ID).remove(int64(file.Message().ID))
	_ = advanceWatchCursor(ctx, watch.ID, int64(file.Message().ID))
}

// resolveWatchMessages records a flushed album's resolved outcome: unpin
// every member from the pending set and return the largest message ID —
// the group's cursor-advance candidate.
func resolveWatchMessages(watchID uint, files []tfile.TGFileMessage) int64 {
	ids := make([]int64, 0, len(files))
	var maxID int64
	for _, f := range files {
		id := int64(f.Message().ID)
		ids = append(ids, id)
		maxID = max(maxID, id)
	}
	watchPendingFor(watchID).remove(ids...)
	return maxID
}
