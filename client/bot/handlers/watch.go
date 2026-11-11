package handlers

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gotd/td/telegram/message/markup"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/conflictutil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/dirutil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/mediautil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/msgelem"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/ruleutil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/shortcut"
	userclient "github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/mediafilter"
	"github.com/krau/SaveAny-Bot/common/utils/strutil"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/core/tasks/batchtfile"
	coretfile "github.com/krau/SaveAny-Bot/core/tasks/tfile"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/enums/fnamest"
	"github.com/krau/SaveAny-Bot/pkg/tcbdata"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"
	"github.com/krau/mygotg/dispatcher"
	"github.com/krau/mygotg/ext"
	"github.com/rs/xid"
)

func handleWatchCmd(ctx *ext.Context, update *ext.Update) error {
	logger := log.FromContext(ctx)
	args := strutil.ParseArgsRespectQuotes(update.EffectiveMessage.Text)
	if len(args) < 2 {
		ctx.Reply(update, ext.ReplyTextString(watchMenuText("")), &ext.ReplyOpts{Markup: watchMenuMarkup()})
		return dispatcher.EndGroups
	}
	userChatID := responsibleUserID(update)
	user, err := database.GetUserByChatID(ctx, userChatID)
	if err != nil {
		logger.Errorf("Failed to get user: %s", err)
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorGetUserFailed)), nil)
		return dispatcher.EndGroups
	}
	if user.DefaultStorageID == 0 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorDefaultStorageNotSet)), nil)
		return dispatcher.EndGroups
	}
	// Filter syntax: /watch <chat> [storage:<name>] [dir:<path>] [msgre:<regex>]
	// [mediatype:<types>] — flags in any order, all optional. The msgre regex
	// may contain ':' and span words; dir paths may be quoted.
	parsed, err := parseWatchArgs(args[1:])
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
		ctx.Reply(update, ext.ReplyTextString(watchMenuText(i18n.T(i18nk.BotMsgWatchMenuAddHelp))), &ext.ReplyOpts{Markup: watchBackMarkup()})
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
		// watch that silently never matches, and reject a spec that names
		// no type at all (it would mean "allow everything").
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
	// Per-watch destination override: the storage is resolved to its ID at
	// add time (renames never orphan the watch), and the dir must survive
	// the traversal sanitizer — both are stored validated.
	storageID := uint(0)
	if parsed.StorageName != "" {
		stor, err := storage.GetStorageByUserIDAndName(ctx, user.ChatID, parsed.StorageName)
		if err != nil {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgRuleErrorStorageNotFound, map[string]any{"Storage": parsed.StorageName})), nil)
			return dispatcher.EndGroups
		}
		storageID = storage.ID(stor)
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
		UserID:        user.ID,
		ChatID:        chatID,
		MessageFilter: filter,
		MediaType:     mediaType,
		StorageID:     storageID,
		DirPath:       dirPath,
	}); err != nil {
		logger.Errorf("Failed to watch chat %d: %s", chatID, err)
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchErrorWatchChatFailed, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgWatchInfoWatchChatStarted, map[string]any{"Chat": chatArg})), nil)
	return dispatcher.EndGroups
}

func watchMenuText(extra string) string {
	text := i18n.T(i18nk.BotMsgWatchMenuTitle) + "\n\n" + i18n.T(i18nk.BotMsgWatchMenuHint)
	if extra != "" {
		text += "\n\n" + extra
	}
	return text
}

func watchMenuMarkup() *tg.ReplyInlineMarkup {
	return &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{
		{Buttons: []tg.KeyboardInlineButton{
			markup.Callback(i18n.T(i18nk.BotMsgWatchMenuAdd), fmt.Appendf(nil, "%s add", tcbdata.TypeWatch)),
			markup.Callback(i18n.T(i18nk.BotMsgWatchMenuList), fmt.Appendf(nil, "%s list", tcbdata.TypeWatch)),
		}},
		{Buttons: []tg.KeyboardInlineButton{
			markup.Callback(i18n.T(i18nk.BotMsgWatchMenuNotify), fmt.Appendf(nil, "%s notify", tcbdata.TypeWatch)),
			markup.Callback(i18n.T(i18nk.BotMsgWatchMenuSync), fmt.Appendf(nil, "%s sync", tcbdata.TypeWatch)),
		}},
	}}
}

