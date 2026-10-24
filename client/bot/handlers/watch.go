package handlers

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/conflictutil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/dirutil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/mediautil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/ruleutil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/shortcut"
	userclient "github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/utils/strutil"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/core"
	coretfile "github.com/krau/SaveAny-Bot/core/tasks/tfile"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/enums/fnamest"
	"github.com/krau/SaveAny-Bot/pkg/tcbdata"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"
	"github.com/rs/xid"
)

func handleWatchCmd(ctx *ext.Context, update *ext.Update) error {
	logger := log.FromContext(ctx)
	parts := strutil.ParseArgsRespectQuotes(update.EffectiveMessage.Text)
	if len(parts) < 2 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchHelpText)), nil)
		return dispatcher.EndGroups
	}
	userChatID := update.GetUserChat().GetID()
	user, err := database.GetUserByChatID(ctx, userChatID)
	if err != nil {
		logger.Errorf("Failed to get user: %s", err)
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorGetUserFailed)), nil)
		return dispatcher.EndGroups
	}
	if parts[1] == "notify" {
		return handleWatchNotifyCmd(ctx, update, user, parts[2:])
	}
	if user.DefaultStorage == "" {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorDefaultStorageNotSet)), nil)
		return dispatcher.EndGroups
	}
	// Filter syntax: /watch <chat> [storage:<name>] [dir:<path>] [msgre:<regex>]
	// [mediatype:<types>] — flags in any order, all optional. The msgre regex
	// may contain ':' and span words; dir paths may be quoted.
	parsed, err := parseWatchArgs(parts[1:])
	if err != nil {
		switch {
		case errors.Is(err, errWatchFilterTypeUnsupported):
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchErrorFilterTypeUnsupported)), nil)
		default:
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchErrorFilterFormatInvalid)), nil)
		}
		return dispatcher.EndGroups
	}
	if parsed.Chat == "" {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchHelpText)), nil)
		return dispatcher.EndGroups
	}
	chatArg := parsed.Chat
	chatID, err := tgutil.ParseChatID(ctx, chatArg)
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidIdOrUsername, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	watching, err := user.WatchingChat(ctx, chatID)
	if err != nil {
		logger.Errorf("Failed to check if user is watching chat %d: %s", chatID, err)
		return dispatcher.EndGroups
	}
	if watching {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchInfoAlreadyWatchingChat)), nil)
		return dispatcher.EndGroups
	}
	filter := ""
	if parsed.MsgRe != "" {
		if len(parsed.MsgRe) > maxWatchRegexLen {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidRegex, map[string]any{"Error": "regex too long (max 512 characters)"})), nil)
			return dispatcher.EndGroups
		}
		if _, err := regexp.Compile(parsed.MsgRe); err != nil {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidRegex, map[string]any{"Error": err.Error()})), nil)
			return dispatcher.EndGroups
		}
		filter = watchFlagMsgRe + parsed.MsgRe
	}
	mediaType := parsed.MediaType
	if mediaType != "" {
		// Fail-closed: reject an unknown token now instead of storing a
		// watch that silently never matches, and reject a spec that
		// names no type at all (it would mean "allow everything").
		parsedFilter, err := mediautil.ParseMediaFilter(mediaType)
		if err != nil {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidMediaTypeFilter, map[string]any{"Error": err.Error()})), nil)
			return dispatcher.EndGroups
		}
		if parsedFilter == nil {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchErrorFilterFormatInvalid)), nil)
			return dispatcher.EndGroups
		}
	}
	// Per-watch destination override: the storage must exist now, and the
	// dir must survive the traversal sanitizer — both are stored validated.
	storageName := parsed.StorageName
	if storageName != "" {
		if !config.C().HasStorage(user.ChatID, storageName) {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgRuleErrorStorageNotFound, map[string]any{"Storage": storageName})), nil)
			return dispatcher.EndGroups
		}
		if _, err := storage.GetStorageByUserIDAndName(ctx, user.ChatID, storageName); err != nil {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgRuleErrorStorageNotFound, map[string]any{"Storage": storageName})), nil)
			return dispatcher.EndGroups
		}
	}
	dirPath := ""
	if parsed.DirPath != "" {
		sanitized, err := dirutil.SanitizeRelDirPath(parsed.DirPath)
		if err != nil {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchErrorDirPathInvalid, map[string]any{"Error": err.Error()})), nil)
			return dispatcher.EndGroups
		}
		dirPath = sanitized
	}
	if err := user.WatchChat(ctx, database.WatchChat{
		UserID:      user.ID,
		ChatID:      chatID,
		Filter:      filter,
		MediaType:   mediaType,
		StorageName: storageName,
		DirPath:     dirPath,
	}); err != nil {
		logger.Errorf("Failed to watch chat %d: %s", chatID, err)
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchErrorWatchChatFailed, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchInfoWatchChatStarted, map[string]any{"Chat": chatArg})), nil)
	return dispatcher.EndGroups
}

