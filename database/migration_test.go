package database

import (
	"context"
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

// openTestDB opens a throwaway SQLite database and points the package-global
// handle at it. The returned close func restores the previous handle.
//
// The default ncruces WASM driver cannot open absolute Windows paths
// (pre-existing upstream defect, unaffected by the fork); those tests run
// with -tags sqlite_glebarez, so an open failure here skips instead of fail.
func openTestDB(t *testing.T) func() {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	prev := db
	g, err := gorm.Open(GetDialect(path), &gorm.Config{})
	if err != nil {
		t.Skipf("sqlite driver cannot open a database on this platform (pre-existing upstream defect); run with -tags sqlite_glebarez: %v", err)
	}
	db = g
	return func() {
		sqlDB, err := g.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		db = prev
	}
}

// TestMigrationV1ToV2 upgrades a database carrying exactly the upstream v1
// schema (migration 1 only) to the fork schema, then checks every fork
// column, table and index exists. This is the shape a production v1 install
// has on disk before the fork's first boot.
func TestMigrationV1ToV2(t *testing.T) {
	restore := openTestDB(t)
	defer restore()
	ctx := context.Background()

	// Baseline: only migration 1.
	if err := db.WithContext(ctx).Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at DATETIME NOT NULL
)`).Error; err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := baselineSchema(tx); err != nil {
			return err
		}
		return tx.Exec(
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			1, "2026-01-01 00:00:00",
		).Error
	}); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}

	// Upgrade to the fork schema.
	if err := migrate(ctx); err != nil {
		t.Fatalf("migrate to v2: %v", err)
	}

	assertColumn := func(table, column string) {
		t.Helper()
		var count int64
		err := db.WithContext(ctx).Raw(
			"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column,
		).Scan(&count).Error
		if err != nil || count != 1 {
			t.Errorf("column %s.%s missing after upgrade (err=%v, count=%d)", table, column, err, count)
		}
	}
	assertColumn("users", "media_filter")
	assertColumn("watch_chats", "media_type")
	assertColumn("watch_chats", "storage_id")
	assertColumn("watch_chats", "dir_path")
	assertColumn("watch_chats", "last_message_id")
	for _, table := range []string{"saved_file_dedups", "telegram_saved_files"} {
		var count int64
		if err := db.WithContext(ctx).Raw(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?", table,
		).Scan(&count).Error; err != nil || count != 1 {
			t.Errorf("table %s missing after upgrade (err=%v)", table, err)
		}
	}
	for _, idx := range []string{"idx_dedup_fileid", "idx_dedup_hash", "idx_dedup_chat"} {
		var count int64
		if err := db.WithContext(ctx).Raw(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = ?", idx,
		).Scan(&count).Error; err != nil || count != 1 {
			t.Errorf("index %s missing after upgrade (err=%v)", idx, err)
		}
	}

	var version int
	if err := db.WithContext(ctx).Raw(
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version).Error; err != nil || version != CurrentSchemaVersion {
		t.Errorf("schema version after upgrade = %d (err=%v), want %d", version, err, CurrentSchemaVersion)
	}
}

// TestMigrationFreshDB runs the full migration list on an empty database:
// fresh installs must boot with the complete fork schema in one pass.
func TestMigrationFreshDB(t *testing.T) {
	restore := openTestDB(t)
	defer restore()
	ctx := context.Background()

	if err := migrate(ctx); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var version int
	if err := db.WithContext(ctx).Raw(
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version).Error; err != nil || version != CurrentSchemaVersion {
		t.Fatalf("schema version = %d (err=%v), want %d", version, err, CurrentSchemaVersion)
	}
}

// TestDedupPartialUniques proves the migration's partial unique indexes
// actually deduplicate: re-inserting the same (user, storage, file-id) or
// (user, storage, hash) row must not create a second row, while empty-string
// keys (non-TG files, resends) must coexist freely — the exact property
// OnConflict DoNothing relies on.
func TestDedupPartialUniques(t *testing.T) {
	restore := openTestDB(t)
	defer restore()
	ctx := context.Background()

	if err := migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	row := SavedFileDedup{
		UserID: 1, StorageID: 2, FileUniqueID: "doc:42", ContentHash: "abc",
		Size: 10, StoragePath: "x.mp4", ChatID: 100, MessageID: 7,
	}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		t.Fatalf("insert dedup row: %v", err)
	}
	dup := row
	dup.ID = 0
	dup.StoragePath = "y.mp4"
	if err := db.WithContext(ctx).Create(&dup).Error; err == nil {
		t.Fatal("re-insert with same file-id+hash must violate the partial unique indexes")
	}

	// Empty file ids must not collide with each other (resends, non-TG).
	emptyA := SavedFileDedup{UserID: 1, StorageID: 2, StoragePath: "a.bin"}
	emptyB := SavedFileDedup{UserID: 1, StorageID: 2, StoragePath: "b.bin"}
	if err := db.WithContext(ctx).Create(&emptyA).Error; err != nil {
		t.Fatalf("insert empty-key row A: %v", err)
	}
	if err := db.WithContext(ctx).Create(&emptyB).Error; err != nil {
		t.Fatalf("two empty-key rows must coexist: %v", err)
	}

	// Same file-id on a different user or storage is a different scope.
	otherUser := SavedFileDedup{UserID: 9, StorageID: 2, FileUniqueID: "doc:42", StoragePath: "z.mp4"}
	otherStorage := SavedFileDedup{UserID: 1, StorageID: 3, FileUniqueID: "doc:42", StoragePath: "z.mp4"}
	if err := db.WithContext(ctx).Create(&otherUser).Error; err != nil {
		t.Fatalf("same file-id under another user must be allowed: %v", err)
	}
	if err := db.WithContext(ctx).Create(&otherStorage).Error; err != nil {
		t.Fatalf("same file-id under another storage must be allowed: %v", err)
	}

	// telegram_saved_files keeps its plain unique (storage_id, storage_path).
	led := TelegramSavedFile{StorageID: 2, StoragePath: "x.mp4"}
	if err := db.WithContext(ctx).Create(&led).Error; err != nil {
		t.Fatalf("insert ledger row: %v", err)
	}
	ledDup := led
	ledDup.ID = 0
	if err := db.WithContext(ctx).Create(&ledDup).Error; err == nil {
		t.Fatal("re-insert of identical ledger row must violate the unique index")
	}
}
