package shortcut

import (
	"context"

	"github.com/charmbracelet/log"
	"github.com/krau/SaveAny-Bot/database"
)

// DedupUserIDForChat resolves the users.id that scopes content dedup for a
// task created in the given chat. 0 (dedup off) on any miss: an unresolvable
// scope must never be guessed, or saves would land in the wrong ledger
// partition.
func DedupUserIDForChat(ctx context.Context, chatID int64) uint {
	user, err := database.GetUserByChatID(ctx, chatID)
	if err != nil {
		log.FromContext(ctx).Debugf("No user for chat %d; content dedup disabled for it: %v", chatID, err)
		return 0
	}
	return user.ID
}
