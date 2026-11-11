package shortcut

import (
	"os/exec"

	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"
	"github.com/krau/mygotg/dispatcher"
	"github.com/krau/mygotg/ext"
	"github.com/rs/xid"

	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/core/tasks/ytdlp"
	"github.com/krau/SaveAny-Bot/storage"
)

func CreateAndAddYtdlpTaskWithEdit(ctx *ext.Context, stor storage.Storage, dirPath string, urls []string, flags []string, msgID int, chatID int64) error {
	logger := log.FromContext(ctx)
	injectCtx := tgutil.ExtWithContext(ctx.Context, ctx)

	if len(urls) == 0 {
		logger.Error("URLs list is empty")
		ctx.EditMessage(chatID, &tg.MessagesEditMessageRequest{
			ID:      msgID,
			Message: i18n.T(i18nk.BotMsgDownloadErrorNoValidLinks, nil),
		})
		return dispatcher.EndGroups
	}

	// Surface the missing binary at task creation: in slim images (micro/
	// pico) yt-dlp is absent and the task would otherwise die mid-run with
	// an opaque exec error.
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		logger.Warn("yt-dlp not found: the task will fail; install yt-dlp or use an image that bundles it")
	}
	logger.Infof("Creating yt-dlp task for %d URL(s) with %d flag(s)", len(urls), len(flags))

	task := ytdlp.NewTask(
		xid.New().String(),
		injectCtx,
		urls,
		flags,
		stor,
		dirPath,
		ytdlp.NewProgress(msgID, chatID),
	)
	task.UserID = DedupUserIDForChat(ctx, chatID)

	if err := core.AddTask(injectCtx, task); err != nil {
		logger.Errorf("Failed to add yt-dlp task: %s", err)
		ctx.EditMessage(chatID, &tg.MessagesEditMessageRequest{
			ID: msgID,
			Message: i18n.T(i18nk.BotMsgCommonErrorTaskAddFailed, map[string]any{
				"Error": err.Error(),
			}),
		})
		return dispatcher.EndGroups
	}

	ctx.EditMessage(chatID, &tg.MessagesEditMessageRequest{
		ID:      msgID,
		Message: i18n.T(i18nk.BotMsgCommonInfoTaskAdded, nil),
	})

	return dispatcher.EndGroups
}
