// Startup recovery: re-enqueue TG-file tasks that were in flight when the
// bot last stopped. One journal row per task (see pkg/journal); this file
// turns surviving rows back into tasks, idempotently, and sends each
// affected user one summary message.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"

	userclient "github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/core/tasks/batchtfile"
	coretfile "github.com/krau/SaveAny-Bot/core/tasks/tfile"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/journal"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"
)

// recoveryMaxAge bounds how long a pending row may sit unrecovered. Rows
// older than this are dropped: either the sources are long gone or the bot
// never managed to run recovery for days, and a stale backlog would spam
// users on every boot. Age, not attempt count — restart loops during a
// flood penalty must never burn tasks.
const recoveryMaxAge = 48 * time.Hour

type recoveryStats struct {
	TasksRecovered    int
	FilesRecovered    int
	Deleted           int
	StorageFailed     int
	ClientUnavailable int
	Dropped           int
}

type rowOutcome int

const (
	rowRecovered rowOutcome = iota
	rowStorageFailed
	rowClientUnavailable
	rowAllSourcesGone
	rowInfraError
	rowDropped
)

// RunRecovery re-enqueues pending tasks after startup. It runs in its own
// goroutine and never blocks startup. Safe to call once per boot, after the
// bot client is ready.
func RunRecovery(ctx context.Context, botCtx *ext.Context) {
	logger := log.FromContext(ctx)
	if !config.C().Task.Recovery || botCtx == nil {
		return
	}
	rows, err := journal.ListPending(ctx)
	if err != nil {
		logger.Errorf("Recovery: failed to list pending tasks: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	logger.Infof("Recovery: %d pending task(s) from the last run", len(rows))
	stats := &recoveryStats{}

	live := make([]database.PendingTask, 0, len(rows))
	cutoff := time.Now().Add(-recoveryMaxAge)
	for _, row := range rows {
		if row.CreatedAt.Before(cutoff) {
			journal.Complete(ctx, row.ID)
			stats.Dropped++
			continue
		}
		live = append(live, row)
	}

	byUser := make(map[int64][]database.PendingTask)
	for _, row := range live {
		byUser[row.UserID] = append(byUser[row.UserID], row)
	}
	for userID, userRows := range byUser {
		recoverUserRows(ctx, botCtx, userID, userRows, stats)
	}

	logger.Infof("Recovery finished: %d task(s) recovered, %d file(s), %d deleted source(s), %d storage-failed, %d client-unavailable, %d dropped",
		stats.TasksRecovered, stats.FilesRecovered, stats.Deleted,
		stats.StorageFailed, stats.ClientUnavailable, stats.Dropped)
}

func recoverUserRows(ctx context.Context, botCtx *ext.Context, userID int64, rows []database.PendingTask, stats *recoveryStats) {
	logger := log.FromContext(ctx).WithPrefix(fmt.Sprintf("recovery[user:%d]", userID))
	var (
		recTasks, recFiles, deleted    int
		storageFailed, clientUnavailbl int
	)
	for i, row := range rows {
		outcome, files, rowDeleted := recoverRow(ctx, botCtx, row, stats)
		switch outcome {
		case rowRecovered:
			recTasks++
			recFiles += files
		case rowStorageFailed:
			storageFailed++
		case rowClientUnavailable:
			clientUnavailbl++
		case rowInfraError:
			// Network or flood: keep every remaining row of this user for
			// the next boot instead of burning them; attempts are
			// observational only.
			journal.BumpAttempts(ctx, row.ID)
			for _, rest := range rows[i+1:] {
				journal.BumpAttempts(ctx, rest.ID)
			}
			logger.Warnf("Fetch failed (infrastructure), %d task(s) kept for the next boot", len(rows)-i)
			sendRecoverySummary(ctx, botCtx, userID, recTasks, recFiles, deleted, storageFailed, clientUnavailbl)
			return
		}
		deleted += rowDeleted
	}
	sendRecoverySummary(ctx, botCtx, userID, recTasks, recFiles, deleted, storageFailed, clientUnavailbl)
}

func recoverRow(ctx context.Context, botCtx *ext.Context, row database.PendingTask, stats *recoveryStats) (rowOutcome, int, int) {
	logger := log.FromContext(ctx).WithPrefix(fmt.Sprintf("recovery[%s]", row.ID))
	var payload journal.TGTaskPayload
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		// Corrupt payload: nothing to rebuild from, drop the row.
		logger.Errorf("Corrupt journal payload, dropping: %v", err)
		journal.Complete(ctx, row.ID)
		stats.Dropped++
		return rowDropped, 0, 0
	}

	uctx := userclient.GetCtx()
	// Storage and client availability are permanent conditions of the
	// current config: retrying on later boots is pointless.
	type preparedRef struct {
		ref  journal.TGFileRef
		stor storage.Storage
	}
	prepared := make([]preparedRef, 0, len(payload.Files))
	for _, ref := range payload.Files {
		stor, err := resolveRecoveryStorage(ctx, row.UserID, ref.StorageName)
		if err != nil {
			logger.Errorf("Storage %q unavailable, dropping task: %v", ref.StorageName, err)
			journal.Complete(ctx, row.ID)
			stats.StorageFailed++
			return rowStorageFailed, 0, 0
		}
		if ref.ViaUserbot && uctx == nil {
			logger.Errorf("Task needs the userbot client but it is disabled, dropping task")
			journal.Complete(ctx, row.ID)
			stats.ClientUnavailable++
			return rowClientUnavailable, 0, 0
		}
		prepared = append(prepared, preparedRef{ref: ref, stor: stor})
	}

	// Re-fetch the source messages to get fresh file references. Grouped by
	// chat and client kind so each chat costs one chunked request.
	type fetchKey struct {
		chatID     int64
		viaUserbot bool
	}
	fetchGroups := make(map[fetchKey][]journal.TGFileRef)
	for _, p := range prepared {
		k := fetchKey{p.ref.ChatID, p.ref.ViaUserbot}
		fetchGroups[k] = append(fetchGroups[k], p.ref)
	}
	msgs := make(map[int]*tg.Message, len(prepared))
	deletedSources := 0
	for k, refs := range fetchGroups {
		fetchCtx := botCtx
		if k.viaUserbot {
			fetchCtx = uctx
		}
		ids := make([]int, 0, len(refs))
		for _, ref := range refs {
			ids = append(ids, ref.MsgID)
		}
		fetched, err := tgutil.GetMessagesByIDs(fetchCtx, k.chatID, ids)
		if err != nil {
			// Infrastructure (network/flood), not a deleted source: the row
			// stays for a later boot.
			logger.Warnf("Fetch failed (infrastructure), keeping task for a later boot: %v", err)
			return rowInfraError, 0, deletedSources
		}
		for _, ref := range refs {
			if msg, ok := fetched[ref.MsgID]; ok {
				msgs[ref.MsgID] = msg
			} else {
				logger.Infof("Source message %d/%d no longer exists", k.chatID, ref.MsgID)
				stats.Deleted++
				deletedSources++
			}
		}
	}
	if len(msgs) == 0 {
		journal.Complete(ctx, row.ID)
		return rowAllSourcesGone, 0, deletedSources
	}

	// Rebuild the task on the bot context (progress edits go through the
	// bot; downloads ride the client each file was built with).
	taskCtx := ext.NewContext(ctx, botCtx.Raw, botCtx.PeerStorage, botCtx.Self, botCtx.Sender, nil, false)
	injectCtx := tgutil.ExtWithContext(ctx, taskCtx)
	trackMsgID := 0
	if row.UserID != 0 {
		if sent, err := taskCtx.SendMessage(row.UserID, &tg.MessagesSendMessageRequest{
			Message: i18n.T(i18nk.BotMsgRecoveryInfoTaskRecovered),
		}); err == nil {
			trackMsgID = sent.ID
		} else {
			logger.Warnf("Failed to send recovery notice to user %d: %v", row.UserID, err)
		}
	}

	dlerFor := func(ref journal.TGFileRef) *tg.Client {
		if ref.ViaUserbot && uctx != nil {
			return uctx.Raw
		}
		return botCtx.Raw
	}

	if payload.AsBatch {
		elems := make([]batchtfile.TaskElement, 0, len(prepared))
		for _, p := range prepared {
			msg, ok := msgs[p.ref.MsgID]
			if !ok {
				continue
			}
			media, ok := msg.GetMedia()
			if !ok {
				continue
			}
			file, err := tfile.FromMediaMessage(media, dlerFor(p.ref), msg, tfile.WithName(p.ref.Name))
			if err != nil {
				logger.Errorf("Failed to rebuild file from message %d: %v", p.ref.MsgID, err)
				continue
			}
			elem, err := batchtfile.NewTaskElement(p.stor, p.ref.Path, file)
			if err != nil {
				logger.Errorf("Failed to create task element: %v", err)
				continue
			}
			elems = append(elems, *elem)
		}
		if len(elems) == 0 {
			journal.Complete(ctx, row.ID)
			return rowAllSourcesGone, 0, deletedSources
		}
		var progress batchtfile.ProgressTracker
		if trackMsgID != 0 {
			progress = batchtfile.NewProgressTracker(trackMsgID, row.UserID)
		}
		task := batchtfile.NewBatchTGFileTask(row.ID, injectCtx, elems, progress, true)
		task.UserID = row.UserID
		task.SkipIfExists = true
		if err := core.AddTask(injectCtx, task); err != nil {
			// Only realistically fails while the queue is shutting down;
			// AddTask keeps the row in that case, so treat it like any
			// other keep-for-next-boot outcome.
			logger.Warnf("Failed to re-enqueue recovered task, keeping row for the next boot: %v", err)
			return rowInfraError, 0, deletedSources
		}
		stats.TasksRecovered++
		stats.FilesRecovered += len(elems)
		return rowRecovered, len(elems), deletedSources
	}

	// Single-file task: take the first ref whose source survived.
	for _, p := range prepared {
		msg, ok := msgs[p.ref.MsgID]
		if !ok {
			continue
		}
		media, ok := msg.GetMedia()
		if !ok {
			continue
		}
		file, err := tfile.FromMediaMessage(media, dlerFor(p.ref), msg, tfile.WithName(p.ref.Name))
		if err != nil {
			logger.Errorf("Failed to rebuild file from message %d: %v", p.ref.MsgID, err)
			continue
		}
		var progress coretfile.ProgressTracker
		if trackMsgID != 0 {
			progress = coretfile.NewProgressTrack(trackMsgID, row.UserID)
		}
		task, err := coretfile.NewTGFileTask(row.ID, injectCtx, file, p.stor, p.ref.Path, progress)
		if err != nil {
			logger.Errorf("Failed to create task: %v", err)
			continue
		}
		task.UserID = row.UserID
		task.SkipIfExists = true
		if err := core.AddTask(injectCtx, task); err != nil {
			logger.Warnf("Failed to re-enqueue recovered task, keeping row for the next boot: %v", err)
			return rowInfraError, 0, deletedSources
		}
		stats.TasksRecovered++
		stats.FilesRecovered++
		return rowRecovered, 1, deletedSources
	}
	journal.Complete(ctx, row.ID)
	return rowAllSourcesGone, 0, deletedSources
}

func resolveRecoveryStorage(ctx context.Context, userID int64, name string) (storage.Storage, error) {
	if userID != 0 {
		return storage.GetStorageByUserIDAndName(ctx, userID, name)
	}
	// Tasks created through the HTTP API have no user: their storage comes
	// from the global registry.
	if stor, ok := storage.GetStorage(name); ok {
		return stor, nil
	}
	return nil, fmt.Errorf("storage not found: %s", name)
}

// sendRecoverySummary reports one boot's recovery result to the user. One
// message per user, per boot — the final word, after the per-task notices.
func sendRecoverySummary(ctx context.Context, botCtx *ext.Context, userID int64, tasks, files, deleted, storageFailed, clientUnavailable int) {
	if userID == 0 || botCtx == nil {
		return
	}
	if tasks == 0 && files == 0 && deleted == 0 && storageFailed == 0 && clientUnavailable == 0 {
		return
	}
	text := i18n.T(i18nk.BotMsgRecoveryInfoSummary, map[string]any{
		"TasksRecovered":    tasks,
		"FilesRecovered":    files,
		"Deleted":           deleted,
		"StorageFailed":     storageFailed,
		"ClientUnavailable": clientUnavailable,
	})
	if _, err := botCtx.SendMessage(userID, &tg.MessagesSendMessageRequest{
		Message: text,
	}); err != nil {
		log.FromContext(ctx).Warnf("Failed to send recovery summary to user %d: %v", userID, err)
	}
}
