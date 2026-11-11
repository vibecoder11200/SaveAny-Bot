package database

import (
	"context"
	"testing"
)

func TestTelegramSavedLedger(t *testing.T) {
	restore := openTestDB(t)
	defer restore()
	ctx := context.Background()
	if err := migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if TelegramSavedExists(ctx, 5, "a/b.mp4") {
		t.Fatal("empty ledger must not report exists")
	}
	if err := RecordTelegramSaved(ctx, 5, "a/b.mp4", "", "c/d.mp4"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !TelegramSavedExists(ctx, 5, "a/b.mp4") || !TelegramSavedExists(ctx, 5, "c/d.mp4") {
		t.Fatal("recorded paths must exist")
	}
	if TelegramSavedExists(ctx, 6, "a/b.mp4") {
		t.Fatal("different storage id must not match")
	}
	if TelegramSavedExists(ctx, 5, "a/c.mp4") {
		t.Fatal("different path must not match")
	}
	if TelegramSavedExists(ctx, 5, "") {
		t.Fatal("empty path must short-circuit to false")
	}
	// Re-recording the same row is a no-op, not an error.
	if err := RecordTelegramSaved(ctx, 5, "a/b.mp4"); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	var count int64
	if err := db.WithContext(ctx).Model(&TelegramSavedFile{}).
		Where("storage_id = ?", 5).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("ledger rows = %d (err=%v), want 2", count, err)
	}

	n, err := PurgeTelegramSavedByStorage(ctx, 5)
	if err != nil || n != 2 {
		t.Fatalf("purge = %d rows (err=%v), want 2", n, err)
	}
	if TelegramSavedExists(ctx, 5, "a/b.mp4") {
		t.Fatal("purged path must not exist")
	}
}
