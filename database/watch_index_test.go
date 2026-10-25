package database_test

import (
	"context"
	"testing"

	"github.com/krau/SaveAny-Bot/database"
)

func watchCursor(v int64) *int64 { return &v }

func seedWatchRow(t *testing.T, userID uint, chatID int64, cursor *int64) uint {
	t.Helper()
	row := database.WatchChat{UserID: userID, ChatID: chatID, LastMessageID: cursor}
	if err := database.DB().Create(&row).Error; err != nil {
		t.Fatalf("seed watch row (user %d, chat %d): %v", userID, chatID, err)
	}
	return row.ID
}

func liveWatchRows(t *testing.T, userID uint, chatID int64) []database.WatchChat {
	t.Helper()
	var rows []database.WatchChat
	if err := database.DB().Where("user_id = ? AND chat_id = ?", userID, chatID).Find(&rows).Error; err != nil {
		t.Fatalf("load watch rows (user %d, chat %d): %v", userID, chatID, err)
	}
	return rows
}

// The upgrade path behind the (user_id, chat_id) invariant: legacy duplicate
// rows are swept (highest cursor wins, ties keep the newest), the partial
// unique index is created, live duplicates become impossible, and soft-deleted
// rows stay non-blocking.
func TestEnsureWatchChatUniqueIndex(t *testing.T) {
	setupDedupEnv(t)
	ctx := context.Background()
	// Recreate the legacy dirty state: Init already ensured the index.
	if err := database.DB().Exec("DROP INDEX IF EXISTS idx_watch_chats_user_chat_active").Error; err != nil {
		t.Fatalf("drop index: %v", err)
	}

	user := database.User{ChatID: 1}
	if err := database.DB().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	// Pair A: NULL cursor loses to 300, 300 loses to 500.
	seedWatchRow(t, user.ID, 100, nil)
	seedWatchRow(t, user.ID, 100, watchCursor(300))
	pairABest := seedWatchRow(t, user.ID, 100, watchCursor(500))
	// Pair B: equal cursors, the newer row survives.
	seedWatchRow(t, user.ID, 200, watchCursor(9))
	pairBNewest := seedWatchRow(t, user.ID, 200, watchCursor(9))

	removed, err := database.DedupWatchChats(ctx)
	if err != nil || removed != 3 { // pair A: 2 losers, pair B: 1 loser
		t.Fatalf("DedupWatchChats removed=%d err=%v, want 3/nil", removed, err)
	}
	if err := database.EnsureWatchChatUniqueIndex(ctx); err != nil {
		t.Fatalf("ensure index: %v", err)
	}

	rowsA := liveWatchRows(t, user.ID, 100)
	if len(rowsA) != 1 || rowsA[0].ID != pairABest || *rowsA[0].LastMessageID != 500 {
		t.Fatalf("pair A survivor = %+v, want id %d cursor 500", rowsA, pairABest)
	}
	rowsB := liveWatchRows(t, user.ID, 200)
	if len(rowsB) != 1 || rowsB[0].ID != pairBNewest {
		t.Fatalf("pair B survivor = %+v, want the newest id %d", rowsB, pairBNewest)
	}

	// A live duplicate is now rejected by the database itself.
	if err := database.DB().Create(&database.WatchChat{UserID: user.ID, ChatID: 100}).Error; err == nil {
		t.Fatal("duplicate live watch row must violate the unique index")
	}

	// Soft-deleted rows do not block a fresh watch of the same chat.
	pairC := seedWatchRow(t, user.ID, 300, nil)
	if err := database.DB().Delete(&database.WatchChat{}, pairC).Error; err != nil {
		t.Fatalf("soft-delete pair C: %v", err)
	}
	if err := database.DB().Create(&database.WatchChat{UserID: user.ID, ChatID: 300}).Error; err != nil {
		t.Fatalf("re-watch after soft delete must succeed (partial index): %v", err)
	}

	// Idempotent on the next boot.
	if _, err := database.DedupWatchChats(ctx); err != nil {
		t.Fatalf("second dedup pass: %v", err)
	}
	if err := database.EnsureWatchChatUniqueIndex(ctx); err != nil {
		t.Fatalf("second ensure pass: %v", err)
	}
	if n := len(liveWatchRows(t, user.ID, 100)) + len(liveWatchRows(t, user.ID, 200)); n != 2 {
		t.Fatalf("second pass changed survivors: %d live rows in pairs A+B", n)
	}
}
