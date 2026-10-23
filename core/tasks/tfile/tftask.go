package tfile

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/gotd/td/telegram/downloader"
	userclient "github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/pkg/enums/tasktype"
	"github.com/krau/SaveAny-Bot/pkg/journal"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"
)

var _ core.Executable = (*Task)(nil)

type Task struct {
	ID       string
	Ctx      context.Context
	File     tfile.TGFile
	Storage  storage.Storage
	Path     string
	Progress ProgressTracker
	UserID   int64 // requesting user's chat ID; 0 when created without one (HTTP API)
	// SkipIfExists makes Execute skip files that already exist in storage,
	// regardless of the user's conflict strategy. Recovery sets it: an
	// existing file is this same task's earlier progress, and at boot there
	// is nobody to ask "overwrite?".
	SkipIfExists bool
	// DisableDedup turns the content-dedup checks off while still recording
	// saves: set when the user explicitly chose the overwrite conflict
	// strategy — an overwrite is a deliberate re-save of known content.
	DisableDedup bool
	stream       bool
	localPath    string
	// uploadHash is the sha256 of the downloaded cache file, captured during
	// the pre-upload dedup check and recorded in the ledger once the upload
	// actually succeeds.
	uploadHash string
}

// JournalPayload implements core's journal source contract: the task is
// described as fetchable source messages plus resolved destinations, so
// recovery can rebuild it after a restart.
func (t *Task) JournalPayload() (int64, journal.TGTaskPayload, bool) {
	msgFile, ok := t.File.(tfile.TGFileMessage)
	if !ok {
		return 0, journal.TGTaskPayload{}, false
	}
	ref, ok := journal.NewTGFileRef(msgFile, t.Storage.Name(), t.Path, viaUserbot(t.File.Dler()))
	if !ok {
		return 0, journal.TGTaskPayload{}, false
	}
	payload := journal.TGTaskPayload{Files: []journal.TGFileRef{ref}, AsBatch: false}
	// Record the progress message so recovery can re-attach to it instead of
	// orphaning it and opening a new thread after a restart.
	if p, ok := t.Progress.(*Progress); ok && p.MessageID != 0 {
		payload.TrackChatID, payload.TrackMsgID = p.ChatID, p.MessageID
	}
	return t.UserID, payload, true
}

// viaUserbot reports whether the file was built on the userbot client;
// recovery must re-fetch with the same account or the source chat may be
// unreachable (e.g. a PM the bot is not a member of).
func viaUserbot(dler downloader.Client) bool {
	uctx := userclient.GetCtx()
	return uctx != nil && dler == uctx.Raw
}

// Title implements core.Exectable.
func (t *Task) Title() string {
	return fmt.Sprintf("[%s](%s->%s:%s)", t.Type(), t.File.Name(), t.Storage.Name(), t.Path)
}

func (t *Task) Type() tasktype.TaskType {
	return tasktype.TaskTypeTgfiles
}

func NewTGFileTask(
	id string,
	ctx context.Context,
	file tfile.TGFile,
	stor storage.Storage,
	path string,
	progress ProgressTracker,
) (*Task, error) {
	_, ok := stor.(storage.StorageCannotStream)
	if !config.C().Stream || ok {
		cachePath, err := filepath.Abs(filepath.Join(config.C().Temp.BasePath, fmt.Sprintf("%s_%s", id, file.Name())))
		if err != nil {
			return nil, fmt.Errorf("failed to get absolute path for cache: %w", err)
		}
		tfile := &Task{
			ID:        id,
			Ctx:       ctx,
			File:      file,
			Storage:   stor,
			Path:      path,
			Progress:  progress,
			localPath: cachePath,
		}
		return tfile, nil
	}
	tfileTask := &Task{
		ID:       id,
		Ctx:      ctx,
		File:     file,
		Storage:  stor,
		Path:     path,
		Progress: progress,
		stream:   true,
	}
	return tfileTask, nil
}
