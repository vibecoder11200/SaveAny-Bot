package core

import (
	"context"
	"errors"
	"sync"

	"github.com/charmbracelet/log"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/pkg/enums/tasktype"
	"github.com/krau/SaveAny-Bot/pkg/journal"
	"github.com/krau/SaveAny-Bot/pkg/queue"
	"github.com/krau/SaveAny-Bot/pkg/taskevent"
)

var (
	queueOnce     sync.Once
	queueInstance *queue.TaskQueue[Executable]
)

// initQueue lazily creates the shared task queue.
func initQueue() *queue.TaskQueue[Executable] {
	queueOnce.Do(func() {
		queueInstance = queue.NewTaskQueue[Executable]()
	})
	return queueInstance
}

type Executable interface {
	Type() tasktype.TaskType
	Title() string
	TaskID() string
	Execute(ctx context.Context) error
}

// journalSource is implemented by tasks that can be rebuilt from a journal
// payload after a restart (the TG-file tasks): the choke point below records
// their payload before enqueueing, so a row that survives a crash is
// recoverable. Tasks that cannot be re-derived from source messages simply
// do not implement it.
type journalSource interface {
	JournalPayload() (userID int64, payload journal.TGTaskPayload, ok bool)
}

func worker(ctx context.Context, qe *queue.TaskQueue[Executable], semaphore chan struct{}) {
	logger := log.FromContext(ctx)
	execHooks := config.C().Hook.Exec
	for {
		semaphore <- struct{}{}
		qtask, err := qe.Get()
		if err != nil {
			logger.Error("Failed to get task from queue:", err)
			break // queue closed and empty
		}
		exe := qtask.Data
		taskCtx := qtask.Context()
		logger.Infof("Processing task: %s", exe.TaskID())
		taskevent.Emit(taskCtx, taskevent.Event{TaskID: exe.TaskID(), Phase: taskevent.PhaseStart})
		if err := ExecCommandString(taskCtx, execHooks.TaskBeforeStart); err != nil {
			logger.Errorf("Failed to execute before start hook for task %s: %v", exe.TaskID(), err)
		}
		err = exe.Execute(taskCtx)
		// finalizeJournal drops the journal row of a finished TG-file task.
		// It must not run when the root context is dead: a shutdown is
		// exactly the moment the row becomes the recovery data for the next
		// boot. User cancels and real failures land here only while the bot
		// is alive (and the delete is idempotent, so also deleting from
		// CancelTask never conflicts).
		finalizeJournal := func() {
			if ctx.Err() != nil {
				return
			}
			journal.Complete(ctx, exe.TaskID())
		}
		if err != nil {
			if errors.Is(taskCtx.Err(), context.Canceled) {
				logger.Infof("Task %s was canceled", exe.TaskID())
				finalizeJournal()
				if err := ExecCommandString(ctx, execHooks.TaskCancel); err != nil {
					logger.Errorf("Failed to execute cancel hook for task %s: %v", exe.TaskID(), err)
				}
			} else {
				logger.Errorf("Failed to execute task %s: %v", exe.TaskID(), err)
				finalizeJournal()
				if err := ExecCommandString(ctx, execHooks.TaskFail); err != nil {
					logger.Errorf("Failed to execute fail hook for task %s: %v", exe.TaskID(), err)
				}
			}
		} else {
			logger.Infof("Task %s completed successfully", exe.TaskID())
			finalizeJournal()
			if err := ExecCommandString(ctx, execHooks.TaskSuccess); err != nil {
				logger.Errorf("Failed to execute success hook for task %s: %v", exe.TaskID(), err)
			}
		}
		taskevent.Emit(taskCtx, taskevent.Event{TaskID: exe.TaskID(), Phase: taskevent.PhaseDone, Err: err})
		qe.Done(qtask.ID)
		<-semaphore
	}
}

func Run(ctx context.Context) {
	log.FromContext(ctx).Info("Start processing tasks...")
	semaphore := make(chan struct{}, config.C().Workers)
	q := initQueue()
	for range config.C().Workers {
		go worker(ctx, q, semaphore)
	}

}

// Close stops the queue and unblocks workers in Get.
func Close() {
	if q := initQueue(); q != nil {
		q.Close()
	}
}

func AddTask(ctx context.Context, task Executable) error {
	// Single journaling choke point: every TG-file task creation site (bot
	// handlers, watch, HTTP API, and any upstream site added later) funnels
	// through here. The row must exist before the task enters the queue — a
	// task that is not yet queued cannot have completed, so Record-then-
	// enqueue can never race a Complete.
	if js, ok := task.(journalSource); ok {
		if userID, payload, ok := js.JournalPayload(); ok {
			if err := journal.Record(ctx, task.TaskID(), userID, payload); err != nil {
				// Journal failures never block the task; it just won't be
				// recovered after a crash.
				log.FromContext(ctx).Warnf("Journal record failed for task %s: %v", task.TaskID(), err)
			}
		}
	}
	if err := initQueue().Add(queue.NewTask(ctx, task.TaskID(), task.Title(), task)); err != nil {
		// The task never entered the queue, so its freshly written row must
		// not survive: it would resurrect a task nobody is running.
		journal.Complete(ctx, task.TaskID())
		return err
	}
	return nil
}

func CancelTask(ctx context.Context, id string) error {
	err := queueInstance.CancelTask(id)
	if err == nil {
		// The user asked for this cancellation: a queued task is discarded
		// without ever reaching the worker, and a running one ends canceled
		// — either way it must not resurrect on restart. Idempotent delete.
		journal.Complete(ctx, id)
	}
	return err
}

func GetLength(ctx context.Context) int {
	return queueInstance.ActiveLength()
}

func GetRunningTasks(ctx context.Context) []queue.TaskInfo {
	return queueInstance.RunningTasks()
}

func GetQueuedTasks(ctx context.Context) []queue.TaskInfo {
	return queueInstance.QueuedTasks()
}
