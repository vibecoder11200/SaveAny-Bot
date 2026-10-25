package shortcut

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/conflictutil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/msgelem"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/ruleutil"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/core/tasks/batchtfile"
	tftask "github.com/krau/SaveAny-Bot/core/tasks/tfile"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/enums/tasktype"
	"github.com/krau/SaveAny-Bot/pkg/tcbdata"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"
	"github.com/rs/xid"
)

func CreateAndAddTGFileTaskWithEdit(ctx *ext.Context, userID int64, stor storage.Storage, dirPath string, file tfile.TGFileMessage, trackMsgID int, conflictStrategy ...string) error {
	logger := log.FromContext(ctx)
	strategy := selectedConflictStrategy(conflictStrategy)
	user, err := database.GetUserByChatID(ctx, userID)
	if err != nil {
		logger.Errorf("Failed to get user by chat ID: %s", err)
		ctx.EditMessage(userID, &tg.MessagesEditMessageRequest{
			ID: trackMsgID,
			Message: i18n.T(i18nk.BotMsgCommonErrorGetUserWithErrFailed, map[string]any{
				"Error": err.Error(),
			}),
		})
		return dispatcher.EndGroups
	}
	strategy = conflictutil.ResolveStrategy(user, strategy)
	if user.ApplyRule && user.Rules != nil {
		matched, matchedStorageName, matchedDirPath := ruleutil.ApplyRule(ctx, user.Rules, ruleutil.NewInput(file))
		if !matched {
			goto startCreateTask
		}
		if matchedDirPath != "" {
			dirPath = matchedDirPath.String()
		}
		if matchedStorageName.Usable() {
			stor, err = storage.GetStorageByUserIDAndName(ctx, user.ChatID, matchedStorageName.String())
			if err != nil {
				logger.Errorf("Failed to get storage by user ID and name: %s", err)
				ctx.EditMessage(userID, &tg.MessagesEditMessageRequest{
					ID: trackMsgID,
					Message: i18n.T(i18nk.BotMsgCommonErrorGetStorageFailed, map[string]any{
						"Error": err.Error(),
					}),
				})
				return dispatcher.EndGroups
			}
		}
	}
startCreateTask:
	storagePath := path.Join(dirPath, file.Name())
	if strategy == tcbdata.ConflictStrategyAsk || strategy == tcbdata.ConflictStrategySkip {
		exists := stor.Exists(ctx, storagePath)
		if exists && strategy == tcbdata.ConflictStrategyAsk {
			return promptTGFileConflictStrategy(ctx, userID, stor.Name(), dirPath, []tfile.TGFileMessage{file}, false, []string{conflictutil.FormatPath(stor.Name(), storagePath)}, trackMsgID)
		}
		if exists {
			ctx.EditMessage(userID, &tg.MessagesEditMessageRequest{
				ID: trackMsgID,
				Message: i18n.T(i18nk.BotMsgCommonInfoAllConflictFilesSkipped, map[string]any{
					"Skipped": file.Name(),
				}),
				ReplyMarkup: nil,
			})
			return dispatcher.EndGroups
		}
	}
	injectCtx := tgutil.ExtWithContext(ctx.Context, ctx)
	if strategy == tcbdata.ConflictStrategyOverwrite {
		injectCtx = storage.WithOverwrite(injectCtx)
	}
	taskid := xid.New().String()
	task, err := tftask.NewTGFileTask(taskid, injectCtx, file, stor, storagePath,
		tftask.NewProgressTrack(
			trackMsgID,
			userID))
	if err != nil {
		logger.Errorf("create task failed: %s", err)
		ctx.EditMessage(userID, &tg.MessagesEditMessageRequest{
			ID: trackMsgID,
			Message: i18n.T(i18nk.BotMsgCommonErrorTaskCreateFailed, map[string]any{
				"Error": err.Error(),
			}),
		})
		return dispatcher.EndGroups
	}
	task.UserID = userID
	// Overwrite is a deliberate re-save of known content: dedup must not
	// silently drop the file the user explicitly asked to save again.
	task.DisableDedup = strategy == tcbdata.ConflictStrategyOverwrite
	if err := core.AddTask(injectCtx, task); err != nil {
		logger.Errorf("add task failed: %s", err)
		ctx.EditMessage(userID, &tg.MessagesEditMessageRequest{
			ID: trackMsgID,
			Message: i18n.T(i18nk.BotMsgCommonErrorTaskAddFailed, map[string]any{
				"Error": err.Error(),
			}),
		})
		return dispatcher.EndGroups
	}
	text, entities := msgelem.BuildTaskAddedEntities(ctx, file.Name(), core.GetLength(injectCtx))
	ctx.EditMessage(userID, &tg.MessagesEditMessageRequest{
		ID:       trackMsgID,
		Message:  text,
		Entities: entities,
	})

	return dispatcher.EndGroups
}

