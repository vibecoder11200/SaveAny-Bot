package journal_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/journal"
)

func setupJournalEnv(t *testing.T, recovery bool) context.Context {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[db]\npath = '" + filepath.Join(dir, "journal_test.db") + "'\n\n[task]\nrecovery = " + boolToTOML(recovery) + "\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := config.Init(context.Background(), cfgPath); err != nil {
		t.Fatalf("config init: %v", err)
	}
	database.Init(context.Background())
	t.Cleanup(func() {
		// Release the sqlite file so TempDir cleanup can remove it.
		_ = database.CloseDB()
	})
	return context.Background()
}

func boolToTOML(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func testPayload(n int) journal.TGTaskPayload {
	refs := make([]journal.TGFileRef, 0, n)
	for i := 0; i < n; i++ {
		refs = append(refs, journal.TGFileRef{
			ChatID:      -100123,
			MsgID:       10 + i,
			Name:        "file.mp4",
			StorageName: "local",
			Path:        "dir/file.mp4",
		})
	}
	return journal.TGTaskPayload{Files: refs, AsBatch: n > 1, Source: "save"}
}

func pendingCount(t *testing.T, ctx context.Context) int {
	t.Helper()
	rows, err := journal.ListPending(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	return len(rows)
}

func TestJournalRecordAndCompleteLifecycle(t *testing.T) {
	ctx := setupJournalEnv(t, true)

	if err := journal.Record(ctx, "task1", 42, testPayload(1)); err != nil {
		t.Fatalf("record: %v", err)
	}
	rows, err := journal.ListPending(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 pending row, got %d", len(rows))
	}
	if rows[0].ID != "task1" || rows[0].UserID != 42 {
		t.Errorf("unexpected row: id=%s userID=%d", rows[0].ID, rows[0].UserID)
	}
	var payload journal.TGTaskPayload
	if err := json.Unmarshal([]byte(rows[0].Payload), &payload); err != nil {
		t.Fatalf("payload round-trip: %v", err)
	}
	if len(payload.Files) != 1 || payload.Files[0].ChatID != -100123 || payload.Files[0].MsgID != 10 {
		t.Errorf("unexpected payload: %+v", payload)
	}

	// Re-record with the same ID must upsert, not duplicate.
	if err := journal.Record(ctx, "task1", 42, testPayload(2)); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	if got := pendingCount(t, ctx); got != 1 {
		t.Fatalf("duplicate ID created a second row: %d rows", got)
	}

	journal.Complete(ctx, "task1")
	if got := pendingCount(t, ctx); got != 0 {
		t.Fatalf("complete did not delete the row: %d rows", got)
	}
	// Idempotent.
	journal.Complete(ctx, "task1")
	if got := pendingCount(t, ctx); got != 0 {
		t.Fatalf("second complete changed state: %d rows", got)
	}
}

func TestJournalUpsertPreservesAttempts(t *testing.T) {
	ctx := setupJournalEnv(t, true)

	if err := journal.Record(ctx, "task1", 42, testPayload(1)); err != nil {
		t.Fatalf("record: %v", err)
	}
	journal.BumpAttempts(ctx, "task1")
	journal.BumpAttempts(ctx, "task1")
	if err := journal.Record(ctx, "task1", 42, testPayload(1)); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	rows, err := journal.ListPending(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Attempts != 2 {
		t.Fatalf("attempts not preserved across upsert: %+v", rows)
	}
}

func TestJournalDisabledIsNoop(t *testing.T) {
	ctx := setupJournalEnv(t, false)

	if err := journal.Record(ctx, "task1", 42, testPayload(1)); err != nil {
		t.Fatalf("record with recovery disabled should be a silent no-op: %v", err)
	}
	if got := pendingCount(t, ctx); got != 0 {
		t.Fatalf("record wrote a row despite recovery being disabled: %d rows", got)
	}
}