func watchBackMarkup() *tg.ReplyInlineMarkup {
	return &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{
		Buttons: []tg.KeyboardInlineButton{
			markup.Callback(i18n.T(i18nk.BotMsgWatchMenuBack), fmt.Appendf(nil, "%s menu", tcbdata.TypeWatch)),
		},
	}}}
}

func handleWatchCallback(ctx *ext.Context, update *ext.Update) error {
	args := strings.Fields(string(update.CallbackQuery.Data))
	if len(args) < 2 {
		return fmt.Errorf("invalid callback data: %q", update.CallbackQuery.Data)
	}
	switch args[1] {
	case "menu":
		return editWatchMenu(ctx, update)
	case "add":
		return editWatchAdd(ctx, update)
	case "list":
		return editWatchList(ctx, update)
	case "notify":
		return editWatchNotify(ctx, update)
	case "sync":
		return editWatchSync(ctx, update)
	case "rm":
		return editWatchRemove(ctx, update, args)
	default:
		return editWatchMenu(ctx, update)
	}
}

func editWatchMenu(ctx *ext.Context, update *ext.Update) error {
	ctx.EditMessage(chatID(update), &tg.MessagesEditMessageRequest{
		ID:          update.CallbackQuery.GetMsgID(),
		Message:     watchMenuText(""),
		ReplyMarkup: watchMenuMarkup(),
	})
	return dispatcher.EndGroups
}

func editWatchAdd(ctx *ext.Context, update *ext.Update) error {
	ctx.EditMessage(chatID(update), &tg.MessagesEditMessageRequest{
		ID:          update.CallbackQuery.GetMsgID(),
		Message:     watchMenuText(i18n.T(i18nk.BotMsgWatchMenuAddHelp)),
		ReplyMarkup: watchBackMarkup(),
	})
	return dispatcher.EndGroups
}

func watchListText(chats []database.WatchChat) string {
	if len(chats) == 0 {
		return i18n.T(i18nk.BotMsgWatchInfoWatchListEmpty)
	}
	var b strings.Builder
	b.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListHeader))
	for _, chat := range chats {
		fmt.Fprintf(&b, "\n- %d", chat.ChatID)
		if chat.StorageID != 0 {
			b.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListStoragePrefix))
			b.WriteString(config.C().GetStorageNameByID(chat.StorageID))
			b.WriteString(")")
		}
		if chat.DirPath != "" {
			b.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListDirPrefix))
			b.WriteString(chat.DirPath)
			b.WriteString(")")
		}
		if chat.MediaType != "" {
			b.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListMediatypePrefix))
			b.WriteString(chat.MediaType)
			b.WriteString(")")
		}
		if chat.MessageFilter != "" {
			b.WriteString(i18n.T(i18nk.BotMsgWatchInfoWatchListFilterPrefix))
			b.WriteString(chat.MessageFilter)
			b.WriteString(")")
		}
	}
	return b.String()
}

func watchListMarkup(chats []database.WatchChat) *tg.ReplyInlineMarkup {
	rows := make([]tg.KeyboardInlineButtonRow, 0, len(chats)+1)
	for _, chat := range chats {
		rows = append(rows, tg.KeyboardInlineButtonRow{Buttons: []tg.KeyboardInlineButton{
			markup.Callback(i18n.T(i18nk.BotMsgWatchMenuStop), fmt.Appendf(nil, "%s rm %d", tcbdata.TypeWatch, chat.ChatID)),
		}})
	}
	rows = append(rows, watchBackMarkup().Rows...)
	return &tg.ReplyInlineMarkup{Rows: rows}
}

func editWatchList(ctx *ext.Context, update *ext.Update) error {
	userID := update.CallbackQuery.GetUserID()
	user, err := database.GetUserByChatID(ctx, userID)
	if err != nil {
		return err
	}
	ctx.EditMessage(chatID(update), &tg.MessagesEditMessageRequest{
		ID:          update.CallbackQuery.GetMsgID(),
		Message:     watchListText(user.WatchChats),
		ReplyMarkup: watchListMarkup(user.WatchChats),
	})
	return dispatcher.EndGroups
}

