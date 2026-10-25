package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// descending builds a getHistory-style batch: ids listed newest-first.
func descending(ids ...int) []*tg.Message {
	msgs := make([]*tg.Message, 0, len(ids))
	for _, id := range ids {
		msgs = append(msgs, &tg.Message{ID: id})
	}
	return msgs
}

// fakeHistory serves pages from a full DESCENDING id list, anchored like the
// real API: the page holds messages with id > offsetID (and id > minID),
// newest first, up to pageSize.
func fakeHistory(all []int, pageSize int, minID int, calls *int) historyFetchPage {
	return func(offsetID int) ([]*tg.Message, error) {
		*calls++
		if offsetID < 0 {
			offsetID = 0
		}
		boundary := offsetID
		if minID > boundary {
			boundary = minID
		}
		page := make([]int, 0, pageSize)
		for _, id := range all {
			if id <= boundary {
				continue
			}
			page = append(page, id)
			if len(page) == pageSize {
				break
			}
		}
		// reverse → descending (newest first)
		for i, j := 0, len(page)-1; i < j; i, j = i+1, j-1 {
			page[i], page[j] = page[j], page[i]
		}
		return descending(page...), nil
	}
}

func ids(msgs []*tg.Message) []int {
	out := make([]int, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

func TestPlanWatchCatchUpPages(t *testing.T) {
	all := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	t.Run("walks ascending from cursor to present", func(t *testing.T) {
		calls := 0
		fetch := fakeHistory(all, 4, 0, &calls)
		msgs, capped, err := planPagesWithSize(fetch, 3, 500, 4)
		if err != nil {
			t.Fatal(err)
		}
		if capped {
			t.Error("must not be capped")
		}
		if got := ids(msgs); fmt.Sprint(got) != "[4 5 6 7 8 9 10]" {
			t.Errorf("ids = %v, want ascending [4 5 6 7 8 9 10]", got)
		}
	})
	t.Run("gaps across page boundaries are kept", func(t *testing.T) {
		sparse := []int{1, 2, 5, 9, 20}
		calls := 0
		fetch := fakeHistory(sparse, 2, 0, &calls)
		msgs, capped, err := planPagesWithSize(fetch, 0, 500, 2)
		if err != nil {
			t.Fatal(err)
		}
		if capped || len(msgs) != len(sparse) {
			t.Fatalf("got %v capped=%v", ids(msgs), capped)
		}
	})
	t.Run("cap stops the walk", func(t *testing.T) {
		calls := 0
		fetch := fakeHistory(all, 3, 0, &calls)
		msgs, capped, err := planPagesWithSize(fetch, 0, 5, 3)
		if err != nil {
			t.Fatal(err)
		}
		if !capped {
			t.Error("cap hit must be reported")
		}
		if len(msgs) != 5 {
			t.Fatalf("len = %d, want 5", len(msgs))
		}
		if got := ids(msgs); fmt.Sprint(got) != "[1 2 3 4 5]" {
			t.Errorf("ids = %v, want the OLDEST five (backfill is forward)", got)
		}
	})
	t.Run("album split across pages stays contiguous", func(t *testing.T) {
		// Album 7,6,5 split: page 1 returns 10..6, page 2 returns 5..1.
		calls := 0
		fetch := fakeHistory(all, 5, 0, &calls)
		msgs, _, err := planPagesWithSize(fetch, 4, 500, 5)
		if err != nil {
			t.Fatal(err)
		}
		// cursor=4 → [5 6 7 8 9 10] ascending, album members adjacent
		if got := ids(msgs); fmt.Sprint(got) != "[5 6 7 8 9 10]" {
			t.Errorf("ids = %v", got)
		}
	})
	t.Run("straddled boundary: nothing at or below the cursor returns", func(t *testing.T) {
		calls := 0
		fetch := fakeHistory(all, 4, 0, &calls)
		msgs, _, err := planPagesWithSize(fetch, 10, 500, 4)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 0 {
			t.Errorf("cursor at newest must fetch nothing, got %v", ids(msgs))
		}
	})
	t.Run("short batch ends the walk", func(t *testing.T) {
		calls := 0
		fetch := fakeHistory(all, 100, 0, &calls)
		_, _, err := planPagesWithSize(fetch, 0, 500, 100)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 (single short batch)", calls)
		}
	})
	t.Run("fetch error surfaces with collected messages", func(t *testing.T) {
		calls := 0
		underlying := fakeHistory(all, 2, 0, &calls)
		fetch := func(offsetID int) ([]*tg.Message, error) {
			msgs, err := underlying(offsetID)
			if calls >= 2 {
				return nil, errors.New("boom")
			}
			return msgs, err
		}
		msgs, capped, err := planPagesWithSize(fetch, 0, 500, 2)
		if err == nil {
			t.Fatal("error must surface")
		}
		if capped {
			t.Error("an abort is not a cap")
		}
		if len(msgs) != 2 {
			t.Errorf("collected = %v, want the first page kept", ids(msgs))
		}
	})
	t.Run("budget abort is honored", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		// A full first page forces a second fetch, which the aborted
		// budget rejects.
		underlying := fakeHistory(all, 2, 0, &calls)
		fetch := func(offsetID int) ([]*tg.Message, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if calls == 0 {
				cancel() // budget expires after the first page
			}
			return underlying(offsetID)
		}
		msgs, capped, err := planPagesWithSize(fetch, 0, 500, 2)
		if err == nil {
			t.Fatal("abort must surface")
		}
		if capped || len(msgs) == 0 {
			t.Errorf("collected before abort must be kept: %v capped=%v", ids(msgs), capped)
		}
	})
}