func handleLswatchCmd(ctx *ext.Context, update *ext.Update) error {
	logger := log.FromContext(ctx)
	userChatID := update.GetUserChat().GetID()
	user, err := database.GetUserByChatID(ctx, userChatID)
	if err != nil {
		logger.Errorf("Failed to get user: %s", err)
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorGetUserFailed)), nil)
		return dispatcher.EndGroups
	}
	chats := user.WatchChats
	if len(chats) == 0 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchInfoWatchListEmpty)), nil)
		return dispatcher.EndGroups
	}
	var sb strings.Builder
	sb.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListHeader))
	for _, chat := range chats {
		sb.WriteString("- ")
		sb.WriteString(fmt.Sprintf("%d", chat.ChatID))
		if chat.StorageName != "" {
			sb.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListStoragePrefix))
			sb.WriteString(chat.StorageName)
			sb.WriteString(")")
		}
		if chat.DirPath != "" {
			sb.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListDirPrefix))
			sb.WriteString(chat.DirPath)
			sb.WriteString(")")
		}
		if chat.Filter != "" {
			sb.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListFilterPrefix))
			sb.WriteString(chat.Filter)
			sb.WriteString(")")
		}
		if chat.MediaType != "" {
			sb.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListMediatypePrefix))
			sb.WriteString(chat.MediaType)
			sb.WriteString(")")
		}
		sb.WriteString("\n")
	}
	ctx.Reply(update, ext.ReplyTextString(sb.String()), nil)
	return dispatcher.EndGroups
}

func handleUnwatchCmd(ctx *ext.Context, update *ext.Update) error {
	logger := log.FromContext(ctx)
	args := strings.Split(update.EffectiveMessage.Text, " ")
	if len(args) < 2 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchErrorUnwatchNoChatProvided)), nil)
		return dispatcher.EndGroups
	}
	userChatID := update.GetUserChat().GetID()
	user, err := database.GetUserByChatID(ctx, userChatID)
	if err != nil {
		logger.Errorf("Failed to get user: %s", err)
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorGetUserFailed)), nil)
		return dispatcher.EndGroups
	}
	chatArg := args[1]
	chatID, err := tgutil.ParseChatID(ctx, chatArg)
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidIdOrUsername, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	if err := user.UnwatchChat(ctx, chatID); err != nil {
		logger.Errorf("Failed to unwatch chat %d: %s", chatID, err)
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchErrorUnwatchChatFailed, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchInfoWatchChatStopped, map[string]any{"Chat": chatArg})), nil)
	return dispatcher.EndGroups
}

// watchIntakeOpts distinguishes the two callers of the shared watch intake.
type watchIntakeOpts struct {
	// live marks events from the live listener: the observed-event cursor
	// advance applies. Backfill-fed events advance the cursor only on
	// durable enqueue, so a crash mid-backfill never loses messages.
	live bool
	// strategyPin, when a valid conflict strategy, overrides the user's
	// configured strategy for the tasks this intake produces. Backfill pins
	// a non-overwrite strategy: overwrite disables dedup, and live/backfill
	// overlap would then duplicate files.
	strategyPin string
}

// watchTargetSpec is a resolved watch destination in plain strings.
type watchTargetSpec struct {
	StorageName string
	DirPath     string
}