// BatchTarget is an explicit destination that replaces the rule layer for
// every file of a batch: set as Override, it makes the caller's storage/dir
// win over any user rule.
type BatchTarget struct {
	Stor    storage.Storage
	DirPath string
}

// BatchOptions configures CreateAndAddBatchTGFileTask. The zero value with
// Files set behaves like the interactive /save batch path.
type BatchOptions struct {
	Stor    storage.Storage // base storage (rule fallback and ASK prompt display)
	DirPath string          // base dir (rule fallback and ASK prompt display)
	Files   []tfile.TGFileMessage
	// TrackMsgID is the message receiving creation feedback. 0 = unattended:
	// without a UISink, feedback edits are dropped instead of addressing
	// message 0.
	TrackMsgID int
	// UISink receives every creation-feedback edit as a closure, so an
	// unattended caller can route them into its own notification channel.
	UISink func(edit func())
	// Progress overrides the batch task's progress tracker (watch album
	// notifications). When nil: a track-message tracker is built when
	// TrackMsgID != 0, and with TrackMsgID == 0 the task runs without any
	// progress reporting — the executor nil-checks Progress throughout.
	Progress batchtfile.ProgressTracker
	// Override, when non-nil, skips the rule layer entirely: every file goes
	// to this storage/dir.
	Override *BatchTarget
	// ForceGrouping routes every grouped file (GroupedID != 0) into an album
	// subfolder, even without a NEW-FOR-ALBUM rule.
	ForceGrouping bool
	// Strategy overrides the user's configured conflict strategy. Unattended
	// callers MUST pin a non-interactive one: the ASK branch would prompt to
	// a nonexistent message and silently drop the files.
	Strategy string
}

// batchPartitionDeps carries the effects partitionBatchFiles needs, so the
// flat/album partitioning logic can be exercised in tests without a Telegram
// context or live storages.
type batchPartitionDeps struct {
	ctx        context.Context
	strategy   string
	baseStor   storage.Storage
	baseDir    string
	override   *BatchTarget
	forceGroup bool
	// applyRule resolves the rule layer for one file (name, dir). Not called
	// when override is set.
	applyRule func(file tfile.TGFileMessage) (string, ruleutil.MatchedDirPath)
	// resolveStor maps a rule storage name to a storage.
	resolveStor func(name string) (storage.Storage, error)
	// exists reports whether the path already exists on the storage.
	exists func(stor storage.Storage, p string) bool
	// dedupHas reports whether the file-id was already saved to the storage.
	dedupHas func(storName, fid string) bool
	// albumNameFor derives the collision-safe album subfolder.
	albumNameFor func(stor storage.Storage, dir, firstFileName string) string
}