func editWatchNotify(ctx *ext.Context, update *ext.Update) error {
	userID := update.CallbackQuery.GetUserID()
	user, err := database.GetUserByChatID(ctx, userID)
	if err != nil {
		return err
	}
	user.WatchNotify = !user.WatchNotify
	if err := database.UpdateUser(ctx, user); err != nil {
		return err
	}
	ctx.EditMessage(chatID(update), &tg.MessagesEditMessageRequest{
		ID:          update.CallbackQuery.GetMsgID(),
		Message:     watchMenuText(i18n.T(watchNotifyStateKey(user.WatchNotify))),
		ReplyMarkup: watchMenuMarkup(),
	})
	return dispatcher.EndGroups
}

func editWatchSync(ctx *ext.Context, update *ext.Update) error {
	count, err := syncPeers(ctx)
	if err != nil {
		ctx.EditMessage(chatID(update), &tg.MessagesEditMessageRequest{
			ID:      update.CallbackQuery.GetMsgID(),
			Message: i18n.T(i18nk.BotMsgSyncpeersFailed, map[string]any{"Error": err.Error()}),
		})
		return dispatcher.EndGroups
	}
	ctx.EditMessage(chatID(update), &tg.MessagesEditMessageRequest{
		ID:          update.CallbackQuery.GetMsgID(),
		Message:     watchMenuText(i18n.T(i18nk.BotMsgSyncpeersSuccess, map[string]any{"Count": count})),
		ReplyMarkup: watchMenuMarkup(),
	})
	return dispatcher.EndGroups
}