// The guarded first-boot init must never lower an existing cursor: the
// init write goes through the same TouchWatchCursor guard as every advance.
func TestTouchWatchCursorGuard(t *testing.T) {
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
	if err := database.TouchWatchCursor(ctx, watch.ID, 500); err != nil {
		t.Fatal(err)
	}
	// A stale write (older message observed out of order, or a concurrent
	// init) must not lower the cursor.
	if err := database.TouchWatchCursor(ctx, watch.ID, 300); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetWatchChatsByChatID(ctx, 100)
	if err != nil || len(got) != 1 {
		t.Fatalf("reload: %v", err)
	}
	if got[0].LastMessageID == nil || *got[0].LastMessageID != 500 {
		t.Fatalf("cursor = %v, want 500 (guarded, never lowered)", got[0].LastMessageID)
	}
	// The same ID is a no-op, not an error.
	if err := database.TouchWatchCursor(ctx, watch.ID, 500); err != nil {
		t.Fatal(err)
	}
}

// setupWatchDB provisions a temp config + SQLite database for cursor tests.
func setupWatchDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[db]\npath = '" + filepath.Join(dir, "watch_test.db") + "'\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := config.Init(context.Background(), cfgPath); err != nil {
		t.Fatalf("config init: %v", err)
	}
	database.Init(context.Background())
	t.Cleanup(func() {
		_ = database.CloseDB()
	})
}

// The album buffer must never hold the same message twice: the live
// listener and the catch-up backfill feed it concurrently, and a message
// duplicated inside ONE batch task bypasses every dedup layer (the ledger
// row only exists after the first copy uploads).
func TestWatchMediaGroupBufferDedupsMessageIDs(t *testing.T) {
	done := make(chan []tfile.TGFileMessage, 1)
	file := buildGroupFile(t, "", "dup.jpg", 42)
	watchMediaGroupMgr.addFile(1001, 7, file, 50*time.Millisecond, func(files []tfile.TGFileMessage) {
		done <- files
	})
	// The same message re-fed by the overlapping catch-up path.
	watchMediaGroupMgr.addFile(1001, 7, file, 50*time.Millisecond, func(files []tfile.TGFileMessage) {
		t.Error("second registration must not restart or own the flush")
	})
	select {
	case files := <-done:
		if len(files) != 1 {
			t.Fatalf("flushed %d files, want 1 (duplicate message rejected)", len(files))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flush did not fire")
	}
	// A different message ID in the same chat still buffers and flushes.
	flushed := make(chan int, 1)
	watchMediaGroupMgr.addFile(1001, 7, groupFileWithID(t, "", "other.jpg", 42, 556), 50*time.Millisecond, func(files []tfile.TGFileMessage) {
		flushed <- len(files)
	})
	watchMediaGroupMgr.FlushChat(1001)
	select {
	case n := <-flushed:
		if n != 1 {
			t.Fatalf("distinct message flushed %d files, want 1", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FlushChat did not flush the distinct message")
	}
}

// groupFileWithID is buildGroupFile with an explicit message ID.
func groupFileWithID(t *testing.T, caption, fileName string, groupID, msgID int) tfile.TGFileMessage {
	t.Helper()
	doc := &tg.Document{
		ID:         1,
		Size:       10,
		MimeType:   "video/mp4",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: fileName}},
	}
	msg := &tg.Message{Message: caption, ID: msgID}
	msg.Flags.Set(9)
	msg.Media = &tg.MessageMediaDocument{Document: doc}
	msg.SetGroupedID(int64(groupID))
	file, err := tfile.FromMediaMessage(msg.Media, nil, msg)
	if err != nil {
		t.Fatalf("FromMediaMessage: %v", err)
	}
	return file
}

// CurrentWatchCursor reads the durable cursor and distinguishes unset.
func TestCurrentWatchCursor(t *testing.T) {
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
	if _, ok, err := database.CurrentWatchCursor(ctx, watch.ID); err != nil || ok {
		t.Fatalf("unset cursor: ok=%v err=%v, want false/nil", ok, err)
	}
	if err := database.TouchWatchCursor(ctx, watch.ID, 4242); err != nil {
		t.Fatal(err)
	}
	cur, ok, err := database.CurrentWatchCursor(ctx, watch.ID)
	if err != nil || !ok || cur != 4242 {
		t.Fatalf("cursor = %d ok=%v err=%v, want 4242/true/nil", cur, ok, err)
	}
}