// partitionBatchFiles splits files into flat task elements and album groups,
// applying the conflict (skip/ask) and file-id dedup filters along the way.
// Files awaiting an ASK decision land in conflicts; skipped and deduped names
// are reported for the creation feedback. It is the decision core shared by
// the interactive /save batch path and the watch flush.
func partitionBatchFiles(files []tfile.TGFileMessage, deps batchPartitionDeps) ([]batchtfile.TaskElement, []string, []string, []string, error) {
	logger := log.FromContext(deps.ctx)
	strategy := deps.strategy
	skipped := make([]string, 0)
	conflicts := make([]string, 0)
	deduped := make([]string, 0)
	elems := make([]batchtfile.TaskElement, 0, len(files))
	type albumFile struct {
		file    tfile.TGFileMessage
		storage storage.Storage
		dirPath string
	}
	albumFiles := make(map[int64][]albumFile)

	// checkFlat runs the conflict and dedup filters for one file destined for
	// a flat path and appends its element. A nil return means the file was
	// either appended or intentionally filtered out (conflict ask-pending,
	// skipped, or deduped); a non-nil return is a hard failure.
	checkFlat := func(file tfile.TGFileMessage, fileStor storage.Storage, storPath string) error {
		if strategy == tcbdata.ConflictStrategyAsk || strategy == tcbdata.ConflictStrategySkip {
			exists := deps.exists(fileStor, storPath)
			if exists && strategy == tcbdata.ConflictStrategyAsk {
				conflicts = append(conflicts, conflictutil.FormatPath(fileStor.Name(), storPath))
				return nil
			}
			if exists {
				skipped = append(skipped, file.Name())
				return nil
			}
		}
		// Name conflict keeps priority; file-id dedup only fires for
		// content this user has not already saved to this storage. An
		// explicit overwrite is a deliberate re-save — never deduped.
		if strategy != tcbdata.ConflictStrategyOverwrite {
			if fid := file.FileUniqueID(); fid != "" && deps.dedupHas(fileStor.Name(), fid) {
				logger.Infof("dedup skip (file-id %s): already saved to storage %s, path %s", fid, fileStor.Name(), storPath)
				deduped = append(deduped, file.Name())
				return nil
			}
		}
		elem, err := batchtfile.NewTaskElement(fileStor, storPath, file)
		if err != nil {
			logger.Errorf("Failed to create task element: %s", err)
			return err
		}
		elems = append(elems, *elem)
		return nil
	}

	for _, file := range files {
		var fileStor storage.Storage
		var dirPath string
		var matchedDirPath ruleutil.MatchedDirPath
		if deps.override != nil {
			fileStor = deps.override.Stor
			dirPath = deps.override.DirPath
		} else {
			storName, ruleDir := deps.applyRule(file)
			var err error
			if storName == deps.baseStor.Name() || storName == "" {
				fileStor = deps.baseStor
			} else {
				fileStor, err = deps.resolveStor(storName)
				if err != nil {
					logger.Errorf("Failed to get storage by user ID and name: %s", err)
					return nil, nil, nil, nil, err
				}
			}
			dirPath = ruleDir.String()
			matchedDirPath = ruleDir
		}
		groupId, isGroup := file.Message().GetGroupedID()
		needAlbum := matchedDirPath.NeedNewForAlbum() ||
			(deps.forceGroup && isGroup && groupId != 0)
		if !needAlbum {
			storPath := path.Join(dirPath, file.Name())
			if err := checkFlat(file, fileStor, storPath); err != nil {
				return nil, nil, nil, nil, err
			}
			continue
		}
		if !isGroup || groupId == 0 {
			logger.Warnf("File %s is not in a group, skipping album handling", file.Name())
			continue
		}
		// Album files live under the resolved directory; a NEW-FOR-ALBUM
		// sentinel resolves to the caller's base dir (the sentinel string
		// itself is never a path). The album subdirectory is named after the
		// first file (loop below).
		albumBase := dirPath
		if matchedDirPath.NeedNewForAlbum() {
			albumBase = deps.baseDir
			if deps.override != nil {
				albumBase = deps.override.DirPath
			}
		}
		albumFiles[groupId] = append(albumFiles[groupId], albumFile{
			file:    file,
			storage: fileStor,
			dirPath: albumBase,
		})
	}

	// Deterministic album processing order: map iteration would otherwise
	// shuffle multi-group flushes run to run.
	groupIDs := make([]int64, 0, len(albumFiles))
	for groupID := range albumFiles {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })
	for _, groupID := range groupIDs {
		afiles := albumFiles[groupID]
		if len(afiles) <= 1 {
			// A media group reduced to one surviving member is kept as a
			// plain file — an album send needs at least two items, and
			// dropping the survivor would silently lose it.
			for _, af := range afiles {
				if err := checkFlat(af.file, af.storage, path.Join(af.dirPath, af.file.Name())); err != nil {
					return nil, nil, nil, nil, err
				}
			}
			continue
		}
		// 对于需要新建目录的文件, 将第一个文件的文件名(去除扩展名)作为目录名
		// 存储以第一个文件的存储为准
		albumStor := afiles[0].storage
		albumDir := deps.albumNameFor(albumStor, afiles[0].dirPath, afiles[0].file.Name())
		for _, af := range afiles {
			afstorPath := path.Join(af.dirPath, albumDir, af.file.Name())
			if strategy == tcbdata.ConflictStrategyAsk || strategy == tcbdata.ConflictStrategySkip {
				exists := deps.exists(albumStor, afstorPath)
				if exists && strategy == tcbdata.ConflictStrategyAsk {
					conflicts = append(conflicts, conflictutil.FormatPath(albumStor.Name(), afstorPath))
					continue
				}
				if exists {
					skipped = append(skipped, af.file.Name())
					continue
				}
			}
			if strategy != tcbdata.ConflictStrategyOverwrite {
				if fid := af.file.FileUniqueID(); fid != "" && deps.dedupHas(albumStor.Name(), fid) {
					logger.Infof("dedup skip (file-id %s): already saved to storage %s, path %s", fid, albumStor.Name(), afstorPath)
					deduped = append(deduped, af.file.Name())
					continue
				}
			}
			elem, err := batchtfile.NewTaskElement(albumStor, afstorPath, af.file)
			if err != nil {
				logger.Errorf("Failed to create task element for album file: %s", err)
				return nil, nil, nil, nil, err
			}
			elems = append(elems, *elem)
		}
	}
	return elems, skipped, conflicts, deduped, nil
}

