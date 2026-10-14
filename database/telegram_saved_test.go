package database_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/database"
)

func setupSavedIndexEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[db]\npath = '" + filepath.Join(dir, "saved_index_test.db") + "'\n"
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

func TestTelegramSavedIndex(t *testing.T) {
	setupSavedIndexEnv(t)
	ctx := context.Background()

	if database.TelegramSavedExists(ctx, "TG Hehe", "a/b.mp4") {
		t.Fatal("empty index must not report exists")
	}
	if err := database.RecordTelegramSaved(ctx, "TG Hehe", "a/b.mp4"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !database.TelegramSavedExists(ctx, "TG Hehe", "a/b.mp4") {
		t.Fatal("recorded path must exist")
	}
	if database.TelegramSavedExists(ctx, "Other Store", "a/b.mp4") {
		t.Fatal("different storage must not match")
	}
	if database.TelegramSavedExists(ctx, "TG Hehe", "a/c.mp4") {
		t.Fatal("different path must not match")
	}
	// Re-recording the same row is a no-op, not an error.
	if err := database.RecordTelegramSaved(ctx, "TG Hehe", "a/b.mp4"); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	// Batch recording.
	if err := database.RecordTelegramSaved(ctx, "TG Hehe", "x/1.mp4", "x/2.mp4"); err != nil {
		t.Fatalf("batch record: %v", err)
	}
	if !database.TelegramSavedExists(ctx, "TG Hehe", "x/2.mp4") {
		t.Fatal("batch-recorded path must exist")
	}
	// Empty paths are dropped silently.
	if err := database.RecordTelegramSaved(ctx, "TG Hehe", ""); err != nil {
		t.Fatalf("empty path record: %v", err)
	}
}
