package handlers

import (
	"strings"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/ext"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/storage"
)

// handlePurgeCmd wipes the dedup and saved-path ledgers of one storage for
// the requesting user. Run it after clearing a storage chat by hand: the
// ledgers would otherwise keep treating the deleted content as already
// saved and block re-runs.
func handlePurgeCmd(ctx *ext.Context, update *ext.Update) error {
	args := strings.Fields(string(update.EffectiveMessage.Text))
	if len(args) != 2 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgPurgeUsage, nil)), nil)
		return dispatcher.EndGroups
	}
	userID := update.EffectiveChat().GetID()
	name := args[1]
	if _, err := storage.GetStorageByUserIDAndName(ctx, userID, name); err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgPurgeErrorNotFound, map[string]any{
			"Storage": name,
		})), nil)
		return dispatcher.EndGroups
	}
	dedupRows, err := database.PurgeDedupByStorage(ctx, userID, name)
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgPurgeErrorFailed, map[string]any{
			"Error": err.Error(),
		})), nil)
		return dispatcher.EndGroups
	}
	savedRows, perr := database.PurgeTelegramSavedByStorage(ctx, name)
	note := ""
	if perr != nil {
		// The dedup ledger is already gone; report partial success honestly.
		note = i18n.T(i18nk.BotMsgPurgeErrorPartial, map[string]any{"Error": perr.Error()})
	}
	ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgPurgeInfoDone, map[string]any{
		"Storage":   name,
		"Deduped":   dedupRows,
		"SavedPath": savedRows,
		"Note":      note,
	})), nil)
	return dispatcher.EndGroups
}