// resolveWatchTarget merges, in precedence order: the explicit per-watch
// override (storage:/dir: flags) — which replaces both the rule result and
// the defaults entirely; then the rule match; then the user's defaults. An
// override that sets only one flag keeps the user default for the other.
func resolveWatchTarget(watchStorage, watchDir, ruleStorage, ruleDir, defaultStorage, defaultDir string) watchTargetSpec {
	if watchStorage != "" || watchDir != "" {
		stor := watchStorage
		if stor == "" {
			stor = defaultStorage
		}
		return watchTargetSpec{StorageName: stor, DirPath: watchDir}
	}
	stor := ruleStorage
	if stor == "" {
		stor = defaultStorage
	}
	dir := ruleDir
	if dir == "" {
		dir = defaultDir
	}
	return watchTargetSpec{StorageName: stor, DirPath: dir}
}

// watchFilterRegexFor returns the compiled regex of a watch's stored filter,
// or nil when the watch has no filter. A stored filter that no longer
// compiles means corrupt data: the caller must match nothing.
func watchFilterRegexFor(watch *database.WatchChat) *regexp.Regexp {
	if watch.Filter == "" {
		return nil
	}
	regex, ok := watchFilterRegex(watch.Filter)
	if !ok {
		return nil
	}
	return regex
}

// watchFileMatchesFilter applies the per-file msgre filter (singles path).
func watchFileMatchesFilter(watch *database.WatchChat, msg *tg.Message) bool {
	regex := watchFilterRegexFor(watch)
	if regex == nil {
		return watch.Filter == ""
	}
	return regex.MatchString(mediautil.MatchStringForFilter(msg))
}

// groupMatchesFilter evaluates a media group's msgre filter once per group:
// only one member of an album carries the caption, so the decision string is
// built from the first caption-carrying member (fallback: the first member),
// byte-identical to what /save would match for that member.
func groupMatchesFilter(regex *regexp.Regexp, files []tfile.TGFileMessage) bool {
	if len(files) == 0 {
		return false
	}
	chosen := files[0]
	for _, file := range files {
		if file.Message().GetMessage() != "" {
			chosen = file
			break
		}
	}
	return regex.MatchString(mediautil.MatchStringForFilter(chosen.Message()))
}

// watchConflictStrategy pins a non-interactive conflict strategy for an
// unattended flush: the user's configured strategy when it never prompts,
// rename otherwise — ASK would prompt to a nonexistent message and silently
// drop the files. A pin (backfill) wins over the user's choice.
func watchConflictStrategy(user *database.User, pin string) string {
	if tcbdata.IsConflictStrategy(pin) {
		return pin
	}
	strategy := conflictutil.ResolveStrategy(user, "")
	if strategy == tcbdata.ConflictStrategyAsk {
		return tcbdata.ConflictStrategyRename
	}
	return strategy
}

func listenMediaMessageEvent(ch chan userclient.MediaMessageEvent, botCtx *ext.Context) {
	if userclient.GetCtx() == nil {
		return
	}
	logger := log.FromContext(userclient.GetCtx())
	for event := range ch {
		logger.Debug("Received media message event", "chat_id", event.ChatID, "file_name", event.File.Name())
		chats, err := database.GetWatchChatsByChatID(event.Ctx, event.ChatID)
		if err != nil {
			logger.Errorf("Failed to get watch chats for chat ID %d: %v", event.ChatID, err)
			continue
		}
		for _, chat := range chats {
			processMediaEventForWatch(botCtx, event, chat, watchIntakeOpts{live: true})
		}
	}
}

