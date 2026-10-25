package database_test

import (
	"context"
	"testing"

	"github.com/krau/SaveAny-Bot/database"
)

func seedHistory(t *testing.T) context.Context {
	t.Helper()
	setupDedupEnv(t)
	ctx := context.Background()
	rows := []struct {
		stor, fid, path string
		size            int64
		chatID          int64
		msgID           int
	}{
		{"local1", "doc:1", "videos/a.mp4", 100, -100111, 501},
		{"local1", "doc:2", "videos/b.mp4", 200, -100111, 502},
		{"TG Hehe", "doc:3", "clips/c.mp4", 300, -100222, 601},
		{"local1", "", "weird_100%.bin", 50, 0, 0},
	}
	for _, r := range rows {
		if err := database.RecordDedupWithSource(ctx, 1, r.stor, r.fid, "hash-"+r.fid, r.size, r.path, r.chatID, r.msgID); err != nil {
			t.Fatalf("seed %s: %v", r.path, err)
		}
	}
	return ctx
}

func TestListSavedFiles(t *testing.T) {
	ctx := seedHistory(t)

	rows, total, err := database.ListSavedFiles(ctx, 1, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(rows) != 4 {
		t.Fatalf("total=%d len=%d, want 4/4", total, len(rows))
	}
	// Newest first.
	if rows[0].StoragePath != "weird_100%.bin" {
		t.Errorf("newest row = %q", rows[0].StoragePath)
	}
	// Source columns round-trip; legacy/unknown rows read 0.
	if rows[1].ChatID != -100222 || rows[1].MessageID != 601 {
		t.Errorf("source = %d/%d, want -100222/601", rows[1].ChatID, rows[1].MessageID)
	}

	// Another user sees nothing.
	rows, total, err = database.ListSavedFiles(ctx, 2, "", 1)
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("cross-user leak: %d/%d/%v", len(rows), total, err)
	}
}

func TestListSavedFilesSearch(t *testing.T) {
	ctx := seedHistory(t)

	rows, total, err := database.ListSavedFiles(ctx, 1, "videos", 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("search total = %d, want 2", total)
	}

	// % and _ in the query must match literally, not as wildcards: "100%"
	// matches only the literal name, not everything.
	rows, total, err = database.ListSavedFiles(ctx, 1, "100%", 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].StoragePath != "weird_100%.bin" {
		t.Fatalf("escaped search: total=%d rows=%d", total, len(rows))
	}
	// A bare underscore is literal too: it matches only the one path that
	// actually contains an underscore (a wildcard _ would match all 4).
	_, total, _ = database.ListSavedFiles(ctx, 1, "_", 1)
	if total != 1 {
		t.Errorf("underscore matched %d rows, want the single literal underscore row", total)
	}

	// Search also matches the storage name.
	_, total, _ = database.ListSavedFiles(ctx, 1, "Hehe", 1)
	if total != 1 {
		t.Errorf("storage-name search total = %d, want 1", total)
	}
}

func TestGetSavedFileStats(t *testing.T) {
	ctx := seedHistory(t)

	stats, err := database.GetSavedFileStats(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalCount != 4 || stats.TotalSize != 650 {
		t.Fatalf("totals = %d/%d, want 4/650", stats.TotalCount, stats.TotalSize)
	}
	if len(stats.ByStorage) != 2 {
		t.Fatalf("by-storage groups = %d, want 2", len(stats.ByStorage))
	}
	// Ordered by count DESC: local1 (3) before TG Hehe (1).
	if stats.ByStorage[0].StorageName != "local1" || stats.ByStorage[0].Count != 3 || stats.ByStorage[0].Size != 350 {
		t.Errorf("top storage = %+v", stats.ByStorage[0])
	}
	if len(stats.ByDay) == 0 {
		t.Error("by-day aggregation must include today's rows")
	}
	var dayCount int64
	for _, d := range stats.ByDay {
		dayCount += d.Count
	}
	if dayCount != 4 {
		t.Errorf("by-day counts sum = %d, want 4", dayCount)
	}
}