// CreateAndAddBatchTGFileTask builds and enqueues one batch task from files,
// applying conflict/dedup filters and the album grouping rules. It is the
// shared core behind the interactive /save batch path (WithEdit wrapper) and
// the unattended watch flush.
func CreateAndAddBatchTGFileTask(ctx *ext.Context, userID int64, opts BatchOptions) error {
	logger := log.FromContext(ctx)
	strategy := selectedConflictStrategy([]string{opts.Strategy})
	user, err := database.GetUserByChatID(ctx, userID)
	if err != nil {
		logger.Errorf("Failed to get user by chat ID: %s", err)
		emitBatchEdit(ctx, userID, opts, &tg.MessagesEditMessageRequest{
			Message: i18n.T(i18nk.BotMsgCommonErrorGetUserWithErrFailed, map[string]any{
				"Error": err.Error(),
			}),
		})
		return err
	}
	strategy = conflictutil.ResolveStrategy(user, strategy)

	useRule := opts.Override == nil && user.ApplyRule && user.Rules != nil

	// An unattended caller has no message to prompt on: the ASK branch
	// would address message 0 and silently drop the files. Defensive —
	// every current unattended caller pins a strategy, but the core must
	// not depend on that.
	if strategy == tcbdata.ConflictStrategyAsk && opts.TrackMsgID == 0 && opts.UISink == nil {
		strategy = tcbdata.ConflictStrategyRename
	}

	applyRule := func(file tfile.TGFileMessage) (string, ruleutil.MatchedDirPath) {
		if !useRule {
			return opts.Stor.Name(), ruleutil.MatchedDirPath(opts.DirPath)
		}
		matched, storName, dirP := ruleutil.ApplyRule(ctx, user.Rules, ruleutil.NewInput(file))
		if !matched {
			return opts.Stor.Name(), ruleutil.MatchedDirPath(opts.DirPath)
		}
		storname := storName.String()
		if !storName.Usable() {
			storname = opts.Stor.Name()
		}
		return storname, dirP
	}

	resolveFileStor := func(storName string) (storage.Storage, error) {
		if opts.Override != nil {
			return opts.Override.Stor, nil
		}
		if storName == opts.Stor.Name() || storName == "" {
			return opts.Stor, nil
		}
		return storage.GetStorageByUserIDAndName(ctx, user.ChatID, storName)
	}

	elems, skipped, conflicts, deduped, err := partitionBatchFiles(opts.Files, batchPartitionDeps{
		ctx:         ctx,
		strategy:    strategy,
		baseStor:    opts.Stor,
		baseDir:     opts.DirPath,
		override:    opts.Override,
		forceGroup:  opts.ForceGrouping,
		applyRule:   applyRule,
		resolveStor: resolveFileStor,
		exists: func(stor storage.Storage, p string) bool {
			return stor.Exists(ctx, p)
		},
		dedupHas: func(storName, fid string) bool {
			return database.DedupHasFileID(ctx, user.ChatID, storName, fid)
		},
		albumNameFor: func(stor storage.Storage, dir, firstFileName string) string {
			return albumDirNameAt(ctx, stor, dir, firstFileName, time.Now())
		},
	})
	if err != nil {
		emitBatchEdit(ctx, userID, opts, &tg.MessagesEditMessageRequest{
			Message: i18n.T(i18nk.BotMsgCommonErrorGetStorageFailed, map[string]any{
				"Error": err.Error(),
			}),
		})
		return err
	}

	// emitEdit routes creation-feedback edits through the UI sink, or edits
	// the track message directly, or drops them (unattended, no sink).
	emitEdit := func(req *tg.MessagesEditMessageRequest) {
		emitBatchEdit(ctx, userID, opts, req)
	}

	if strategy == tcbdata.ConflictStrategyAsk && len(conflicts) > 0 {
		return promptTGFileConflictStrategy(ctx, userID, opts.Stor.Name(), opts.DirPath, opts.Files, true, conflicts, opts.TrackMsgID)
	}

	injectCtx := tgutil.ExtWithContext(ctx.Context, ctx)
	if strategy == tcbdata.ConflictStrategyOverwrite {
		injectCtx = storage.WithOverwrite(injectCtx)
	}
	if len(elems) == 0 {
		emitEdit(&tg.MessagesEditMessageRequest{
			Message:     buildAllDedupedMessage(deduped, skipped),
			ReplyMarkup: nil,
		})
		return nil
	}
	taskid := xid.New().String()
	var taskProgress batchtfile.ProgressTracker
	switch {
	case opts.Progress != nil:
		taskProgress = opts.Progress
	case opts.TrackMsgID != 0:
		taskProgress = batchtfile.NewProgressTrackerWithSkipped(opts.TrackMsgID, userID, skipped, deduped)
	}
	task := batchtfile.NewBatchTGFileTask(taskid, injectCtx, elems, taskProgress, true)
	task.UserID = userID
	// Overwrite is a deliberate re-save of known content: dedup must not
	// silently drop files the user explicitly asked to save again.
	task.DisableDedup = strategy == tcbdata.ConflictStrategyOverwrite
	if err := core.AddTask(injectCtx, task); err != nil {
		logger.Errorf("Failed to add batch task: %s", err)
		emitEdit(&tg.MessagesEditMessageRequest{
			Message: i18n.T(i18nk.BotMsgCommonErrorTaskAddFailed, map[string]any{
				"Error": err.Error(),
			}),
		})
		return err
	}
	emitEdit(&tg.MessagesEditMessageRequest{
		Message:     buildBatchAddedMessage(len(elems), skipped, deduped),
		ReplyMarkup: nil,
	})
	return nil
}

