package handlers

import (
	"regexp"
	"strings"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/dirutil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/mediautil"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/msgelem"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/shortcut"
	"github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/utils/strutil"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/tcbdata"
	"github.com/krau/SaveAny-Bot/pkg/tfile"

	"github.com/krau/SaveAny-Bot/storage"
)

func handleSaveCmd(ctx *ext.Context, update *ext.Update) error {
	logger := log.FromContext(ctx)
	args := strings.Split(update.EffectiveMessage.Text, " ")
	if len(args) >= 3 {
		return handleBatchSave(ctx, update, args[1:])
	}
	replyTo := update.EffectiveMessage.ReplyToMessage
	if replyTo == nil || replyTo.Message == nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgSaveHelpText)), nil)
		return dispatcher.EndGroups
	}
	userDB, err := database.GetUserByChatID(ctx, update.GetUserChat().GetID())
	if err != nil {
		return err
	}
	opts := mediautil.TfileOptions(ctx, userDB, replyTo.Message)
	if len(args) > 1 {
		opts = append(opts, tfile.WithName(strings.Join(args[1:], " ")))
	}
	msg, file, err := shortcut.GetFileFromMessageWithReply(ctx, update, replyTo.Message, opts...)
	if err != nil {
		return err
	}
	userId := update.GetUserChat().GetID()
	stors := storage.GetUserStorages(ctx, userId)
	req, err := msgelem.BuildAddOneSelectStorageMessage(ctx, stors, file, msg.ID)
	if err != nil {
		logger.Errorf("Failed to build storage selection message: %s", err)
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorBuildStorageSelectMessageFailed, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	ctx.EditMessage(update.EffectiveChat().GetID(), req)
	return dispatcher.EndGroups
}

func handleSilentSaveReplied(ctx *ext.Context, update *ext.Update) error {
	args := strings.Split(string(update.EffectiveMessage.Text), " ")
	if len(args) >= 3 {
		return handleBatchSave(ctx, update, args[1:])
	}
	stor := storage.FromContext(ctx)
	replyTo := update.EffectiveMessage.ReplyToMessage
	if replyTo == nil || replyTo.Message == nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgSaveHelpText)), nil)
		return dispatcher.EndGroups
	}
	userDB, err := database.GetUserByChatID(ctx, update.GetUserChat().GetID())
	if err != nil {
		return err
	}
	opts := mediautil.TfileOptions(ctx, userDB, replyTo.Message)
	if len(args) > 1 {
		opts = append(opts, tfile.WithName(strings.Join(args[1:], " ")))
	}
	msg, file, err := shortcut.GetFileFromMessageWithReply(ctx, update, replyTo.Message, opts...)
	if err != nil {
		return err
	}
	return shortcut.CreateAndAddTGFileTaskWithEdit(ctx, update.GetUserChat().GetID(), stor, dirutil.PathFromContext(ctx), file, msg.GetID())
}

