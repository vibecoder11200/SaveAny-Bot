package handlers

import (
	"strconv"
	"strings"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/ext"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/database"
)

// handleHistoryCmd reads the caller's saved-file ledger.
// Grammar: /history [page] | /history search <text> [p<page>] | /history stats
// The search page uses the explicit "p2" marker so a query ending in a
// plain number ("report 2024") stays part of the text. The ledger is
// per-user (WHERE user_id = caller); /purge resets it per storage, which
// the listing footer documents.
func handleHistoryCmd(ctx *ext.Context, update *ext.Update) error {
	args := strings.Fields(update.EffectiveMessage.Text)
	userID := update.GetUserChat().GetID()

	if len(args) >= 2 && args[1] == "stats" {
		return showHistoryStats(ctx, update, userID)
	}
	if len(args) >= 2 && args[1] == "search" {
		query, page := historySearchArgs(args[2:])
		if query == "" {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgHistoryUsage)), nil)
			return dispatcher.EndGroups
		}
		return showHistoryList(ctx, update, userID, query, page)
	}
	page := 1
	if len(args) >= 2 {
		if p, ok := historyPageArg(args[1]); ok {
			page = p
		} else {
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgHistoryUsage)), nil)
			return dispatcher.EndGroups
		}
	}
	return showHistoryList(ctx, update, userID, "", page)
}

// historySearchArgs splits the tail of /history search into the query text
// and page. A trailing "p<digits>" token sets the page; any other trailing
// token (including a bare number) is query text.
func historySearchArgs(args []string) (query string, page int) {
	if len(args) >= 2 {
		if p, ok := historyPageMarker(args[len(args)-1]); ok {
			return strings.Join(args[:len(args)-1], " "), p
		}
	}
	return strings.Join(args, " "), 1
}

// historyPageMarker parses a "p<digits>" page marker (p2, p10); ok is false
// for anything else, including "p0" and "p".
func historyPageMarker(token string) (page int, ok bool) {
	digits, matched := strings.CutPrefix(token, "p")
	if !matched || digits == "" {
		return 0, false
	}
	return historyPageArg(digits)
}

func showHistoryList(ctx *ext.Context, update *ext.Update, userID int64, query string, page int) error {
	rows, total, err := database.ListSavedFiles(ctx, userID, query, page)
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgHistoryErrorQuery, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	if len(rows) == 0 {
		pages := historyPageCount(total)
		switch {
		case total > 0 && page > pages:
			// Matches exist, the page just ran past the end — say so
			// instead of reporting "no matches".
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgHistoryPageOutOfRange, map[string]any{
				"Page": page, "Pages": pages,
			})), nil)
		case query == "" && total == 0:
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgHistoryEmpty)), nil)
		default:
			ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgHistoryNoMatches)), nil)
		}
		return dispatcher.EndGroups
	}
	var sb strings.Builder
	if query != "" {
		sb.WriteString(i18n.T(i18nk.BotMsgHistorySearchHeader, map[string]any{"Query": query, "Total": total}))
	} else {
		sb.WriteString(i18n.T(i18nk.BotMsgHistoryHeader, map[string]any{"Total": total}))
	}
	for _, row := range rows {
		source := i18n.T(i18nk.BotMsgHistorySourceUnknown)
		if row.ChatID != 0 {
			source = i18n.T(i18nk.BotMsgHistorySourceKnown, map[string]any{
				"Chat": row.ChatID, "Message": row.MessageID,
			})
		}
		sb.WriteString(i18n.T(i18nk.BotMsgHistoryRow, map[string]any{
			"Name":    historyRowName(row.StoragePath),
			"Storage": row.StorageName,
			"Size":    dlutil.FormatSize(row.Size),
			"Time":    row.CreatedAt.Local().Format("2006-01-02 15:04"),
			"Source":  source,
		}))
	}
	pages := historyPageCount(total)
	sb.WriteString(i18n.T(i18nk.BotMsgHistoryFooter, map[string]any{
		"Page": page, "Pages": pages, "Size": database.HistoryPageSize,
	}))
	ctx.Reply(update, ext.ReplyTextString(sb.String()), nil)
	return dispatcher.EndGroups
}

func showHistoryStats(ctx *ext.Context, update *ext.Update, userID int64) error {
	stats, err := database.GetSavedFileStats(ctx, userID)
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgHistoryErrorQuery, map[string]any{"Error": err.Error()})), nil)
		return dispatcher.EndGroups
	}
	var sb strings.Builder
	sb.WriteString(i18n.T(i18nk.BotMsgHistoryStatsHeader, map[string]any{
		"Count": stats.TotalCount, "Size": dlutil.FormatSize(stats.TotalSize),
	}))
	sb.WriteString(i18n.T(i18nk.BotMsgHistoryStatsByStorageTitle))
	for _, s := range stats.ByStorage {
		sb.WriteString(i18n.T(i18nk.BotMsgHistoryStatsStorageRow, map[string]any{
			"Storage": s.StorageName, "Count": s.Count, "Size": dlutil.FormatSize(s.Size),
		}))
	}
	if len(stats.ByDay) > 0 {
		sb.WriteString(i18n.T(i18nk.BotMsgHistoryStatsByDayTitle))
		for _, d := range stats.ByDay {
			sb.WriteString(i18n.T(i18nk.BotMsgHistoryStatsDayRow, map[string]any{
				"Day": d.Day, "Count": d.Count, "Size": dlutil.FormatSize(d.Size),
			}))
		}
	}
	sb.WriteString(i18n.T(i18nk.BotMsgHistoryStatsFooter))
	ctx.Reply(update, ext.ReplyTextString(sb.String()), nil)
	return dispatcher.EndGroups
}

// historyPageCount is the page count of a listing with total rows (fixed
// page size, rounded up).
func historyPageCount(total int64) int {
	return (int(total) + database.HistoryPageSize - 1) / database.HistoryPageSize
}

// historyPageArg parses a page-number argument; ok is false unless it is an
// integer >= 1.
func historyPageArg(token string) (page int, ok bool) {
	p, err := strconv.Atoi(token)
	if err != nil || p < 1 {
		return 0, false
	}
	return p, true
}

// historyRowName shortens a stored path to its base name for the listing.
func historyRowName(storagePath string) string {
	if storagePath == "" {
		return "?"
	}
	if i := strings.LastIndexByte(storagePath, '/'); i >= 0 {
		return storagePath[i+1:]
	}
	return storagePath
}
