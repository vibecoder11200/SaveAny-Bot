package database_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/database"
)

func setupDedupEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[db]\npath = '" + filepath.Join(dir, "dedup_test.db") + "'\n"
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

func TestDedupFileID(t *testing.T) {
	setupDedupEnv(t)
	ctx := context.Background()

	if database.DedupHasFileID(ctx, 1, "TG Hehe", "doc:123") {
		t.Fatal("empty ledger must not report exists")
	}
	if err := database.RecordDedup(ctx, 1, "TG Hehe", "doc:123", "", 100, "a/b.mp4"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !database.DedupHasFileID(ctx, 1, "TG Hehe", "doc:123") {
		t.Fatal("recorded file-id must exist")
	}
	if database.DedupHasFileID(ctx, 1, "Other Store", "doc:123") {
		t.Fatal("different storage must not match (per-storage dedup)")
	}
	if database.DedupHasFileID(ctx, 2, "TG Hehe", "doc:123") {
		t.Fatal("different user must not match (per-user dedup)")
	}
	if database.DedupHasFileID(ctx, 1, "TG Hehe", "photo:123") {
		t.Fatal("doc and photo namespaces must stay separate")
	}
	if database.DedupHasFileID(ctx, 1, "TG Hehe", "") {
		t.Fatal("empty file-id must never match")
	}
	// Recording again is idempotent.
	if err := database.RecordDedup(ctx, 1, "TG Hehe", "doc:123", "", 100, "a/b.mp4"); err != nil {
		t.Fatalf("re-record: %v", err)
	}
}

func TestDedupHash(t *testing.T) {
	setupDedupEnv(t)
	ctx := context.Background()

	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := database.RecordDedup(ctx, 1, "TG Hehe", "", hash, 200, "a/c.mp4"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !database.DedupHasHash(ctx, 1, "TG Hehe", hash) {
		t.Fatal("recorded hash must exist")
	}
	if database.DedupHasHash(ctx, 1, "TG Hehe", "bbbb") {
		t.Fatal("different hash must not match")
	}
	if database.DedupHasHash(ctx, 1, "TG Hehe", "") {
		t.Fatal("empty hash must never match")
	}
}

func TestDedupNoUserIsNoOp(t *testing.T) {
	setupDedupEnv(t)
	ctx := context.Background()

	if database.DedupHasFileID(ctx, 0, "TG Hehe", "doc:1") {
		t.Fatal("user 0 must never report exists")
	}
	if database.DedupHasHash(ctx, -5, "TG Hehe", "abc") {
		t.Fatal("negative user must never report exists")
	}
	// Recording under user 0 is a no-op; a later real-user save is unaffected.
	if err := database.RecordDedup(ctx, 0, "TG Hehe", "doc:1", "h", 1, "p"); err != nil {
		t.Fatalf("record user 0: %v", err)
	}
	if database.DedupHasFileID(ctx, 1, "TG Hehe", "doc:1") {
		t.Fatal("user-0 record must not leak into user 1 namespace")
	}
}

func TestDedupCheckHash(t *testing.T) {
	setupDedupEnv(t)
	ctx := context.Background()

	content := []byte("dedup check body")
	a := filepath.Join(t.TempDir(), "a.bin")
	if err := os.WriteFile(a, content, 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	skip, hash, err := database.DedupCheckHash(ctx, 1, "TG Hehe", a)
	if err != nil {
		t.Fatalf("check hash: %v", err)
	}
	if skip || hash == "" {
		t.Fatalf("first sight: skip=%v hash=%q", skip, hash)
	}
	if err := database.RecordDedup(ctx, 1, "TG Hehe", "", hash, int64(len(content)), a); err != nil {
		t.Fatalf("record: %v", err)
	}
	skip, hash2, err := database.DedupCheckHash(ctx, 1, "TG Hehe", a)
	if err != nil {
		t.Fatalf("check hash again: %v", err)
	}
	if !skip || hash2 != hash {
		t.Fatalf("second sight: skip=%v hash=%q want %q", skip, hash2, hash)
	}

	b := filepath.Join(t.TempDir(), "b.bin")
	if err := os.WriteFile(b, append(append([]byte{}, content...), '!'), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	skip, _, err = database.DedupCheckHash(ctx, 1, "TG Hehe", b)
	if err != nil {
		t.Fatalf("check hash b: %v", err)
	}
	if skip {
		t.Fatal("one byte different must not dedup")
	}
}