// processMediaEventForWatch is the shared intake for one observed media
// message and one watch row — live events and catch-up backfill feed the
// exact same path, so filters, album buffering and task creation cannot
// drift between them.
func processMediaEventForWatch(botCtx *ext.Context, event userclient.MediaMessageEvent, watch *database.WatchChat, opts watchIntakeOpts) {
	ctx := event.Ctx
	logger := log.FromContext(ctx)
	file := event.File

	// Live cursor: advance on every observed media event for this watch,
	// BEFORE any filter — a busy channel whose messages mostly do not match
	// must still move the cursor, or every boot re-walks the same window.
	if opts.live {
		if err := database.TouchWatchCursor(ctx, watch.ID, int64(event.MessageID)); err != nil {
			logger.Warnf("Failed to advance watch cursor for chat %d: %v", watch.ChatID, err)
		}
	}

	user, err := database.GetUserByID(ctx, watch.UserID)
	if err != nil {
		logger.Errorf("Failed to get user by ID %d: %v", watch.UserID, err)
		return
	}
	// Media type check comes first: a filtered-out type never reaches the
	// caption regex nor the album buffer.
	if watch.MediaType != "" {
		mediaFilter, err := mediautil.ParseMediaFilter(watch.MediaType)
		if err != nil {
			// Stored values are validated at watch creation; a parse
			// failure here means corrupt data — match nothing rather
			// than silently saving everything.
			logger.Errorf("Invalid stored mediatype filter in chat %d, skipping: %v", watch.ChatID, err)
			return
		}
		if !mediaFilter.Allows(mediautil.MediaType(file.Message().Media)) {
			return
		}
	}
	// No per-chat media type: fall back to the user's default filter.
	if watch.MediaType == "" {
		if !mediautil.UserMediaFilter(user).Allows(mediautil.MediaType(file.Message().Media)) {
			return
		}
	}
	if user.DefaultStorage == "" {
		logger.Warnf("User %d has no default storage set, skipping media message handling", watch.UserID)
		return
	}
	applyFilenameStrategy(ctx, user, file)

	groupID, isGroup := file.Message().GetGroupedID()
	if isGroup && groupID != 0 {
		// ANY watched media group becomes ONE batch task at flush — the
		// msgre filter is evaluated per group at flush time, because only
		// one album member carries the caption.
		watchMediaGroupMgr.addFile(event.ChatID, watch.UserID, file, time.Duration(max(config.C().Telegram.MediaGroupTimeout, 1))*time.Second, func(files []tfile.TGFileMessage) {
			processWatchMediaGroup(ctx, botCtx, watch, user, files, opts)
		})
		return
	}
	// Singles: the msgre filter applies per file.
	if !watchFileMatchesFilter(watch, file.Message()) {
		return
	}
	processWatchSingle(ctx, botCtx, watch, user, file, opts)
}

// applyFilenameStrategy renames the file per the user's filename strategy
// (message-derived or template), keeping the default name on any failure.
func applyFilenameStrategy(ctx *ext.Context, user *database.User, file tfile.TGFileMessage) {
	switch user.FilenameStrategy {
	case fnamest.Message.String():
		file.SetName(tgutil.GenFileNameFromMessage(*file.Message()))
	case fnamest.Template.String():
		if user.FilenameTemplate == "" {
			log.FromContext(ctx).Warnf("Empty filename template for user %d, using default filename", user.ChatID)
			return
		}
		tmpl, err := template.New("filename").Parse(user.FilenameTemplate)
		if err != nil {
			log.FromContext(ctx).Errorf("Failed to parse filename template for user %d: %s", user.ChatID, err)
			return
		}
		data := mediautil.BuildFilenameTemplateData(file.Message())
		var sb strings.Builder
		if err := tmpl.Execute(&sb, data); err != nil {
			log.FromContext(ctx).Errorf("failed to execute filename template: %s", err)
			return
		}
		file.SetName(sb.String())
	}
}

// resolveWatchFlushTarget resolves the concrete destination for a watch
// save: the per-watch override when set (rule layer skipped entirely),
// otherwise the rule match over the user's defaults.
func resolveWatchFlushTarget(ctx *ext.Context, watch *database.WatchChat, user *database.User, ruleInput tfile.TGFileMessage) (storage.Storage, string, error) {
	defaultStorage := user.DefaultStorage
	defaultDir := ""
	if user.DefaultDir != 0 {
		dir, err := database.GetDirByID(ctx, user.DefaultDir)
		if err != nil {
			log.FromContext(ctx).Warnf("Failed to get default dir for user %d: %v, using root", user.ChatID, err)
		} else {
			defaultDir = dir.Path
		}
	}
	var spec watchTargetSpec
	if watch.StorageName != "" || watch.DirPath != "" {
		// Explicit override: the rule layer never runs. The stored dir was
		// sanitized at creation; sanitize again at join time as defense in
		// depth against out-of-band database edits.
		dirPath := ""
		if watch.DirPath != "" {
			sanitized, err := dirutil.SanitizeRelDirPath(watch.DirPath)
			if err != nil {
				log.FromContext(ctx).Warnf("Stored watch dir %q failed sanitization, using root: %v", watch.DirPath, err)
			} else {
				dirPath = sanitized
			}
		}
		spec = resolveWatchTarget(watch.StorageName, dirPath, "", "", defaultStorage, defaultDir)
	} else {
		ruleStorage := ""
		ruleDir := ""
		if user.ApplyRule && user.Rules != nil {
			matched, storName, dirPath := ruleutil.ApplyRule(ctx, user.Rules, ruleutil.NewInput(ruleInput))
			if matched {
				if storName.Usable() {
					ruleStorage = storName.String()
				}
				// A NEW-FOR-ALBUM sentinel resolves to the base dir: the
				// album subfolder itself is appended by the grouping.
				if !dirPath.NeedNewForAlbum() {
					ruleDir = dirPath.String()
				}
			}
		}
		spec = resolveWatchTarget("", "", ruleStorage, ruleDir, defaultStorage, defaultDir)
	}
	stor, err := storage.GetStorageByUserIDAndName(ctx, user.ChatID, spec.StorageName)
	if err != nil {
		return nil, "", err
	}
	return stor, spec.DirPath, nil
}