func handleBatchSave(ctx *ext.Context, update *ext.Update, args []string) error {
	chatArg := args[0]
	msgIdRangeArg := args[1]
	// Grammar: /save <chat> <range> [t:types] [regex]
	// An argument starting with "t:" is always a media type filter (even a
	// broken one — it errors out instead of falling back to regex), the
	// optional next argument is the caption/filename regex, exactly like the
	// legacy third argument.
	var filterStr string
	var filter *regexp.Regexp
	var mediaFilter mediautil.MediaFilter
	if len(args) > 2 && mediautil.IsMediaFilterSpec(args[2]) {
		var err error
		mediaFilter, err = mediautil.ParseMediaFilter(strings.TrimPrefix(args[2], "t:"))
		if err != nil {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidMediaTypeFilter, map[string]any{"Error": err.Error()})), nil)
			return dispatcher.EndGroups
		}
		if mediaFilter == nil {
			// A bare "t:" or "t:,,," names no type: an explicit filter
			// intent that fails closed instead of silently falling back to
			// the user's default filter.
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidMediaTypeFilter, map[string]any{"Error": "no media type given (t: is empty)"})), nil)
			return dispatcher.EndGroups
		}
		if len(args) > 3 {
			filterStr = args[3]
		}
	} else if len(args) > 2 {
		filterStr = args[2]
	}
	if filterStr != "" {
		var err error
		filter, err = regexp.Compile(filterStr)
		if err != nil {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidRegex, map[string]any{"Error": err.Error()})), nil)
			return dispatcher.EndGroups
		}
	}
	startID, endID, err := strutil.ParseIntStrRange(msgIdRangeArg, "-")
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidMsgIdRange, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	// Prefer the userbot for source resolution and fetching: it reaches
	// every chat the user's account can see, while the bot only reads
	// channels it administers (a @username source therefore returns zero
	// messages when routed through the bot).
	tctx := ctx
	if uctx := user.GetCtx(); uctx != nil {
		tctx = uctx
	}
	chatID, err := tgutil.ParseChatID(tctx, chatArg)
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorInvalidIdOrUsername, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}

	// Priority: an explicit t: filter on the command > the user's default
	// media filter (set via /config) > no filtering.
	if mediaFilter == nil {
		userDB, err := database.GetUserByChatID(ctx, update.GetUserChat().GetID())
		if err != nil {
			return err
		}
		mediaFilter = mediautil.UserMediaFilter(userDB)
	}

	replied, err := ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonInfoFetchingMessages)), nil)
	if err != nil {
		log.FromContext(ctx).Errorf("Failed to reply: %s", err)
		return dispatcher.EndGroups
	}

	msgs, err := tgutil.GetMessagesRange(tctx, chatID, int(startID), int(endID))
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorGetMessagesFailed, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	if len(msgs) == 0 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorNoMessagesInRange)), nil)
		return dispatcher.EndGroups
	}
	files := make([]tfile.TGFileMessage, 0, len(msgs))
	sb := strings.Builder{}
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		media, ok := msg.GetMedia()
		if !ok {
			continue
		}
		supported := mediautil.IsSupported(media)
		if !supported {
			continue
		}
		if !mediaFilter.Allows(mediautil.MediaType(media)) {
			continue
		}
		if filter != nil {
			sb.Reset()
			sb.WriteString(msg.GetMessage())
			sb.WriteString(" ")
			fn, _ := tgutil.GetMediaFileName(media)
			sb.WriteString(fn)
			if !filter.MatchString(sb.String()) {
				continue
			}
		}
		file, err := tfile.FromMediaMessage(media, tctx.Raw, msg, tfile.WithNameIfEmpty(tgutil.GenFileNameFromMessage(*msg)))
		if err != nil {
			log.FromContext(ctx).Errorf("Failed to get file from message: %s", err)
			continue
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorNoSavableMessagesInRange)), nil)
		return dispatcher.EndGroups
	}
	stor := storage.FromContext(ctx)
	if stor == nil {
		// not in silent mode
		stors := storage.GetUserStorages(ctx, update.GetUserChat().GetID())
		markup, err := msgelem.BuildAddSelectStorageKeyboard(stors, tcbdata.Add{
			Files: files,
		})
		if err != nil {
			log.FromContext(ctx).Errorf("Failed to build storage selection keyboard: %s", err)
			ctx.EditMessage(update.EffectiveChat().GetID(), &tg.MessagesEditMessageRequest{
				ID:      replied.ID,
				Message: i18n.T(i18nk.BotMsgCommonErrorBuildStorageSelectKeyboardFailed, map[string]any{"Error": err.Error()}),
			})
			return dispatcher.EndGroups
		}
		ctx.EditMessage(update.EffectiveChat().GetID(), &tg.MessagesEditMessageRequest{
			ID:          replied.ID,
			Message:     i18n.T(i18nk.BotMsgCommonInfoFoundFilesSelectStorage, map[string]any{"Count": len(files)}),
			ReplyMarkup: markup,
		})
		return dispatcher.EndGroups
	}
	return shortcut.CreateAndAddBatchTGFileTaskWithEdit(ctx, update.GetUserChat().GetID(), stor, "", files, replied.ID)
}