// emitBatchEdit delivers a creation-feedback edit for a batch task: through
// the caller's UI sink when one is set, otherwise by editing the track
// message when one exists. Unattended callers with neither get no edit —
// never an API call addressing message 0.
func emitBatchEdit(ctx *ext.Context, userID int64, opts BatchOptions, req *tg.MessagesEditMessageRequest) {
	if opts.UISink != nil {
		opts.UISink(func() { ctx.EditMessage(userID, req) })
		return
	}
	if opts.TrackMsgID != 0 {
		ctx.EditMessage(userID, req)
	}
}

// CreateAndAddBatchTGFileTaskWithEdit is the interactive /save wrapper: all
// creation feedback edits the replied-to message, the rule layer applies, and
// the user's conflict strategy (overridable) decides conflicts. Behavior is
// identical to the pre-refactor shortcut except two deliberate fixes: album
// groups reduced to one member are kept (routed as a plain file) and album
// folder names get a -HHMMSS suffix on collision instead of silently
// overwriting an existing directory.
func CreateAndAddBatchTGFileTaskWithEdit(ctx *ext.Context, userID int64, stor storage.Storage, dirPath string, files []tfile.TGFileMessage, trackMsgID int, conflictStrategy ...string) error {
	_ = CreateAndAddBatchTGFileTask(ctx, userID, BatchOptions{
		Stor:       stor,
		DirPath:    dirPath,
		Files:      files,
		TrackMsgID: trackMsgID,
		Strategy:   selectedConflictStrategy(conflictStrategy),
	})
	// The interactive path is a dispatcher handler: every outcome ends the
	// update chain, exactly as before the core extraction.
	return dispatcher.EndGroups
}