// processWatchSingle enqueues one non-grouped media message as a single
// task. The cursor advances only after AddTask succeeds — the journal
// records the task inside AddTask, so that is the durability boundary.
func processWatchSingle(ctx *ext.Context, botCtx *ext.Context, watch *database.WatchChat, user *database.User, file tfile.TGFileMessage, opts watchIntakeOpts) {
	logger := log.FromContext(ctx)
	stor, dirPath, err := resolveWatchFlushTarget(ctx, watch, user, file)
	if err != nil {
		logger.Errorf("Failed to resolve watch target for user %d: %v", user.ChatID, err)
		return
	}
	storagePath := path.Join(dirPath, file.Name())
	injectCtx := tgutil.ExtWithContext(ctx.Context, ctx)
	taskid := xid.New().String()
	task, err := coretfile.NewTGFileTask(taskid, injectCtx, file, stor, storagePath, newWatchNotifyProgress(ctx, botCtx, user))
	if err != nil {
		logger.Errorf("create task failed: %s", err)
		return
	}
	task.UserID = user.ChatID
	if err := core.AddTask(injectCtx, task); err != nil {
		logger.Errorf("add task failed: %s", err)
		return
	}
	if file.Message() != nil {
		_ = database.TouchWatchCursor(ctx, watch.ID, int64(file.Message().ID))
	}
	logger.Infof("Added media message task for user %d in chat %d: %s", watch.UserID, watch.ChatID, file.Name())
}

// processWatchMediaGroup flushes one buffered watch media group: the msgre
// filter is evaluated once per group (only one album member carries the
// caption), then the group is enqueued as ONE batch task routed by the
// watch target — a real album send on Telegram storage, a first-file-named
// subfolder on every other storage. The cursor advances per group only
// after the batch task is durably enqueued.
func processWatchMediaGroup(ctx *ext.Context, botCtx *ext.Context, watch *database.WatchChat, user *database.User, files []tfile.TGFileMessage, opts watchIntakeOpts) {
	logger := log.FromContext(ctx)
	if len(files) == 0 {
		return
	}
	groups := make(map[int64][]tfile.TGFileMessage)
	groupIDs := make([]int64, 0, len(files))
	for _, file := range files {
		groupID, isGroup := file.Message().GetGroupedID()
		if !isGroup || groupID == 0 {
			// Defensive: only grouped files enter the buffer.
			processWatchSingle(ctx, botCtx, watch, user, file, opts)
			continue
		}
		if _, ok := groups[groupID]; !ok {
			groupIDs = append(groupIDs, groupID)
		}
		groups[groupID] = append(groups[groupID], file)
	}
	regex := watchFilterRegexFor(watch)
	if watch.Filter != "" && regex == nil {
		logger.Warnf("Invalid filter format in chat %d, dropping %d buffered files", watch.ChatID, len(files))
		return
	}
	for _, groupID := range groupIDs {
		groupFiles := groups[groupID]
		if watch.Filter != "" && !groupMatchesFilter(regex, groupFiles) {
			continue
		}
		if len(groupFiles) == 1 {
			// A media group reduced to one member is saved as a plain
			// file — an album send needs at least two items.
			processWatchSingle(ctx, botCtx, watch, user, groupFiles[0], opts)
			continue
		}
		stor, dirPath, err := resolveWatchFlushTarget(ctx, watch, user, groupFiles[0])
		if err != nil {
			logger.Errorf("Failed to resolve watch target for user %d: %v", user.ChatID, err)
			continue
		}
		err = shortcut.CreateAndAddBatchTGFileTask(ctx, user.ChatID, shortcut.BatchOptions{
			Stor:    stor,
			DirPath: dirPath,
			Files:   groupFiles,
			Override: &shortcut.BatchTarget{
				Stor:    stor,
				DirPath: dirPath,
			},
			ForceGrouping: true,
			Strategy:      watchConflictStrategy(user, opts.strategyPin),
			Progress:      newBatchWatchNotifyProgress(ctx, botCtx, user, nil, nil),
		})
		if err != nil {
			logger.Errorf("Failed to add watch album batch task: %s", err)
			continue
		}
		var maxMsgID int64
		for _, file := range groupFiles {
			if id := int64(file.Message().ID); id > maxMsgID {
				maxMsgID = id
			}
		}
		_ = database.TouchWatchCursor(ctx, watch.ID, maxMsgID)
		logger.Infof("Added watch album batch task for user %d in chat %d: %d files", watch.UserID, watch.ChatID, len(groupFiles))
	}
}

