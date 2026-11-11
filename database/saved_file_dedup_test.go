package database

import (
	"context"
	"testing"
)

func setupDedupDB(t *testing.T) context.Context {
	t.Helper()
	restore := openTestDB(t)
	t.Cleanup(restore)
	ctx := context.Background()
	if err := migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return ctx
}

func TestDedupFileIDCheck(t *testing.T) {
	ctx := setupDedupDB(t)
	if DedupHasFileID(ctx, 1, 2, "doc:42") {
		t.Fatal("empty ledger must not report exists")
	}
	if err := RecordDedup(ctx, 1, 2, "doc:42", "", 100, "x.mp4"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !DedupHasFileID(ctx, 1, 2, "doc:42") {
		t.Fatal("recorded file-id must exist")
	}
	if DedupHasFileID(ctx, 2, 2, "doc:42") || DedupHasFileID(ctx, 1, 3, "doc:42") {
		t.Fatal("different user or storage scope must not match")
	}
	// Re-recording is idempotent.
	if err := RecordDedup(ctx, 1, 2, "doc:42", "", 100, "y.mp4"); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	var count int64
	if err := db.WithContext(ctx).Model(&SavedFileDedup{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("rows = %d (err=%v), want 1 (OnConflict DoNothing)", count, err)
	}
	// Empty file-id never matches or records.
	if DedupHasFileID(ctx, 1, 2, "") {
		t.Fatal("empty file-id must short-circuit")
	}
	if err := RecordDedup(ctx, 1, 2, "", "", 5, "z.bin"); err == nil {
		var count int64
		db.WithContext(ctx).Model(&SavedFileDedup{}).Count(&count)
		if count != 1 {
			t.Fatalf("neither-id-nor-hash record must be a no-op, rows=%d", count)
		}
	}
	// Unresolved storage must never record.
	if err := RecordDedup(ctx, 1, 0, "doc:43", "", 5, "w.mp4"); err != nil {
		t.Fatalf("storage 0 record: %v", err)
	}
	if DedupHasFileID(ctx, 1, 0, "doc:43") {
		t.Fatal("storage 0 must never record dedup rows")
	}
}

func TestDedupNoUserDisabled(t *testing.T) {
	ctx := setupDedupDB(t)
	if dedupEnabled(ctx, 0) {
		t.Fatal("userID 0 must disable dedup")
	}
	if err := RecordDedup(ctx, 0, 2, "doc:9", "", 1, "p"); err != nil {
		t.Fatalf("no-user record must be a no-op not an error: %v", err)
	}
	var count int64
	db.WithContext(ctx).Model(&SavedFileDedup{}).Count(&count)
	if count != 0 {
		t.Fatalf("no-user record must write nothing, rows=%d", count)
	}
}

func TestDedupHashCheck(t *testing.T) {
	ctx := setupDedupDB(t)
	if err := RecordDedup(ctx, 1, 2, "", "cafe1234", 10, "a.bin"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !DedupHasHash(ctx, 1, 2, "cafe1234") {
		t.Fatal("recorded hash must exist")
	}
	if DedupHasHash(ctx, 1, 2, "") || DedupHasHash(ctx, 1, 2, "beef") {
		t.Fatal("empty/unknown hash must not match")
	}
	// Purge scoped per user+storage.
	if err := RecordDedup(ctx, 9, 2, "", "cafe1234", 10, "b.bin"); err != nil {
		t.Fatalf("record other user: %v", err)
	}
	n, err := PurgeDedupByStorage(ctx, 1, 2)
	if err != nil || n != 1 {
		t.Fatalf("purge = %d (err=%v), want 1", n, err)
	}
	if DedupHasHash(ctx, 1, 2, "cafe1234") {
		t.Fatal("purged hash must be gone")
	}
	if !DedupHasHash(ctx, 9, 2, "cafe1234") {
		t.Fatal("purge must not touch other users' rows")
	}
}
