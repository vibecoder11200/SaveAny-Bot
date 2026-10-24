package handlers

import (
	"context"
	"time"

	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"
	userclient "github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/tcbdata"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage/telegram"
)

// Catch-up bounds. The per-boot cap bounds the work one restart picks up
// (older messages stay for manual /save); the wall-clock budget bounds the
// whole per-watch run — FLOOD_WAIT is already retried by the userbot's
// middleware (floodwait.NewSimpleWaiter), so NO manual flood wrapper is
// added here: stacking one would multiply the waits into an hour-long stall.
const (
	watchCatchUpCap      = 500
	watchCatchUpPageSize = 100
	watchCatchUpBudget   = 10 * time.Minute
	watchCatchUpPacing   = time.Second
)

// watchCatchUpCounters observes durable enqueues at the intake's
// durability boundaries (after AddTask succeeds), so the summary can report
// "enqueued" honestly.
type watchCatchUpCounters struct {
	singles    int
	albumFiles int
	albumTasks int
}

// watchCatchUpResult summarizes one watch's backfill run.
type watchCatchUpResult struct {
	ChatID  int64
	Fetched int
	Capped  bool
	Err     error
}

// enqueueCount reports how many files were durably enqueued (singles tasks
// plus album members inside batch tasks).
func (c *watchCatchUpCounters) enqueueCount() int {
	return c.singles + c.albumFiles
}

// historyFetchPage returns one raw getHistory batch: up to
// watchCatchUpPageSize messages NEWER than the anchor, in Telegram's
// descending order.
type historyFetchPage func(offsetID int) ([]*tg.Message, error)

// planWatchCatchUpPages walks history from the cursor forward (ascending)
// using only real message IDs as anchors — never ID arithmetic. It stops on
// a short batch (caught up), on the cap (capped=true — older messages are
// left for a later boot / manual /save), on a stalled anchor, or on a fetch
// error (returned as-is with the messages collected so far).
func planWatchCatchUpPages(fetch historyFetchPage, cursor, capMsgs int) ([]*tg.Message, bool, error) {
	return planPagesWithSize(fetch, cursor, capMsgs, watchCatchUpPageSize)
}

// planPagesWithSize is planWatchCatchUpPages with an injectable page size,
// so tests can serve small pages.
func planPagesWithSize(fetch historyFetchPage, cursor, capMsgs, pageSize int) ([]*tg.Message, bool, error) {
	var collected []*tg.Message
	capped := false
	offset := cursor
	for {
		if len(collected) >= capMsgs {
			capped = true
			break
		}
		batch, err := fetch(offset)
		if err != nil {
			return collected, capped, err
		}
		if len(batch) == 0 {
			break
		}
		// Telegram returns descending (newest first): reverse so the
		// intake sees messages in posting order.
		for i := len(batch) - 1; i >= 0; i-- {
			msg := batch[i]
			if msg == nil || msg.ID <= cursor {
				continue
			}
			collected = append(collected, msg)
		}
		if len(batch) < pageSize {
			break
		}
		newest := batch[0].ID
		if newest <= offset {
			// The anchor did not advance: stop instead of re-fetching the
			// same window forever.
			break
		}
		offset = newest
	}
	if len(collected) > capMsgs {
		collected = collected[:capMsgs]
	}
	return collected, capped, nil
}

// runWatchCatchUp backfills every watch after boot: first-boot watches get
// their cursor initialized to the newest message (NO historical backfill —
// owner decision), existing watches fetch what they missed while offline
// through the SAME intake as live events.
func runWatchCatchUp(botCtx *ext.Context) {
	uctx := userclient.GetCtx()
	if uctx == nil {
		return
	}
	logger := log.FromContext(uctx)
	watches, err := database.ListAllWatchChats(uctx)
	if err != nil {
		logger.Errorf("Watch catch-up: failed to list watches: %v", err)
		return
	}
	if len(watches) == 0 {
		return
	}
	logger.Infof("Watch catch-up: starting for %d watch(es)", len(watches))
	for _, watch := range watches {
		runWatchCatchUpOne(botCtx, uctx, watch)
		// Pace the sequential watch loop: at least 1s between getHistory
		// RPCs across watches.
		time.Sleep(watchCatchUpPacing)
	}
	logger.Info("Watch catch-up: finished")
}