type watchMediaGroupHandler struct {
	groups    map[int64]map[uint][]tfile.TGFileMessage // chatID -> userID -> files
	timers    map[int64]map[uint]*time.Timer
	callbacks map[int64]map[uint]func([]tfile.TGFileMessage)
	mu        sync.Mutex
}

var watchMediaGroupMgr = &watchMediaGroupHandler{
	groups:    make(map[int64]map[uint][]tfile.TGFileMessage),
	timers:    make(map[int64]map[uint]*time.Timer),
	callbacks: make(map[int64]map[uint]func([]tfile.TGFileMessage)),
}

func (w *watchMediaGroupHandler) addFile(chatID int64, userID uint, file tfile.TGFileMessage, timeout time.Duration, callback func([]tfile.TGFileMessage)) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.groups[chatID] == nil {
		w.groups[chatID] = make(map[uint][]tfile.TGFileMessage)
	}
	if w.timers[chatID] == nil {
		w.timers[chatID] = make(map[uint]*time.Timer)
	}
	if w.callbacks[chatID] == nil {
		w.callbacks[chatID] = make(map[uint]func([]tfile.TGFileMessage))
	}

	if timer, exists := w.timers[chatID][userID]; exists {
		timer.Stop()
	}

	w.groups[chatID][userID] = append(w.groups[chatID][userID], file)
	w.callbacks[chatID][userID] = callback

	w.timers[chatID][userID] = time.AfterFunc(timeout, func() {
		files, cb := w.take(chatID, userID)
		if len(files) > 0 {
			cb(files)
		}
	})
}

// take removes and returns the buffered group of one (chat, user) pair.
func (w *watchMediaGroupHandler) take(chatID int64, userID uint) ([]tfile.TGFileMessage, func([]tfile.TGFileMessage)) {
	w.mu.Lock()
	defer w.mu.Unlock()

	files := w.groups[chatID][userID]
	callback := w.callbacks[chatID][userID]
	delete(w.groups[chatID], userID)
	delete(w.callbacks[chatID], userID)
	if timer, exists := w.timers[chatID][userID]; exists {
		timer.Stop()
		delete(w.timers[chatID], userID)
	}
	if len(w.groups[chatID]) == 0 {
		delete(w.groups, chatID)
	}
	if len(w.callbacks[chatID]) == 0 {
		delete(w.callbacks, chatID)
	}
	if len(w.timers[chatID]) == 0 {
		delete(w.timers, chatID)
	}
	return files, callback
}

// FlushChat force-flushes every buffered media group of a chat, as if the
// group timers had fired. The boot catch-up calls it after backfilling a
// watch so a straddled album is not kept waiting for a live event that may
// never come.
func (w *watchMediaGroupHandler) FlushChat(chatID int64) {
	w.mu.Lock()
	userIDs := make([]uint, 0, len(w.groups[chatID]))
	for userID := range w.groups[chatID] {
		userIDs = append(userIDs, userID)
	}
	w.mu.Unlock()
	for _, userID := range userIDs {
		files, callback := w.take(chatID, userID)
		if len(files) > 0 && callback != nil {
			callback(files)
		}
	}
}
