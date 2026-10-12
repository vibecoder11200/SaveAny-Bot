// Package journal persists in-flight TG-file tasks so they survive a
// restart. It is a thin SQLite-backed log: one row per task, written before
// the task enters the queue and deleted when the task truly finishes; a row
// that survives a crash is exactly the recovery input.
//
// The dependency points one way only: this package imports database and
// config, never core (the executor).
package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/charmbracelet/log"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// TGFileRef identifies one Telegram file of a journaled task by its source
// message. The file location is deliberately NOT persisted — locations go
// stale; recovery re-fetches the message with the same kind of client that
// created the task (ViaUserbot) to obtain a fresh one. Name is the
// resolved, already-sanitized destination file name; Path the resolved
// destination path (rules included), so recovery never re-evaluates them.
type TGFileRef struct {
	ChatID      int64  `json:"chat_id"`
	MsgID       int    `json:"msg_id"`
	Name        string `json:"name"`
	StorageName string `json:"storage"`
	Path        string `json:"path"`
	ViaUserbot  bool   `json:"via_userbot"`
}

// TGTaskPayload is the JSON payload persisted per journaled task.
type TGTaskPayload struct {
	Files   []TGFileRef `json:"files"`
	AsBatch bool        `json:"as_batch"`
	Source  string      `json:"source,omitempty"`
}

// NewTGFileRef builds a file reference from a message-backed TG file and
// its resolved destination. ok is false when the file carries no message
// (then its source cannot be re-fetched and the task must not be journaled).
func NewTGFileRef(file tfile.TGFileMessage, storageName, path string, viaUserbot bool) (TGFileRef, bool) {
	msg := file.Message()
	if msg == nil {
		return TGFileRef{}, false
	}
	return TGFileRef{
		ChatID:      tgutil.ChatIdFromPeer(msg.GetPeerID()),
		MsgID:       msg.GetID(),
		Name:        file.Name(),
		StorageName: storageName,
		Path:        path,
		ViaUserbot:  viaUserbot,
	}, true
}

// Record upserts the journal row for a task. It must be called before the
// task enters the queue: a crash between the two leaves a row whose task is
// re-run on recovery, which is the at-least-once contract. Disabled
// recovery or an empty payload is a no-op; a database error is returned for
// the caller to log — it must never block the task itself.
func Record(ctx context.Context, taskID string, userID int64, p TGTaskPayload) error {
	if !config.C().Task.Recovery || len(p.Files) == 0 {
		return nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		err = fmt.Errorf("marshal journal payload for task %s: %w", taskID, err)
		log.FromContext(ctx).Warnf("%v", err)
		return err
	}
	row := database.PendingTask{
		ID:        taskID,
		UserID:    userID,
		Payload:   string(data),
		CreatedAt: time.Now(),
	}
	if err := database.UpsertPendingTask(ctx, row); err != nil {
		log.FromContext(ctx).Warnf("Failed to record journal for task %s: %v", taskID, err)
		return err
	}
	return nil
}

// Complete deletes the journal row for a finished task. Idempotent.
func Complete(ctx context.Context, taskID string) {
	if err := database.DeletePendingTask(ctx, taskID); err != nil {
		log.FromContext(ctx).Warnf("Failed to complete journal for task %s: %v", taskID, err)
	}
}

// ListPending returns all pending rows, oldest first.
func ListPending(ctx context.Context) ([]database.PendingTask, error) {
	return database.ListPendingTasks(ctx)
}

// BumpAttempts counts one more infrastructure failure for a row. Attempts
// are observational only: recovery drops rows by age, never by count.
func BumpAttempts(ctx context.Context, taskID string) {
	if err := database.BumpPendingTaskAttempts(ctx, taskID); err != nil {
		log.FromContext(ctx).Warnf("Failed to bump attempts for task %s: %v", taskID, err)
	}
}