func editWatchRemove(ctx *ext.Context, update *ext.Update, args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("invalid callback data: %q", update.CallbackQuery.Data)
	}
	chatID, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return err
	}
	userID := update.CallbackQuery.GetUserID()
	user, err := database.GetUserByChatID(ctx, userID)
	if err != nil {
		return err
	}
	if err := user.UnwatchChat(ctx, chatID); err != nil {
		ctx.AnswerCallback(msgelem.AlertCallbackAnswer(update.CallbackQuery.GetQueryID(), i18n.T(i18nk.BotMsgWatchErrorUnwatchChatFailed, map[string]any{"Error": err.Error()})))
		return dispatcher.EndGroups
	}
	return editWatchList(ctx, update)
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

	// The live listener and the boot catch-up feed the same buffer
	// concurrently; a message buffered twice would end up twice inside ONE
	// album batch task, where no dedup layer can catch it (the ledger row
	// only exists after the first copy uploads). Same-message re-delivery
	// (edits) also lands here harmlessly.
	if msg := file.Message(); msg != nil {
		for _, existing := range w.groups[chatID][userID] {
			if em := existing.Message(); em != nil && em.ID == msg.ID {
				return
			}
		}
	}

	if timer, exists := w.timers[chatID][userID]; exists {
		timer.Stop()
	}

	w.groups[chatID][userID] = append(w.groups[chatID][userID], file)
	w.callbacks[chatID][userID] = callback

	w.timers[chatID][userID] = time.AfterFunc(timeout, func() {
		files, cb := w.take(chatID, userID)
		if len(files) > 0 && cb != nil {
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

// watchIntakeOpts distinguishes the two callers of the shared watch intake.
type watchIntakeOpts struct {
	// live marks events from the live listener: the observed-event cursor
	// advance applies. Backfill-fed events advance the cursor only on
	// durable enqueue, so a crash mid-backfill never loses messages.
	live bool
	// strategyPin, when a valid conflict strategy, overrides the user's
	// configured strategy for the tasks this intake produces. Backfill pins
	// rename: overwrite would disable dedup, and live/backfill overlap
	// would then duplicate files.
	strategyPin string
	// counters, when set, observes durable enqueues; the boot catch-up
	// summary counts only what AddTask accepted.
	counters *watchCatchUpCounters
}

// watchTargetSpec is a resolved watch destination in pure strings.
type watchTargetSpec struct {
	StorageName string
	DirPath     string
}

// resolveWatchTarget merges, in precedence order: the explicit per-watch
// override (storage:/dir: flags) — which replaces the rule result and the
// defaults for the axes it sets; then the rule match; then the user's
// defaults.
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
	if watch.MessageFilter == "" {
		return nil
	}
	regex, ok := watchFilterRegex(watch.MessageFilter)
	if !ok {
		return nil
	}
	return regex
}

// watchFileMatchesFilter applies the per-file msgre filter (singles path).
func watchFileMatchesFilter(watch *database.WatchChat, msg *tg.Message) bool {
	regex := watchFilterRegexFor(watch)
	if regex == nil {
		return watch.MessageFilter == ""
	}
	return regex.MatchString(mediautil.MatchStringForFilter(msg))
}

// groupMatchesFilter evaluates a media group's msgre filter once per group:
// only one album member carries the caption, so the decision string is
// built from the first caption-carrying member (fallback: the first
// member), byte-identical to what /save matches for that message.
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

// watchStorageLostNotified guards the one-time owner notification when a
// watch's stored storage ID stops resolving (deleted + recreated storage).
var watchStorageLostNotified sync.Map // watch.ID -> struct{}

// ListenUserbotMediaEvents consumes userbot media events until the channel
// is closed. resolve returns the bot context used for notifications.
func ListenUserbotMediaEvents(runCtx context.Context, resolve func() *ext.Context) {
	logger := log.FromContext(runCtx)
	for event := range userclient.GetMediaMessageCh() {
		if event.File == nil || event.Ctx == nil {
			continue
		}
		botCtx := resolve()
		if botCtx == nil {
			continue
		}
		message := event.File.Message()
		// Instance-wide gate stays outermost; scope layers narrow it below.
		if message == nil || !mediafilter.Allowed(message.Media) {
			logger.Debug("Skipping filtered media message", "chat_id", event.ChatID, "file_name", event.File.Name())
			continue
		}
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

	// Live × catch-up overlap guard (singles): anything at or below the
	// cursor has already been observed — a backfill page or a debounce
	// re-delivery must not enqueue it twice.
	if cur, ok, err := database.CurrentWatchCursor(ctx, watch.ID); err == nil && ok && int64(event.MessageID) <= cur {
		return
	}

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
	// Media type check first (fork ordering): a filtered-out type never
	// reaches the caption regex nor the album buffer. Precedence:
	// per-watch mediatype > per-user default, through the Phase 6 helper;
	// the instance gate already applied in the live loop / catch-up fetch.
	message := file.Message()
	if watch.MediaType != "" {
		watchScope, err := mediautil.ParseMediaFilter(watch.MediaType)
		if err != nil {
			// Stored values are validated at watch creation; a parse
			// failure here means corrupt data — match nothing rather than
			// silently saving everything.
			logger.Errorf("Invalid stored mediatype filter in chat %d, skipping: %v", watch.ChatID, err)
			return
		}
		if !mediafilter.AllowedFor(message.Media, watchScope) {
			return
		}
	} else if userScope := mediautil.UserMediaFilter(user); userScope != nil {
		if !mediafilter.AllowedFor(message.Media, userScope) {
			return
		}
	}
	if user.DefaultStorageID == 0 {
		logger.Warnf("User %d has no default storage set, skipping media message handling", watch.UserID)
		return
	}
	applyFilenameStrategy(ctx, user, file)

	groupID, isGroup := message.GetGroupedID()
	if isGroup && groupID != 0 {
		// ANY watched media group becomes ONE batch task at flush — the
		// msgre filter is evaluated per group at flush time, because only
		// one album member carries the caption.
		watchMediaGroupMgr.addFile(event.ChatID, watch.UserID, file, time.Duration(max(config.C().Telegram.MediaGroupTimeout, 1))*
			time.Second, func(files []tfile.TGFileMessage) {
			processWatchMediaGroup(ctx, botCtx, watch, user, files, opts)
		})
		return
	}
	// Singles: the msgre filter applies per file.
	if !watchFileMatchesFilter(watch, message) {
		return
	}
	if opts.counters != nil {
		opts.counters.singlesFed++
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
// otherwise the rule match over the user's defaults. A stored storage ID
// that no longer resolves (deleted + recreated under the same name yields a
// NEW id) falls back to the user's default storage and notifies the owner
// once; while the default itself is unresolvable nothing is enqueued and
// the cursor never moves past the message.
func resolveWatchFlushTarget(ctx *ext.Context, botCtx *ext.Context, watch *database.WatchChat, user *database.User, ruleInput tfile.TGFileMessage) (storage.Storage, string, error) {
	logger := log.FromContext(ctx)
	defaultStor, err := storage.GetStorageByUserIDAndID(ctx, user.ChatID, user.DefaultStorageID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve user default storage %d: %w", user.DefaultStorageID, err)
	}
	defaultDirPath := ""
	if user.DefaultDirID != 0 {
		dir, err := database.GetDirByID(ctx, user.DefaultDirID)
		if err != nil {
			logger.Warnf("Failed to get default dir for user %d: %v, using root", user.ChatID, err)
		} else {
			defaultDirPath = dir.Path
		}
	}

	if watch.StorageID != 0 || watch.DirPath != "" {
		// Explicit override: the rule layer never runs. The stored dir was
		// sanitized at creation; sanitize again at join time as defense in
		// depth against out-of-band database edits.
		dirPath := ""
		if watch.DirPath != "" {
			sanitized, err := dirutil.SanitizeRelDirPath(watch.DirPath)
			if err != nil {
				logger.Warnf("Stored watch dir %q failed sanitization, using root: %v", watch.DirPath, err)
			} else {
				dirPath = sanitized
			}
		}
		if watch.StorageID != 0 {
			stor, err := storage.GetStorageByUserIDAndID(ctx, user.ChatID, watch.StorageID)
			if err != nil {
				// Fall back to the user default; the owner hears about it
				// once. The save still happens, only the destination moved.
				logger.Warnf("Watch storage %d no longer resolves for user %d, falling back to the default storage: %v", watch.StorageID, user.ChatID, err)
				notifyWatchStorageLost(ctx, botCtx, watch, user)
				return defaultStor, dirPath, nil
			}
			return stor, dirPath, nil
		}
		// Dir-only override keeps the default storage.
		return defaultStor, dirPath, nil
	}

	// No override: rules, then user defaults (v1 semantics).
	ruleStorage := ""
	ruleDir := ""
	if user.ApplyRule && user.Rules != nil {
		matched, storName, dirP := ruleutil.ApplyRule(ctx, user.Rules, ruleutil.NewInput(ruleInput))
		if matched {
			if storName.Usable() {
				ruleStorage = storName.String()
			}
			// A NEW-FOR-ALBUM sentinel resolves to the base dir: the album
			// subfolder itself is appended by the grouping below.
			if !dirP.NeedNewForAlbum() {
				ruleDir = dirP.String()
			}
		}
	}
	spec := resolveWatchTarget("", "", ruleStorage, ruleDir, defaultStor.Name(), defaultDirPath)
	if spec.StorageName == defaultStor.Name() {
		return defaultStor, spec.DirPath, nil
	}
	stor, err := storage.GetStorageByUserIDAndName(ctx, user.ChatID, spec.StorageName)
	if err != nil {
		return nil, "", err
	}
	return stor, spec.DirPath, nil
}

// notifyWatchStorageLost tells the watch owner once that their per-watch
// storage override stopped resolving and saves now land in the default
// storage.
func notifyWatchStorageLost(ctx *ext.Context, botCtx *ext.Context, watch *database.WatchChat, user *database.User) {
	if botCtx == nil {
		return
	}
	if _, loaded := watchStorageLostNotified.LoadOrStore(watch.ID, struct{}{}); loaded {
		return
	}
	sendCtx := newBotContext(ctx, botCtx)
	text, entities, err := tgutil.RenderHTML(i18n.T(i18nk.BotMsgWatchInfoStorageLost, tgutil.EscapeHTMLTemplateData(map[string]any{
		"Chat":   watch.ChatID,
		"Stored": watch.StorageID,
	})))
	if err != nil {
		log.FromContext(ctx).Errorf("Failed to render watch storage-lost notice: %s", err)
		return
	}
	if _, err := sendCtx.SendMessage(user.ChatID, &tg.MessagesSendMessageRequest{
		Message:  text,
		Entities: entities,
	}); err != nil {
		log.FromContext(ctx).Errorf("Failed to deliver watch storage-lost notice to user %d: %v", user.ChatID, err)
	}
}

// processWatchSingle enqueues one non-grouped media message as a single
// task. The cursor advances only after AddTask succeeds — the task record
// is persisted inside AddTask, so that is the durability boundary. It
// returns whether the message reached a resolved outcome (durably enqueued
// or dedup-skipped): a false return means the message stays re-fetchable.
func processWatchSingle(ctx *ext.Context, botCtx *ext.Context, watch *database.WatchChat, user *database.User, file tfile.TGFileMessage, opts watchIntakeOpts) bool {
	logger := log.FromContext(ctx)
	stor, dirPath, err := resolveWatchFlushTarget(ctx, botCtx, watch, user, file)
	if err != nil {
		logger.Errorf("Failed to resolve watch target for user %d: %v", user.ChatID, err)
		return false
	}
	// Creation-time content dedup, mirroring the album path (overwrite
	// disables it): a hit means the content is already saved — a resolved
	// outcome the cursor may advance past.
	if watchConflictStrategy(user, opts.strategyPin) != tcbdata.ConflictStrategyOverwrite &&
		shortcut.DedupSkipAtCreation(ctx, user.ID, stor, file) {
		if file.Message() != nil {
			_ = database.TouchWatchCursor(ctx, watch.ID, int64(file.Message().ID))
		}
		if opts.counters != nil {
			opts.counters.singles++
		}
		return true
	}
	storagePath := path.Join(dirPath, file.Name())
	injectCtx := tgutil.ExtWithContext(ctx.Context, ctx)
	taskid := xid.New().String()
	task, err := coretfile.NewTGFileTask(taskid, injectCtx, file, stor, storagePath, newWatchNotifyProgress(ctx, botCtx, user), user.ID)
	if err != nil {
		logger.Errorf("create task failed: %s", err)
		return false
	}
	if err := core.AddTask(injectCtx, task); err != nil {
		logger.Errorf("add task failed: %s", err)
		return false
	}
	if file.Message() != nil {
		_ = database.TouchWatchCursor(ctx, watch.ID, int64(file.Message().ID))
	}
	if opts.counters != nil {
		opts.counters.singles++
	}
	logger.Infof("Added media message task for user %d in chat %d: %s", watch.UserID, watch.ChatID, file.Name())
	return true
}

// processWatchMediaGroup flushes one buffered watch media group: the msgre
// filter is evaluated once per group (only one album member carries the
// caption), then the group is enqueued as ONE batch task routed by the
// watch target — a real album send on batch-capable (Telegram) storage, a
// first-file-named subfolder everywhere else. The cursor advances per group
// only after the batch task is durably enqueued.
func processWatchMediaGroup(ctx *ext.Context, botCtx *ext.Context, watch *database.WatchChat, user *database.User, files []tfile.TGFileMessage, opts watchIntakeOpts) {
	logger := log.FromContext(ctx)
	if len(files) == 0 {
		return
	}
	files = mediautil.ApplyAlbumCaptions(ctx, user, files)
	groups := make(map[int64][]tfile.TGFileMessage)
	groupIDs := make([]int64, 0, len(files))
	for _, file := range files {
		groupID, isGroup := file.Message().GetGroupedID()
		if !isGroup || groupID == 0 {
			// Defensive: only grouped files enter the buffer.
			if processWatchSingle(ctx, botCtx, watch, user, file, opts) && opts.counters != nil {
				opts.counters.albumResolved++
			}
			continue
		}
		if _, ok := groups[groupID]; !ok {
			groupIDs = append(groupIDs, groupID)
		}
		groups[groupID] = append(groups[groupID], file)
	}
	regex := watchFilterRegexFor(watch)
	if watch.MessageFilter != "" && regex == nil {
		logger.Warnf("Invalid filter format in chat %d, dropping %d buffered files", watch.ChatID, len(files))
		// A stored filter that no longer compiles drops deterministically on
		// every re-fetch — resolved, so catch-up is not stalled forever by
		// corrupt data that creation-time validation already rejects.
		if opts.counters != nil {
			for _, f := range files {
				if gid, grouped := f.Message().GetGroupedID(); grouped && gid != 0 {
					opts.counters.albumResolved++
				}
			}
		}
		return
	}
	for _, groupID := range groupIDs {
		groupFiles := groups[groupID]
		if watch.MessageFilter != "" && !groupMatchesFilter(regex, groupFiles) {
			// Filtered-out groups are resolved: the regex is deterministic,
			// re-fetching would reject the same album again.
			if opts.counters != nil {
				opts.counters.albumResolved += len(groupFiles)
			}
			continue
		}
		stor, dirPath, err := resolveWatchFlushTarget(ctx, botCtx, watch, user, groupFiles[0])
		if err != nil {
			logger.Errorf("Failed to resolve watch target for user %d: %v", user.ChatID, err)
			continue
		}
		strategy := watchConflictStrategy(user, opts.strategyPin)

		// Batch-capable storages (Telegram) plan the album from the items'
		// native group keys — flat item paths. Everywhere else each element
		// saves individually, so the fork's album subfolder naming applies.
		_, batchCapable := stor.(storage.StorageBatchSaver)
		albumDir := ""
		if !batchCapable {
			albumDir = strings.TrimSuffix(path.Base(groupFiles[0].Name()), path.Ext(groupFiles[0].Name()))
		}

		// Creation-time guards, the same layers as the interactive /save
		// batch but silent: content dedup (except overwrite, which disables
		// it) and the skip strategy's exists check. Conflicts never prompt.
		injectCtx := tgutil.ExtWithContext(ctx.Context, ctx)
		if strategy == tcbdata.ConflictStrategyOverwrite {
			injectCtx = storage.WithOverwrite(injectCtx)
		}
		elems := make([]batchtfile.TaskElement, 0, len(groupFiles))
		skipped := make([]string, 0, len(groupFiles))
		for _, gf := range groupFiles {
			if strategy != tcbdata.ConflictStrategyOverwrite && shortcut.DedupSkipAtCreation(ctx, user.ID, stor, gf) {
				skipped = append(skipped, gf.Name())
				continue
			}
			itemPath := path.Join(dirPath, gf.Name())
			if albumDir != "" {
				itemPath = path.Join(dirPath, albumDir, gf.Name())
			}
			if strategy == tcbdata.ConflictStrategySkip {
				exists, err := stor.Exists(ctx, itemPath)
				if err != nil {
					logger.Errorf("Failed to check storage path %s: %s", itemPath, err)
					continue
				}
				if exists {
					skipped = append(skipped, gf.Name())
					continue
				}
			}
			elem, err := batchtfile.NewTaskElement(stor, itemPath, gf)
			if err != nil {
				logger.Errorf("Failed to create album element: %s", err)
				continue
			}
			elems = append(elems, *elem)
		}
		flushed := false
		if len(elems) == 0 {
			// Every member filtered (deduped/skipped) — a resolved outcome:
			// the content already exists or the user chose to skip it.
			flushed = true
		} else if len(elems) == 1 {
			// A media group reduced to one member is saved as a plain file
			// — an album send needs at least two items.
			elem := elems[0]
			task, err := coretfile.NewTGFileTask(xid.New().String(), injectCtx, elem.File, stor, path.Join(dirPath, elem.File.Name()), newWatchNotifyProgress(ctx, botCtx, user), user.ID)
			if err != nil {
				logger.Errorf("create task failed for album file: %s", err)
			} else if err := core.AddTask(injectCtx, task); err != nil {
				logger.Errorf("add task failed for album file: %s", err)
			} else {
				flushed = true
				if opts.counters != nil {
					opts.counters.singles++
				}
			}
		} else {
			task := batchtfile.NewBatchTGFileTask(xid.New().String(), injectCtx, elems, newBatchWatchNotifyProgress(ctx, botCtx, user, skipped), true, user.ID)
			if err := core.AddTask(injectCtx, task); err != nil {
				logger.Errorf("Failed to add watch album batch task: %s", err)
			} else {
				flushed = true
				if opts.counters != nil {
					opts.counters.albumFiles += len(groupFiles)
					opts.counters.albumTasks++
				}
				logger.Infof("Added watch album batch task for user %d in chat %d: %d files (skipped %d)", watch.UserID, watch.ChatID, len(elems), len(skipped))
			}
		}
		// Cursor advances only past a durably enqueued (or fully resolved)
		// group — a failed AddTask must leave the group for the next run.
		if flushed {
			if opts.counters != nil {
				opts.counters.albumResolved += len(groupFiles)
			}
			var maxMsgID int64
			for _, gf := range groupFiles {
				if id := int64(gf.Message().ID); id > maxMsgID {
					maxMsgID = id
				}
			}
			_ = database.TouchWatchCursor(ctx, watch.ID, maxMsgID)
		}
	}
}
