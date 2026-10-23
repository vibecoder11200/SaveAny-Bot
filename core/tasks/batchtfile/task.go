package batchtfile

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/gotd/td/telegram/downloader"
	userclient "github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/pkg/enums/tasktype"
	"github.com/krau/SaveAny-Bot/pkg/journal"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"
	"github.com/rs/xid"
)

var _ core.Executable = (*Task)(nil)

type TaskElement struct {
	ID              string
	Storage         storage.Storage
	Path            string
	File            tfile.TGFile
	localPath       string
	stream          bool
	sourceGroupKey  string
	sourceCaption   string
	preserveCaption bool
	// uploadHash is the sha256 of the downloaded cache file, captured during
	// the pre-upload dedup check and recorded in the ledger once the upload
	// actually succeeds.
	uploadHash string
}

type Task struct {
	ID           string
	ctx          context.Context
	elems        []TaskElement
	Progress     ProgressTracker
	IgnoreErrors bool
	UserID       int64 // requesting user's chat ID; 0 when created without one (HTTP API)
	// SkipIfExists makes Execute skip elements whose destination file
	// already exists, regardless of the user's conflict strategy. Recovery
	// sets it: an existing file is this same task's earlier progress.
	SkipIfExists    bool
	downloaded      atomic.Int64
	totalSize       int64
	uploadTotalSize atomic.Int64
	processing      map[string]TaskElementInfo
	processingMu    sync.RWMutex
	itemStates      []itemProgressState
	itemIndex       map[string]int
	itemMu          sync.RWMutex
	uploadOnce      sync.Once
	uploadMu        sync.Mutex
	uploaded        map[string]int64
}

// Title implements core.Exectable.
func (t *Task) Title() string {
	return fmt.Sprintf("[%s](%d files/%.2fMB)", t.Type(), len(t.elems), float64(t.totalSize)/(1024*1024))
}

func (t *Task) Type() tasktype.TaskType {
	return tasktype.TaskTypeTgfiles
}

// JournalPayload implements core's journal source contract for the whole
// batch: every element must carry its source message, otherwise the batch
// cannot be rebuilt after a restart and is not journaled at all.
func (t *Task) JournalPayload() (int64, journal.TGTaskPayload, bool) {
	if len(t.elems) == 0 {
		return 0, journal.TGTaskPayload{}, false
	}
	refs := make([]journal.TGFileRef, 0, len(t.elems))
	for _, elem := range t.elems {
		msgFile, ok := elem.File.(tfile.TGFileMessage)
		if !ok {
			return 0, journal.TGTaskPayload{}, false
		}
		ref, ok := journal.NewTGFileRef(msgFile, elem.Storage.Name(), elem.Path, viaUserbot(elem.File.Dler()))
		if !ok {
			return 0, journal.TGTaskPayload{}, false
		}
		refs = append(refs, ref)
	}
	payload := journal.TGTaskPayload{Files: refs, AsBatch: true}
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

func NewTaskElement(
	stor storage.Storage,
	path string,
	file tfile.TGFile,
) (*TaskElement, error) {
	id := xid.New().String()
	groupKey, caption, preserveCaption := sourceMetadata(file)
	_, ok := stor.(storage.StorageCannotStream)
	if !config.C().Stream || ok {
		cachePath, err := filepath.Abs(filepath.Join(config.C().Temp.BasePath, fmt.Sprintf("%s_%s", id, file.Name())))
		if err != nil {
			return nil, fmt.Errorf("failed to get absolute path for cache: %w", err)
		}
		return &TaskElement{
			ID:              id,
			Storage:         stor,
			Path:            path,
			File:            file,
			localPath:       cachePath,
			sourceGroupKey:  groupKey,
			sourceCaption:   caption,
			preserveCaption: preserveCaption,
		}, nil
	}
	return &TaskElement{
		ID:              id,
		Storage:         stor,
		Path:            path,
		File:            file,
		stream:          true,
		sourceGroupKey:  groupKey,
		sourceCaption:   caption,
		preserveCaption: preserveCaption,
	}, nil
}

func sourceMetadata(file tfile.TGFile) (groupKey, caption string, preserveCaption bool) {
	messageFile, ok := file.(tfile.TGFileMessage)
	if !ok || messageFile.Message() == nil {
		return "", "", false
	}
	msg := messageFile.Message()
	groupID, grouped := msg.GetGroupedID()
	if !grouped || groupID == 0 {
		return "", "", false
	}
	chatID := tgutil.ChatIdFromPeer(msg.GetPeerID())
	return fmt.Sprintf("%T:%d:%d", msg.GetPeerID(), chatID, groupID), msg.GetMessage(), true
}

func NewBatchTGFileTask(
	id string,
	ctx context.Context,
	files []TaskElement,
	progress ProgressTracker,
	ignoreErrors bool,
) *Task {
	itemStates, itemIndex := newItemProgressStates(files)
	task := &Task{
		ID:         id,
		ctx:        ctx,
		elems:      files,
		Progress:   progress,
		downloaded: atomic.Int64{},
		totalSize: func() int64 {
			var total int64
			for _, elem := range files {
				total += elem.File.Size()
			}
			return total
		}(),
		processing:   make(map[string]TaskElementInfo),
		itemStates:   itemStates,
		itemIndex:    itemIndex,
		uploaded:     make(map[string]int64),
		IgnoreErrors: ignoreErrors,
		processingMu: sync.RWMutex{},
	}
	return task
}
