package database

import (
	"testing"

	"gorm.io/gorm"
)

func TestListSavedFilesPaginationAndSearch(t *testing.T) {
	ctx := setupDedupDB(t)
	// Fixture storages so the name join resolves.
	for _, rec := range []StorageRecord{
		{Model: gorm.Model{ID: 2}, Name: "videos", Type: "local", Enable: true, ConfigJSON: "{}"},
		{Model: gorm.Model{ID: 3}, Name: "music", Type: "local", Enable: true, ConfigJSON: "{}"},
	} {
		if err := db.WithContext(ctx).Create(&rec).Error; err != nil {
			t.Fatalf("seed storage: %v", err)
		}
	}
	// Two storages, scoped to user 1.
	for i := 0; i < 3; i++ {
		if err := RecordDedup(ctx, 1, 2, "doc:a"+string(rune('0'+i)), "", 10, "videos/movie"+string(rune('0'+i))+".mp4"); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if err := RecordDedup(ctx, 1, 3, "doc:b1", "", 20, "music/song.mp3"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := RecordDedup(ctx, 2, 2, "doc:other", "", 30, "videos/private.mp4"); err != nil {
		t.Fatalf("record: %v", err)
	}

	rows, total, err := ListSavedFiles(ctx, 1, "", 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 4 || len(rows) != 4 {
		t.Fatalf("rows=%d total=%d, want 4/4", len(rows), total)
	}
	// User scoping: user 2 sees only their row.
	_, total2, err := ListSavedFiles(ctx, 2, "", 1)
	if err != nil || total2 != 1 {
		t.Fatalf("user 2 total=%d (err=%v), want 1", total2, err)
	}
	// Storage name resolves through the join.
	found := false
	for _, r := range rows {
		if r.StorageName == "" {
			t.Fatalf("storage %d name must resolve via the join", r.StorageID)
		}
		if r.StorageID == 3 {
			found = true
		}
	}
	if !found {
		t.Fatal("storage 3 row missing")
	}
	// Search matches path substring literally.
	rows, total, err = ListSavedFiles(ctx, 1, "movie", 1)
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatalf("search total=%d rows=%d (err=%v), want 3/3", total, len(rows), err)
	}
	// LIKE wildcards in the query stay literal.
	rows, total, err = ListSavedFiles(ctx, 1, "%", 1)
	if err != nil || total != 0 {
		t.Fatalf("literal %% must match nothing, got total=%d (err=%v)", total, err)
	}
	// Page 2 is out of range: empty rows but a positive total.
	rows, total, err = ListSavedFiles(ctx, 1, "", 2)
	if err != nil || total != 4 || len(rows) != 0 {
		t.Fatalf("page 2: rows=%d total=%d (err=%v), want 0/4", len(rows), total, err)
	}
}

func TestSavedFileStats(t *testing.T) {
	ctx := setupDedupDB(t)
	if err := RecordDedup(ctx, 1, 2, "doc:x", "", 100, "a.bin"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := RecordDedup(ctx, 1, 3, "doc:y", "", 50, "b.bin"); err != nil {
		t.Fatalf("record: %v", err)
	}
	stats, err := GetSavedFileStats(ctx, 1)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalCount != 2 || stats.TotalSize != 150 {
		t.Fatalf("totals = %d/%d, want 2/150", stats.TotalCount, stats.TotalSize)
	}
	if len(stats.ByStorage) != 2 {
		t.Fatalf("by-storage rows = %d, want 2", len(stats.ByStorage))
	}
	if stats.ByStorage[0].Count < stats.ByStorage[1].Count {
		t.Fatal("by-storage must be ordered by count desc")
	}
}
