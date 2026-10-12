package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/enums/tasktype"
	"github.com/krau/SaveAny-Bot/pkg/journal"
)

// The queue and its workers are process-global, so the whole package runs
// against one worker started in TestMain. Tests must not depend on being
// first; the shutdown test relies only on "row kept", which holds however
// the worker is scheduled.
var rootCtx context.Context
var rootCancel context.CancelFunc

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "core_test_*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[db]\npath = '" + filepath.Join(dir, "core_test.db") + "'\n\n[task]\nrecovery = true\nworkers = 1\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write config: %v\n", err)
		os.Exit(1)
	}
	if err := config.Init(context.Background(), cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "config init: %v\n", err)
		os.Exit(1)
	}
	database.Init(context.Background())
	rootCtx, rootCancel = context.WithCancel(context.Background())
	Run(rootCtx)
	os.Exit(m.Run())
}

// fakeTask is a minimal TG-file-like task: journalable, with a controllable
// Execute.
type fakeTask struct {
	id     string
	userID int64
	exec   func(ctx context.Context) error
}

func (f *fakeTask) Type() tasktype.TaskType           { return tasktype.TaskTypeTgfiles }
func (f *fakeTask) Title() string                     { return "fake[" + f.id + "]" }
func (f *fakeTask) TaskID() string                    { return f.id }
func (f *fakeTask) Execute(ctx context.Context) error { return f.exec(ctx) }
func (f *fakeTask) JournalPayload() (int64, journal.TGTaskPayload, bool) {
	return f.userID, journal.TGTaskPayload{
		Files: []journal.TGFileRef{{
			ChatID:      -100123,
			MsgID:       1,
			Name:        "f.mp4",
			StorageName: "local",
			Path:        "f.mp4",
		}},
	}, true
}

func rowExists(t *testing.T, id string) bool {
	t.Helper()
	rows, err := journal.ListPending(context.Background())
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

// waitFor polls until cond is true or the deadline passes.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestJournalRowDeletedOnSuccess(t *testing.T) {
	task := &fakeTask{id: "ok-task", exec: func(context.Context) error { return nil }}
	if err := AddTask(context.Background(), task); err != nil {
		t.Fatalf("add task: %v", err)
	}
	waitFor(t, func() bool { return !rowExists(t, "ok-task") },
		"journal row still present after task completed")
}

func TestJournalRowDeletedOnUserCancelOfQueuedTask(t *testing.T) {
	release := make(chan struct{})
	blocker := &fakeTask{id: "cancel-blocker", exec: func(ctx context.Context) error {
		<-release
		return nil
	}}
	if err := AddTask(context.Background(), blocker); err != nil {
		t.Fatalf("add blocker: %v", err)
	}
	waitFor(t, func() bool { return rowExists(t, "cancel-blocker") }, "blocker row missing")

	queued := &fakeTask{id: "queued-task", exec: func(context.Context) error { return nil }}
	if err := AddTask(context.Background(), queued); err != nil {
		t.Fatalf("add queued: %v", err)
	}
	if err := CancelTask(context.Background(), "queued-task"); err != nil {
		t.Fatalf("cancel queued: %v", err)
	}
	// The queued task is discarded without ever reaching the worker; its row
	// must be gone right away so a restart cannot resurrect it.
	waitFor(t, func() bool { return !rowExists(t, "queued-task") },
		"canceled queued task's row survived")
	if !rowExists(t, "cancel-blocker") {
		t.Fatal("unrelated blocker row must stay while its task runs")
	}
	close(release)
	waitFor(t, func() bool { return !rowExists(t, "cancel-blocker") }, "blocker row not cleaned up")
}

func TestJournalRowCompensatedOnEnqueueError(t *testing.T) {
	release := make(chan struct{})
	blocker := &fakeTask{id: "dup-blocker", exec: func(ctx context.Context) error {
		<-release
		return nil
	}}
	if err := AddTask(context.Background(), blocker); err != nil {
		t.Fatalf("add blocker: %v", err)
	}
	defer close(release)

	dup := &fakeTask{id: "dup-task", exec: func(context.Context) error { return nil }}
	if err := AddTask(context.Background(), dup); err != nil {
		t.Fatalf("add dup: %v", err)
	}
	// A second AddTask with the same ID fails; the compensation must delete
	// the row it just wrote (documented caveat: this also removes the row of
	// the first same-ID task, but IDs are xid-unique in practice and
	// recovery reuses row IDs only once per boot).
	if err := AddTask(context.Background(), dup); err == nil {
		t.Fatal("duplicate ID enqueue should fail")
	}
	waitFor(t, func() bool { return !rowExists(t, "dup-task") },
		"compensation did not delete the row after enqueue error")
}

func TestJournalRowKeptOnShutdown(t *testing.T) {
	task := &fakeTask{id: "shutdown-task", exec: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := AddTask(context.Background(), task); err != nil {
		t.Fatalf("add task: %v", err)
	}
	waitFor(t, func() bool { return rowExists(t, "shutdown-task") }, "row missing before shutdown")

	// Simulate Ctrl+C: the root (and thus the task) context dies. The worker
	// sees a canceled task caused by shutdown and must keep the row. This
	// must stay the last test: the shared worker ends up parked.
	rootCancel()
	time.Sleep(300 * time.Millisecond)
	if !rowExists(t, "shutdown-task") {
		t.Fatal("shutdown deleted the journal row; recovery would lose the task")
	}
}

func TestJournalRowKeptWhenQueueClosed(t *testing.T) {
	// Must run after every other test: it closes the shared queue. A task
	// journaled while the queue is already closed (Ctrl+C racing startup
	// recovery) must keep its row for the next boot.
	Close()
	task := &fakeTask{id: "closed-queue-task", exec: func(context.Context) error { return nil }}
	if err := AddTask(context.Background(), task); err == nil {
		t.Fatal("enqueue onto a closed queue should fail")
	}
	if !rowExists(t, "closed-queue-task") {
		t.Fatal("closed-queue enqueue deleted the journal row; shutdown during recovery would lose the task")
	}
}