func promptTGFileConflictStrategy(ctx *ext.Context, userID int64, storageName, dirPath string, files []tfile.TGFileMessage, asBatch bool, conflicts []string, trackMsgID int) error {
	markup, err := msgelem.BuildConflictStrategyMarkup(tcbdata.Add{
		TaskType:         tasktype.TaskTypeTgfiles,
		SelectedStorName: storageName,
		SettedDir:        true,
		SelectedDirPath:  dirPath,
		Files:            files,
		AsBatch:          asBatch,
	})
	if err != nil {
		return err
	}
	ctx.EditMessage(userID, &tg.MessagesEditMessageRequest{
		ID:          trackMsgID,
		Message:     i18n.T(i18nk.BotMsgCommonPromptSelectConflictStrategy, map[string]any{"Files": conflictutil.FormatPaths(conflicts)}),
		ReplyMarkup: markup,
	})
	return dispatcher.EndGroups
}

func selectedConflictStrategy(strategies []string) string {
	if len(strategies) == 0 {
		return ""
	}
	return strategies[0]
}

func buildBatchAddedMessage(count int, skipped, deduped []string) string {
	switch {
	case len(skipped) > 0 && len(deduped) > 0:
		return i18n.T(i18nk.BotMsgCommonInfoBatchTasksAddedWithSkippedAndDedup, map[string]any{
			"Count":   count,
			"Skipped": strings.Join(skipped, "\n"),
			"Deduped": strings.Join(deduped, "\n"),
		})
	case len(deduped) > 0:
		return i18n.T(i18nk.BotMsgCommonInfoBatchTasksAddedWithDedup, map[string]any{
			"Count":   count,
			"Deduped": strings.Join(deduped, "\n"),
		})
	case len(skipped) > 0:
		return i18n.T(i18nk.BotMsgCommonInfoBatchTasksAddedWithSkipped, map[string]any{
			"Count":   count,
			"Skipped": strings.Join(skipped, "\n"),
		})
	default:
		return i18n.T(i18nk.BotMsgCommonInfoBatchTasksAdded, map[string]any{
			"Count": count,
		})
	}
}

func buildAllDedupedMessage(deduped, skipped []string) string {
	parts := make([]string, 0, 2)
	if len(deduped) > 0 {
		parts = append(parts, i18n.T(i18nk.BotMsgCommonInfoAllDedupFilesSkipped, map[string]any{
			"Deduped": strings.Join(deduped, "\n"),
		}))
	}
	if len(skipped) > 0 {
		parts = append(parts, i18n.T(i18nk.BotMsgCommonInfoAllConflictFilesSkipped, map[string]any{
			"Skipped": strings.Join(skipped, "\n"),
		}))
	}
	return strings.Join(parts, "\n\n")
}

// albumDirName derives the album subfolder for a flushed media group from its
// first file's base name, avoiding collisions. See albumDirNameAt.
func albumDirName(ctx context.Context, stor storage.Storage, dirPath, firstFileName string) string {
	return albumDirNameAt(ctx, stor, dirPath, firstFileName, time.Now())
}

// albumDirNameAt is albumDirName with an injectable clock for tests. On a
// name collision the folder gets a -HHMMSS suffix; if that collides too, or
// the first file has no usable base name, a timestamped album- folder is
// used. Storage has no listing API, so any existing entry counts as a
// collision — reusing an existing (possibly non-empty) directory is never
// attempted.
func albumDirNameAt(ctx context.Context, stor storage.Storage, dirPath, firstFileName string, now time.Time) string {
	base := strings.TrimSuffix(path.Base(firstFileName), path.Ext(firstFileName))
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." || !usableAlbumName(base) {
		return "album-" + now.Format("20060102-150405")
	}
	if !stor.Exists(ctx, path.Join(dirPath, base)) {
		return base
	}
	suffixed := base + "-" + now.Format("150405")
	if !stor.Exists(ctx, path.Join(dirPath, suffixed)) {
		return suffixed
	}
	return "album-" + now.Format("20060102-150405")
}

// usableAlbumName rejects separator/control surprises that path.Base could
// not fully rule out.
func usableAlbumName(name string) bool {
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if r < 0x20 {
			return false
		}
	}
	return true
}