func runWatchCatchUpOne(botCtx *ext.Context, uctx *ext.Context, watch *database.WatchChat) {
	logger := log.FromContext(uctx)
	peer := telegram.ResolveInputPeer(uctx, watch.ChatID)
	if peer == nil {
		logger.Warnf("Watch catch-up: no peer for chat %d, skipping watch %d", watch.ChatID, watch.ID)
		return
	}
	cursor := int64(0)
	if watch.LastMessageID != nil {
		cursor = *watch.LastMessageID
	}
	// First boot for this watch: record the current newest message as the
	// cursor and backfill nothing (guarded write — never lowers).
	if cursor == 0 {
		newest, err := fetchNewestMessageID(uctx, peer)
		if err != nil {
			logger.Warnf("Watch catch-up: failed to initialize cursor for chat %d: %v", watch.ChatID, err)
			return
		}
		if newest == 0 {
			// Empty chat: leave the cursor unset; the first live event
			// initializes it.
			return
		}
		if err := database.TouchWatchCursor(uctx, watch.ID, newest); err != nil {
			logger.Warnf("Watch catch-up: failed to store init cursor for chat %d: %v", watch.ChatID, err)
		} else {
			logger.Infof("Watch catch-up: cursor initialized to %d for chat %d (no backfill)", newest, watch.ChatID)
		}
		return
	}

	// The whole per-watch run shares one wall-clock budget: aborting
	// cleanly is always safe, because the cursor only advances on durable
	// enqueue — a budget abort simply leaves the rest for the next boot.
	ctx, cancel := context.WithTimeout(uctx, watchCatchUpBudget)
	defer cancel()

	counters := &watchCatchUpCounters{}
	fetch := func(offsetID int) ([]*tg.Message, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if offsetID != int(cursor) {
			time.Sleep(watchCatchUpPacing)
		}
		return fetchHistoryPage(ctx, uctx, peer, offsetID, cursor)
	}

	msgs, capped, err := planWatchCatchUpPages(fetch, int(cursor), watchCatchUpCap)
	if err != nil && len(msgs) == 0 {
		logger.Warnf("Watch catch-up: fetch failed for chat %d: %v", watch.ChatID, err)
		return
	}
	if err != nil {
		// Budget abort or mid-run fetch error: process what was collected,
		// then flush and summarize — the rest re-fetches next boot.
		logger.Warnf("Watch catch-up: aborted for chat %d after %d messages: %v", watch.ChatID, len(msgs), err)
	}

	fetched := 0
	for _, msg := range msgs {
		media, ok := msg.GetMedia()
		if !ok || media == nil {
			continue
		}
		switch media.(type) {
		case *tg.MessageMediaDocument, *tg.MessageMediaPhoto:
		default:
			continue
		}
		file, err := tfile.FromMediaMessage(media, uctx.Raw, msg, tfile.WithNameIfEmpty(tgutil.GenFileNameFromMessage(*msg)))
		if err != nil {
			logger.Warnf("Watch catch-up: failed to build file from message %d in chat %d: %v", msg.ID, watch.ChatID, err)
			continue
		}
		fetched++
		processMediaEventForWatch(botCtx, userclient.MediaMessageEvent{
			Ctx:       uctx,
			ChatID:    watch.ChatID,
			MessageID: msg.ID,
			File:      file,
		}, watch, watchIntakeOpts{
			live:        false,
			strategyPin: tcbdata.ConflictStrategyRename,
			counters:    counters,
		})
	}

	// Force-flush the album buffer BEFORE the summary: a straddled album
	// must not wait for a live event that may never come, and the summary's
	// enqueue counts must include the flushed albums.
	watchMediaGroupMgr.FlushChat(watch.ChatID)

	if fetched == 0 {
		// Silent when nothing new — no summary noise on quiet restarts.
		return
	}
	sendWatchCatchUpSummary(botCtx, uctx, watch, fetched, counters.enqueueCount(), counters.albumTasks, capped)
}

// fetchNewestMessageID returns the newest message ID of a chat (0 when the
// chat has no messages) — the guarded first-boot cursor init.
func fetchNewestMessageID(ctx *ext.Context, peer tg.InputPeerClass) (int64, error) {
	msgs, err := fetchHistoryPage(ctx, ctx, peer, 0, 0)
	if err != nil {
		return 0, err
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	return int64(msgs[0].ID), nil
}

// fetchHistoryPage fetches one page of history newer than minID: OffsetID
// anchors the window, AddOffset shifts it toward newer messages, and MinID
// filters anything the cursor already covers. Results arrive descending.
func fetchHistoryPage(ctx context.Context, ectx *ext.Context, peer tg.InputPeerClass, offsetID int, minID int64) ([]*tg.Message, error) {
	res, err := ectx.Raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:      peer,
		OffsetID:  offsetID,
		AddOffset: -watchCatchUpPageSize,
		Limit:     watchCatchUpPageSize,
		MinID:     int(minID),
	})
	if err != nil {
		return nil, err
	}
	var classes []tg.MessageClass
	switch m := res.(type) {
	case *tg.MessagesMessages:
		classes = m.Messages
	case *tg.MessagesMessagesSlice:
		classes = m.Messages
	case *tg.MessagesChannelMessages:
		classes = m.Messages
	case *tg.MessagesMessagesNotModified:
		return nil, nil
	}
	msgs := make([]*tg.Message, 0, len(classes))
	for _, class := range classes {
		if msg, ok := class.(*tg.Message); ok && msg != nil {
			msgs = append(msgs, msg)
		}
	}
	return msgs, nil
}

// sendWatchCatchUpSummary reports one watch's backfill result to its owner
// after the album flush. Wording says "enqueued": enqueue is the durability
// boundary — a task that later fails terminally is surfaced by the watch
// notification, not re-fetched.
func sendWatchCatchUpSummary(botCtx *ext.Context, uctx *ext.Context, watch *database.WatchChat, fetched, enqueued, albums int, capped bool) {
	logger := log.FromContext(uctx)
	user, err := database.GetUserByID(uctx, watch.UserID)
	if err != nil {
		logger.Errorf("Watch catch-up: failed to get user %d for summary: %v", watch.UserID, err)
		return
	}
	text, entities, err := tgutil.RenderHTML(i18n.T(i18nk.BotMsgWatchCatchupSummary, tgutil.EscapeHTMLTemplateData(map[string]any{
		"Chat":     watch.ChatID,
		"Fetched":  fetched,
		"Enqueued": enqueued,
		"Albums":   albums,
		"Capped":   capped,
		"Cap":      watchCatchUpCap,
	})))
	if err != nil {
		logger.Errorf("Watch catch-up: failed to render summary: %s", err)
		return
	}
	if _, err := botCtx.SendMessage(user.ChatID, &tg.MessagesSendMessageRequest{
		Message:  text,
		Entities: entities,
	}); err != nil {
		logger.Errorf("Watch catch-up: failed to send summary to user %d: %s", user.ChatID, err)
	}
}
