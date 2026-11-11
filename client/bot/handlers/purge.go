package handlers

import (
	"strings"

	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/settings"
	"github.com/krau/SaveAny-Bot/storage"
	"github.com/krau/mygotg/dispatcher"
	"github.com/krau/mygotg/ext"
)

// handlePurgeCmd wipes the dedup ledger of one storage for the owner's user
// scope AND the storage-wide telegram saved-path ledger, then reports both
// row counts. Blast radius: v1 storages are shared by default (an empty user
// blacklist admits every whitelisted user), so the telegram-ledger half
// affects everyone using that storage — hence the owner gate, not merely the
// whitelist gate. Run /purge after clearing a storage chat by hand: the
// ledgers would otherwise keep treating the deleted content as already
// saved and block re-runs.
func handlePurgeCmd(ctx *ext.Context, update *ext.Update) error {
	args := strings.Fields(update.EffectiveMessage.Text)
	if len(args) != 2 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgPurgeUsage)), nil)
		return dispatcher.EndGroups
	}
	chatID := responsibleUserID(update)
	if owner := settings.OwnerChatID(ctx); owner == 0 || chatID != owner {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgPurgeNotPermitted)), nil)
		return dispatcher.EndGroups
	}
	name := args[1]
	stor, err := storage.GetStorageByUserIDAndName(ctx, chatID, name)
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgPurgeErrorNotFound, map[string]any{
			"Storage": name,
		})), nil)
		return dispatcher.EndGroups
	}
	user, err := database.GetUserByChatID(ctx, chatID)
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgCommonErrorGetUserWithErrFailed, map[string]any{
			"Error": err.Error(),
		})), nil)
		return dispatcher.EndGroups
	}
	storageID := storage.ID(stor)
	dedupRows, err := database.PurgeDedupByStorage(ctx, user.ID, storageID)
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgPurgeErrorFailed, map[string]any{
			"Error": err.Error(),
		})), nil)
		return dispatcher.EndGroups
	}
	savedRows, perr := database.PurgeTelegramSavedByStorage(ctx, storageID)
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
