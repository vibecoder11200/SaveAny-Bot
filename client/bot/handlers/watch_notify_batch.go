package handlers

import (
	"context"
	"sync"

	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/core/tasks/batchtfile"
	"github.com/krau/SaveAny-Bot/database"
)

// batchWatchNotifyProgress reports a watched album batch task through ONE
// notification message: the message is opened on first use (like the
// single-file watch notification) and then handed to the standard batch
// progress renderer, so the user sees the batch header, per-item states and
// the skipped/deduped counts in one place.
type batchWatchNotifyProgress struct {
	botCtx *ext.Context
	chatID int64
	once   sync.Once
	// inner is the standard batch tracker; its MessageID is filled in once
	// the notification message exists, after which every callback delegates.
	inner *batchtfile.Progress
}

// newBatchWatchNotifyProgress returns the tracker reporting a watched album
// task to the user, or nil when notifications are disabled — the batch
// executor nil-checks its Progress on every callback.
func newBatchWatchNotifyProgress(ctx context.Context, botCtx *ext.Context, user *database.User, skipped, deduped []string) batchtfile.ProgressTracker {
	if !user.WatchNotify || botCtx == nil {
		return nil
	}
	inner := batchtfile.NewProgressTrackerWithSkipped(0, user.ChatID, skipped, deduped).(*batchtfile.Progress)
	return &batchWatchNotifyProgress{botCtx: newBotContext(ctx, botCtx), chatID: user.ChatID, inner: inner}
}

// ensureOpened sends the notification once. Until it succeeds (or forever on
// failure) MessageID stays 0 and every callback is a no-op — the same
// fail-silent contract as the single-file watch notification.
func (p *batchWatchNotifyProgress) ensureOpened(ctx context.Context, info batchtfile.TaskInfo) {
	p.once.Do(func() {
		text, entities, err := tgutil.RenderHTML(i18n.T(i18nk.BotMsgWatchNotifyAlbumDetected, tgutil.EscapeHTMLTemplateData(map[string]any{
			"Count": info.Count(),
			"Name":  firstBatchFileName(info),
		})))
		if err != nil {
			log.FromContext(ctx).Errorf("Failed to render watch album notification: %s", err)
			return
		}
		msg, err := p.botCtx.SendMessage(p.chatID, &tg.MessagesSendMessageRequest{
			Message:  text,
			Entities: entities,
		})
		if err != nil {
			log.FromContext(ctx).Errorf("Failed to send watch album notification to user %d: %s", p.chatID, err)
			return
		}
		p.inner.MessageID = msg.ID
	})
}

// firstBatchFileName names the album after its first element, mirroring the
// album folder naming.
func firstBatchFileName(info batchtfile.TaskInfo) string {
	for _, item := range info.Items() {
		if item.Name != "" {
			return item.Name
		}
	}
	return ""
}

func (p *batchWatchNotifyProgress) OnStart(ctx context.Context, info batchtfile.TaskInfo) {
	p.ensureOpened(ctx, info)
	if p.inner.MessageID != 0 {
		p.inner.OnStart(tgutil.ExtWithContext(ctx, p.botCtx), info)
	}
}

func (p *batchWatchNotifyProgress) OnProgress(ctx context.Context, info batchtfile.TaskInfo) {
	// ensureOpened is a sync.Once: calling it on every callback keeps the
	// MessageID read properly synchronized with the single write.
	p.ensureOpened(ctx, info)
	if p.inner.MessageID != 0 {
		p.inner.OnProgress(tgutil.ExtWithContext(ctx, p.botCtx), info)
	}
}

func (p *batchWatchNotifyProgress) OnDone(ctx context.Context, info batchtfile.TaskInfo, err error) {
	p.ensureOpened(ctx, info)
	if p.inner.MessageID != 0 {
		p.inner.OnDone(tgutil.ExtWithContext(ctx, p.botCtx), info, err)
	}
}
