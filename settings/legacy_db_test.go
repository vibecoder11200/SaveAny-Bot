package settings

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/krau/SaveAny-Bot/database"
)

// openV0Writable opens a SQLite file WRITABLE through the v1 driver stack —
// used only to BUILD test fixtures.
func openV0Writable(path string) (*gorm.DB, error) {
	return database.Open(path)
}

// v0FixtureDDL recreates the v0 schema with raw DDL — stable against v1
// model changes.
var v0FixtureDDL = []string{
	`CREATE TABLE users (id INTEGER PRIMARY KEY, chat_id INTEGER NOT NULL, silent BOOLEAN, default_storage TEXT, default_dir INTEGER, apply_rule BOOLEAN, watch_notify BOOLEAN, filename_strategy TEXT, filename_template TEXT, conflict_strategy TEXT, media_filter TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	`CREATE TABLE dirs (id INTEGER PRIMARY KEY, user_id INTEGER, storage_name TEXT, path TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	`CREATE TABLE rules (id INTEGER PRIMARY KEY, user_id INTEGER, type TEXT, data TEXT, storage_name TEXT, dir_path TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	`CREATE TABLE watch_chats (id INTEGER PRIMARY KEY, user_id INTEGER, chat_id INTEGER, filter TEXT, media_type TEXT, storage_name TEXT, dir_path TEXT, last_message_id INTEGER, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	`CREATE TABLE saved_file_dedups (id INTEGER PRIMARY KEY, user_id INTEGER, storage_name TEXT, file_unique_id TEXT, content_hash TEXT, size INTEGER, storage_path TEXT, chat_id INTEGER, message_id INTEGER, created_at DATETIME)`,
	`CREATE TABLE telegram_saved_files (id INTEGER PRIMARY KEY, storage_name TEXT, storage_path TEXT, created_at DATETIME)`,
	`CREATE TABLE pending_tasks (id TEXT PRIMARY KEY, user_id INTEGER, payload TEXT, attempts INTEGER, created_at DATETIME)`,
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`,
}

// createV0Fixture writes a fully populated v0 database and returns its path.
func createV0Fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v0.db")
	g, err := openV0Writable(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	for _, stmt := range v0FixtureDDL {
		if err := g.Exec(stmt).Error; err != nil {
			t.Fatalf("fixture DDL: %v (%s)", err, stmt)
		}
	}
	inserts := []string{
		// u1 (id 1, chat 100): full flags. u2 (id 2, chat 200): references
		// unresolvable storage "ghost". u3 (id 3, chat 300): soft-deleted.
		`INSERT INTO users (id, chat_id, silent, default_storage, default_dir, apply_rule, watch_notify, filename_strategy, filename_template, conflict_strategy, media_filter, created_at, updated_at) VALUES (1, 100, 1, 'local', 11, 1, 1, 'message', 'tpl-{filename}', 'rename', 'photo,video', '2024-01-01', '2024-01-01')`,
		`INSERT INTO users (id, chat_id, silent, default_storage, default_dir, apply_rule, watch_notify, filename_strategy, filename_template, conflict_strategy, media_filter, created_at, updated_at) VALUES (2, 200, 0, 'ghost', 0, 0, 0, '', '', '', '', '2024-01-01', '2024-01-01')`,
		`INSERT INTO users (id, chat_id, silent, default_storage, default_dir, apply_rule, watch_notify, filename_strategy, filename_template, conflict_strategy, media_filter, created_at, updated_at, deleted_at) VALUES (3, 300, 0, '', 0, 0, 0, '', '', '', '', '2024-01-01', '2024-01-01', '2024-02-01')`,
		`INSERT INTO dirs (id, user_id, storage_name, path) VALUES (11, 1, 'local', 'media/')`,
		`INSERT INTO dirs (id, user_id, storage_name, path) VALUES (12, 2, 'ghost', 'x/')`,
		`INSERT INTO rules (id, user_id, type, data, storage_name, dir_path) VALUES (21, 1, 'file_name_regex', '.*mp4', 'local', 'videos/')`,
		`INSERT INTO rules (id, user_id, type, data, storage_name, dir_path) VALUES (22, 2, 'file_name_regex', '.*mkv', 'ghost', 'k/')`,
		// Watch pair on (user 1, chat 9001): higher cursor 500 must win over
		// 100. Watch 34 is soft-deleted. Watch 32 carries an invalid regex.
		`INSERT INTO watch_chats (id, user_id, chat_id, filter, media_type, storage_name, dir_path, last_message_id) VALUES (31, 1, 9001, 'msgre:.*plana.*', 'photo,video', 'local', 'watched/', 500)`,
		`INSERT INTO watch_chats (id, user_id, chat_id, filter, media_type, storage_name, dir_path, last_message_id) VALUES (33, 1, 9001, 'msgre:older', '', '', '', 100)`,
		`INSERT INTO watch_chats (id, user_id, chat_id, filter, media_type, storage_name, dir_path, last_message_id) VALUES (32, 2, 9002, 'msgre:[', '', '', '', 10)`,
		`INSERT INTO watch_chats (id, user_id, chat_id, filter, media_type, storage_name, dir_path, last_message_id, deleted_at) VALUES (34, 1, 9003, 'msgre:dead', '', '', '', 5, '2024-03-01')`,
		// Dedup ledger keyed by CHAT ID (100); an orphan chat 999 row; a
		// ghost-storage row.
		`INSERT INTO saved_file_dedups (user_id, storage_name, file_unique_id, content_hash, size, storage_path, chat_id, message_id, created_at) VALUES (100, 'local', 'fid-1', '', 10, 'a.txt', 9001, 5, '2024-01-01')`,
		`INSERT INTO saved_file_dedups (user_id, storage_name, file_unique_id, content_hash, size, storage_path, chat_id, message_id, created_at) VALUES (999, 'local', 'fid-orphan', '', 10, 'orphan.txt', 0, 0, '2024-01-01')`,
		`INSERT INTO saved_file_dedups (user_id, storage_name, file_unique_id, content_hash, size, storage_path, chat_id, message_id, created_at) VALUES (100, 'ghost', 'fid-ghost', '', 10, 'g.txt', 0, 0, '2024-01-01')`,
		`INSERT INTO telegram_saved_files (storage_name, storage_path, created_at) VALUES ('local', 'tg/a.txt', '2024-01-01')`,
		`INSERT INTO telegram_saved_files (storage_name, storage_path, created_at) VALUES ('ghost', 'tg/b.txt', '2024-01-01')`,
		`INSERT INTO pending_tasks (id, user_id, payload, attempts, created_at) VALUES ('task1', 100, '{}', 0, '2024-01-01')`,
	}
	for _, stmt := range inserts {
		if err := g.Exec(stmt).Error; err != nil {
			t.Fatalf("fixture insert: %v (%s)", err, stmt)
		}
	}
	return path
}

// initV1DB provisions a fresh v1 database on a temp path. database.Init
// Fatalf's on an open failure, and the default ncruces WASM driver cannot
// open absolute Windows paths (pre-existing upstream defect): probe-open
// first and SKIP, not fail, on those platforms.
func initV1DB(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v1.db")
	if g, err := database.Open(path); err != nil {
		t.Skipf("sqlite driver cannot open a database on this platform; run with -tags sqlite_glebarez: %v", err)
	} else if sqlDB, dbErr := g.DB(); dbErr == nil {
		_ = sqlDB.Close()
	}
	database.Init(context.Background(), path)
	t.Cleanup(func() {
		_ = database.Close()
	})
}

// setupImportV1 provisions a fresh v1 database and seeds the TOML-style
// local storage row the import mapping resolves against.
func setupImportV1(t *testing.T) {
	t.Helper()
	initV1DB(t)
	ctx := context.Background()
	if err := database.DB().WithContext(ctx).Exec(
		`INSERT INTO storages (name, type, enable, config, config_version) VALUES ('local', 'local', 1, '{}', 1)`).Error; err != nil {
		t.Fatalf("seed local storage: %v", err)
	}
}

// importFixture runs the importer against a COPY of the fixture and returns
// the copy's path — the original stays untouched for the byte-compare test.
func importFixture(t *testing.T, src string) string {
	t.Helper()
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(copyPath, data, 0o644); err != nil {
		t.Fatalf("write copy: %v", err)
	}
	if err := ImportLegacyDB(context.Background(), copyPath); err != nil {
		t.Fatalf("import: %v", err)
	}
	return copyPath
}

func v1RowCount(t *testing.T, table string, where string, args ...any) int64 {
	t.Helper()
	var n int64
	q := database.DB().Table(table)
	if where != "" {
		q = q.Where(where, args...)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestImportLegacyDBHappyPath covers the full fixture: users (one matched,
// one created, soft-deleted skipped), dirs/rules/dedups/tg-saved with
// storage mapping, watch collision (higher cursor wins), invalid filter
// cleared, cursor preserved, marker written, and the not-migrated warning
// count.
func TestImportLegacyDBHappyPath(t *testing.T) {
	setupImportV1(t)
	src := createV0Fixture(t)
	importFixture(t, src)
	ctx := context.Background()

	// Users: u1/chat100 (existing), u2/chat200 (created, disabled), u3
	// (soft-deleted) must not appear.
	var u1, u2 database.User
	if err := database.DB().Where("chat_id = ?", 100).First(&u1).Error; err != nil {
		t.Fatalf("u1: %v", err)
	}
	if err := database.DB().Where("chat_id = ?", 200).First(&u2).Error; err != nil {
		t.Fatalf("u2: %v", err)
	}
	if u2.Enabled {
		t.Fatal("created v0-only user must NOT be enabled")
	}
	if n := v1RowCount(t, "users", "chat_id = ?", 300); n != 0 {
		t.Fatalf("soft-deleted user imported: %d rows", n)
	}
	if !u1.Silent || !u1.ApplyRule || !u1.WatchNotify || u1.FilenameStrategy != "message" || u1.FilenameTemplate != "tpl-{filename}" || u1.ConflictStrategy != "rename" || u1.MediaFilter != "photo,video" {
		t.Fatalf("u1 flags not mapped: %+v", u1)
	}
	// Default storage remapped by name; default dir 11 → the imported dir.
	if u1.DefaultStorageID == 0 {
		t.Fatal("u1 default storage not remapped")
	}
	if u1.DefaultDirID == 0 {
		t.Fatal("u1 default dir not remapped")
	}

	// Dirs: local row imported; ghost row skipped.
	if n := v1RowCount(t, "dirs", ""); n != 1 {
		t.Fatalf("dirs imported = %d, want 1", n)
	}

	// Rules: both import (ghost rule degrades to dir-only, StorageID 0).
	if n := v1RowCount(t, "rules", ""); n != 2 {
		t.Fatalf("rules imported = %d, want 2", n)
	}
	var ghostRule database.Rule
	if err := database.DB().Where("content = ?", ".*mkv").First(&ghostRule).Error; err != nil {
		t.Fatalf("ghost rule: %v", err)
	}
	if ghostRule.StorageID != 0 {
		t.Fatalf("ghost rule storage = %d, want 0 (dir-only)", ghostRule.StorageID)
	}

	// Watches: collision keeps cursor 500; invalid regex dropped but the
	// watch survives; soft-deleted skipped.
	if n := v1RowCount(t, "watch_chats", "deleted_at IS NULL"); n != 2 {
		t.Fatalf("live watches = %d, want 2", n)
	}
	var w database.WatchChat
	if err := database.DB().Where("chat_id = ?", 9001).First(&w).Error; err != nil {
		t.Fatalf("watch 9001: %v", err)
	}
	if w.LastMessageID == nil || *w.LastMessageID != 500 {
		t.Fatalf("watch 9001 cursor = %v, want 500 (higher wins)", w.LastMessageID)
	}
	if w.MediaType != "photo,video" || w.DirPath != "watched" || w.StorageID == 0 {
		t.Fatalf("watch 9001 fields = %+v", w)
	}
	var w2 database.WatchChat
	if err := database.DB().Where("chat_id = ?", 9002).First(&w2).Error; err != nil {
		t.Fatalf("watch 9002: %v", err)
	}
	if w2.MessageFilter != "" {
		t.Fatalf("invalid filter must be dropped, got %q", w2.MessageFilter)
	}

	// Dedup ledger: local row imported keyed by chat 100 → u1; orphan and
	// ghost rows skipped.
	if n := v1RowCount(t, "saved_file_dedups", ""); n != 1 {
		t.Fatalf("dedup rows = %d, want 1", n)
	}
	var d database.SavedFileDedup
	if err := database.DB().First(&d).Error; err != nil {
		t.Fatalf("dedup row: %v", err)
	}
	if d.UserID != u1.ID || d.FileUniqueID != "fid-1" {
		t.Fatalf("dedup row owner/fid = %d/%q", d.UserID, d.FileUniqueID)
	}

	// Telegram saved ledger: local row only.
	if n := v1RowCount(t, "telegram_saved_files", ""); n != 1 {
		t.Fatalf("tg saved rows = %d, want 1", n)
	}

	// Marker written.
	if !ForkDBImported(ctx) {
		t.Fatal("marker not written")
	}
}

// TestImportLegacyDBIdempotent: a second run with the same source is a
// no-op via the marker; a marker-less re-import duplicates nothing.
func TestImportLegacyDBIdempotent(t *testing.T) {
	setupImportV1(t)
	src := createV0Fixture(t)
	importFixture(t, src)

	// Marker path: ForkDBImported short-circuits at the boot layer; the
	// importer itself must still be duplicate-safe when called directly.
	importFixture(t, src)
	if n := v1RowCount(t, "watch_chats", "deleted_at IS NULL"); n != 2 {
		t.Fatalf("second import duplicated watches: %d", n)
	}
	if n := v1RowCount(t, "saved_file_dedups", ""); n != 1 {
		t.Fatalf("second import duplicated dedups: %d", n)
	}
	if n := v1RowCount(t, "rules", ""); n != 2 {
		t.Fatalf("second import duplicated rules: %d", n)
	}
	if n := v1RowCount(t, "users", "chat_id = ?", 200); n != 1 {
		t.Fatalf("second import duplicated user 200: %d", n)
	}
}

// TestImportLegacyDBReadOnly: the source file is byte-identical after the
// import — the v0 production file is never mutated.
func TestImportLegacyDBReadOnly(t *testing.T) {
	setupImportV1(t)
	src := createV0Fixture(t)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	importFixture(t, src)
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("v0 source file was mutated by the import")
	}
}

// TestImportLegacyDBRefusesWrongFile: a v1-shaped file and an empty file
// are both refused; the v1 DB stays unchanged and marker-less.
func TestImportLegacyDBRefusesWrongFile(t *testing.T) {
	setupImportV1(t)
	ctx := context.Background()

	// A v1-shaped file (schema_migrations present).
	v1Path := filepath.Join(t.TempDir(), "v1shape.db")
	g, err := openV0Writable(v1Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := g.Exec(`CREATE TABLE storages (id INTEGER PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := g.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if err := ImportLegacyDB(ctx, v1Path); err == nil {
		t.Fatal("v1-shaped file must be refused")
	}

	// A file with no users table at all.
	emptyPath := filepath.Join(t.TempDir(), "empty.db")
	ge, err := openV0Writable(emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ge.Exec(`CREATE TABLE other (x INTEGER)`).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := ge.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if err := ImportLegacyDB(ctx, emptyPath); err == nil {
		t.Fatal("non-v0 file must be refused")
	}

	if ForkDBImported(ctx) {
		t.Fatal("marker must stay absent after refusals")
	}
}

// TestImportLegacyDBHollowImport: when --config was not passed, no storage
// rows exist and every v0 name reference fails to resolve — the import must
// refuse and leave NO marker.
func TestImportLegacyDBHollowImport(t *testing.T) {
	initV1DB(t)
	src := createV0Fixture(t)
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ImportLegacyDB(context.Background(), copyPath); err == nil {
		t.Fatal("hollow import must be refused (zero resolvable storages)")
	}
	if ForkDBImported(context.Background()) {
		t.Fatal("marker must stay absent after hollow-import refusal")
	}
	// The refused transaction must have left nothing behind.
	if n := v1RowCount(t, "users", ""); n != 0 {
		t.Fatalf("refused import left %d user rows", n)
	}
}

// TestImportLegacyDBProbeFailureFails: a source whose dedup table cannot be
// probed (dropped mid-life) must fail the import — a silent section skip
// would commit an incomplete migration behind the permanent marker.
func TestImportLegacyDBProbeFailureFails(t *testing.T) {
	setupImportV1(t)
	src := createV0Fixture(t)
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := openV0Writable(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Exec(`DROP TABLE saved_file_dedups`).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := g.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if err := ImportLegacyDB(context.Background(), copyPath); err == nil {
		t.Fatal("unprobeable section must fail the import")
	}
	if ForkDBImported(context.Background()) {
		t.Fatal("marker must stay absent after probe failure")
	}
}
