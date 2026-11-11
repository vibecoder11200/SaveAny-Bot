//go:build e2e

package telegrame2e

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/client/bot/handlers"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/mygotg/ext"
)

// localFileCount counts regular files currently saved under the local
// storage root.
func localFileCount(t *testing.T) int {
	t.Helper()
	n := 0
	if err := filepath.WalkDir(harness.LocalPath, func(_ string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			n++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk local storage: %v", err)
	}
	return n
}

// TestWatchCatchUp exercises the boot backfill end to end against the fake
// server: the first boot initializes the cursor without backfilling, a
// later boot enqueues exactly the messages missed while offline, and a
// further boot re-fetches nothing.
func TestWatchCatchUp(t *testing.T) {
	harness.Server.ResetRecords()
	if err := harness.ClearLocal(); err != nil {
		t.Fatalf("clear local storage: %v", err)
	}
	ctx := context.Background()
	user, err := database.GetUserByChatID(ctx, localUserID)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	watch := database.WatchChat{UserID: user.ID, ChatID: channelID}
	if err := database.DB().Create(&watch).Error; err != nil {
		t.Fatalf("seed watch row: %v", err)
	}
	t.Cleanup(func() {
		database.DB().Unscoped().Delete(&database.WatchChat{}, watch.ID)
	})

	peer := &tg.PeerChannel{ChannelID: channelID}

	// First boot: the watch has no cursor, so catch-up records the newest
	// message as the start point and backfills NOTHING.
	before := harness.Server.AddDocumentMessage(peer, peer, "watch-before.txt", []byte("before the watch\n"))
	handlers.RunWatchCatchUp(harness.Ext, func() *ext.Context { return harness.Ext })

	cur, ok, err := database.CurrentWatchCursor(ctx, watch.ID)
	if err != nil || !ok || cur != int64(before.ID) {
		t.Fatalf("cursor after first boot = %d ok=%v err=%v, want %d/true/nil (init, no backfill)", cur, ok, err, before.ID)
	}
	if n := localFileCount(t); n != 0 {
		t.Fatalf("first boot enqueued %d file(s), want 0", n)
	}

	// Two messages arrive while the instance is down.
	harness.Server.AddDocumentMessage(peer, peer, "watch-missed-a.txt", []byte("missed A\n"))
	missedB := harness.Server.AddDocumentMessage(peer, peer, "watch-missed-b.txt", []byte("missed B\n"))

	// The restart catch-up enqueues exactly the missed messages.
	handlers.RunWatchCatchUp(harness.Ext, func() *ext.Context { return harness.Ext })

	gotA, err := harness.WaitFile("watch-missed-a.txt", 30*time.Second)
	if err != nil {
		t.Fatalf("wait for missed A: %v", err)
	}
	gotB, err := harness.WaitFile("watch-missed-b.txt", 30*time.Second)
	if err != nil {
		t.Fatalf("wait for missed B: %v", err)
	}
	if !bytes.Equal(gotA, []byte("missed A\n")) || !bytes.Equal(gotB, []byte("missed B\n")) {
		t.Fatalf("saved contents = %q / %q", gotA, gotB)
	}

	cur, _, err = database.CurrentWatchCursor(ctx, watch.ID)
	if err != nil || cur != int64(missedB.ID) {
		t.Fatalf("cursor after backfill = %d err=%v, want %d", cur, err, missedB.ID)
	}

	// Exactly once: remove the saved files and re-run catch-up — the
	// cursor already covers every message, so nothing may reappear.
	if err := harness.ClearLocal(); err != nil {
		t.Fatalf("clear local storage: %v", err)
	}
	handlers.RunWatchCatchUp(harness.Ext, func() *ext.Context { return harness.Ext })
	time.Sleep(500 * time.Millisecond)
	if n := localFileCount(t); n != 0 {
		t.Fatalf("second catch-up run saved %d file(s), want 0 (exactly-once)", n)
	}
}
