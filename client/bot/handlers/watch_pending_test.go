package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/krau/mygotg/ext"

	userclient "github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// The pending set gates every cursor advance: the ceiling is one below the
// smallest unresolved ID, and clamp bounds any candidate below it.
func TestWatchPendingSetCeilingAndClamp(t *testing.T) {
	const wid = uint(900001)
	p := watchPendingFor(wid)
	p.add(300)
	p.add(100)
	p.add(200)
	if c := clampWatchCursor(wid, 350); c != 99 {
		t.Fatalf("clamp = %d, want 99", c)
	}
	// The pinned message resolving frees the clamp up to the next one.
	p.remove(100)
	if c := clampWatchCursor(wid, 350); c != 199 {
		t.Fatalf("clamp after remove = %d, want 199", c)
	}
	p.remove(200, 300)
	if c := clampWatchCursor(wid, 350); c != 350 {
		t.Fatalf("unclamped = %d, want 350", c)
	}
}

// Beyond the cap the set stops growing (fail-open) instead of eating
// memory from a persistently failing watch.
func TestWatchPendingSetCap(t *testing.T) {
	const wid = uint(900002)
	p := watchPendingFor(wid)
	for i := 0; i < watchPendingCap+50; i++ {
		p.add(int64(i))
	}
	if got := p.len(); got != watchPendingCap {
		t.Fatalf("len = %d, want cap %d", got, watchPendingCap)
	}
}

// groupFlushDelay never schedules past the absolute deadline: far deadline
// gives the full timeout, near gives the remainder, past gives zero.
func TestGroupFlushDelay(t *testing.T) {
	timeout := 10 * time.Millisecond
	if d := groupFlushDelay(timeout, time.Now().Add(time.Hour)); d != timeout {
		t.Fatalf("far deadline: %v, want %v", d, timeout)
	}
	if d := groupFlushDelay(timeout, time.Now().Add(4*time.Millisecond)); d > 4*time.Millisecond {
		t.Fatalf("near deadline: %v, want <= 4ms", d)
	}
	if d := groupFlushDelay(timeout, time.Now().Add(-time.Second)); d != 0 {
		t.Fatalf("past deadline: %v, want 0", d)
	}
}

// Reaching the entry cap must flush synchronously instead of buffering a
// 65th file (a sender spamming grouped media would otherwise grow the
// buffer without bound while resetting the timer forever).
func TestWatchMediaGroupBufferCapForcesFlush(t *testing.T) {
	mgr := &watchMediaGroupHandler{
		groups:    make(map[int64]map[uint][]tfile.TGFileMessage),
		timers:    make(map[int64]map[uint]*time.Timer),
		callbacks: make(map[int64]map[uint]func([]tfile.TGFileMessage)),
		deadlines: make(map[int64]map[uint]time.Time),
	}
	flushed := make(chan int, 2)
	for i := 0; i < watchMediaGroupMaxFiles; i++ {
		mgr.addFile(2001, 9, groupFileWithID(t, "", fmt.Sprintf("cap-%d.jpg", i), 1, 100+i), time.Minute, func(files []tfile.TGFileMessage) {
			flushed <- len(files)
		})
	}
	select {
	case n := <-flushed:
		if n != watchMediaGroupMaxFiles {
			t.Fatalf("cap flush = %d files, want %d", n, watchMediaGroupMaxFiles)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cap reached but no flush fired")
	}
}

// The clamp in action at the intake level: a buffered (unresolved) album
// member pins the cursor, so a LATER resolved single must not leap the
// cursor past it — the monotonic never-lowering cursor would otherwise
// shadow the album forever.
func TestWatchIntakeClampBlocksLeapOverUnresolved(t *testing.T) {
	setupWatchDB(t)
	ctx := context.Background()
	user := database.User{ChatID: 1}
	if err := database.DB().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	watch := database.WatchChat{UserID: user.ID, ChatID: 100}
	if err := database.DB().Create(&watch).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { watchMediaGroupMgr.FlushChat(100) })
	botCtx := ext.NewContext(ctx, nil, nil, nil, nil, nil, false)

	// An unresolved message at 100 (enqueue failure or buffered album
	// member — both land in the pending set via the intake defer).
	watchPendingFor(watch.ID).add(100)
	// Resolved single at 105 (deterministic skip: no default storage).
	processMediaEventForWatch(botCtx, userclient.MediaMessageEvent{
		Ctx: botCtx, ChatID: 100, MessageID: 105,
		File: groupFileWithID(t, "", "single.jpg", 0, 105),
	}, &watch, watchIntakeOpts{live: true})

	cur, ok, err := database.CurrentWatchCursor(ctx, watch.ID)
	if err != nil || !ok || cur != 99 {
		t.Fatalf("cursor = %d ok=%v err=%v, want 99/true/nil (clamped below the pinned album member)", cur, ok, err)
	}
}
