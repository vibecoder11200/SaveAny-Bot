package database

import (
	"context"
	"fmt"

	"github.com/charmbracelet/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (user *User) WatchChat(ctx context.Context, chat WatchChat) error {
	if len(user.WatchChats) == 0 {
		user.WatchChats = make([]WatchChat, 0)
	}

	user.WatchChats = append(user.WatchChats, chat)
	return db.WithContext(ctx).Save(user.WatchChats).Error
}

func (user *User) UnwatchChat(ctx context.Context, chatID int64) error {
	var watchChat WatchChat
	err := db.WithContext(ctx).Where("chat_id = ? AND user_id = ?", chatID, user.ID).First(&watchChat).Error
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Unscoped().Delete(&watchChat).Error
}

func (user *User) WatchingChat(ctx context.Context, chatID int64) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Model(&WatchChat{}).Where("chat_id = ? AND user_id = ?", chatID, user.ID).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func GetWatchChatsByChatID(ctx context.Context, chatID int64) ([]*WatchChat, error) {
	var watchChats []*WatchChat
	err := db.WithContext(ctx).Where("chat_id = ?", chatID).Find(&watchChats).Error
	if err != nil {
		return nil, err
	}
	return watchChats, nil
}

// CountWatchChats returns the number of configured watches; used by the
// /status readout.
func CountWatchChats(ctx context.Context) (int64, error) {
	var n int64
	if err := db.WithContext(ctx).Model(&WatchChat{}).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// ListAllWatchChats returns every configured watch across users; the boot
// catch-up walks it.
func ListAllWatchChats(ctx context.Context) ([]*WatchChat, error) {
	var watches []*WatchChat
	if err := db.WithContext(ctx).Find(&watches).Error; err != nil {
		return nil, err
	}
	return watches, nil
}

// TouchWatchCursor advances the observed-message cursor of a watch. The
// write is guarded: it never lowers an existing cursor, so late delivery,
// catch-up pages and live events cannot move it backwards. An unset cursor
// (nil) is initialized with msgID.
func TouchWatchCursor(ctx context.Context, watchID uint, msgID int64) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var watch WatchChat
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", watchID).First(&watch).Error; err != nil {
			return err
		}
		if watch.LastMessageID != nil && *watch.LastMessageID >= msgID {
			return nil
		}
		watch.LastMessageID = &msgID
		return tx.Model(&WatchChat{}).Where("id = ?", watchID).
			Update("last_message_id", msgID).Error
	})
}

// CurrentWatchCursor reports the watch's observed-message cursor; ok is
// false when the cursor was never set.
func CurrentWatchCursor(ctx context.Context, watchID uint) (int64, bool, error) {
	var watch WatchChat
	if err := db.WithContext(ctx).Select("last_message_id").
		Where("id = ?", watchID).First(&watch).Error; err != nil {
		return 0, false, err
	}
	if watch.LastMessageID == nil {
		return 0, false, nil
	}
	return *watch.LastMessageID, true, nil
}

// watchChatsActiveIndexDDL creates a PARTIAL unique index: among live (not
// soft-deleted) watch rows, one row per (user_id, chat_id). Raw SQL because
// no GORM tag expresses the WHERE predicate, and a full unique index would
// collide with soft-deleted rows (watch→unwatch→watch-again).
const watchChatsActiveIndexDDL = "CREATE UNIQUE INDEX IF NOT EXISTS idx_watch_chats_user_chat_active ON watch_chats(user_id, chat_id) WHERE deleted_at IS NULL"

// DedupWatchChats removes live duplicate (user_id, chat_id) rows so the
// active-watches unique index can be created on upgrade. Among duplicates
// the row with the highest cursor survives (ties: the newest id) — the
// guarded cursor never lowers, so the survivor keeps every duplicate's
// backfill progress. Soft-deleted rows are skipped, matching the partial
// index.
func DedupWatchChats(ctx context.Context) (removed int, err error) {
	var rows []WatchChat
	if err := db.WithContext(ctx).Order("user_id, chat_id, id").Find(&rows).Error; err != nil {
		return 0, err
	}
	type pairKey struct {
		user uint
		chat int64
	}
	type survivor struct {
		id     uint
		cursor int64
	}
	best := make(map[pairKey]survivor)
	for _, row := range rows {
		cursor := int64(0)
		if row.LastMessageID != nil {
			cursor = *row.LastMessageID
		}
		key := pairKey{row.UserID, row.ChatID}
		kept, seen := best[key]
		if !seen || cursor > kept.cursor || (cursor == kept.cursor && row.ID > kept.id) {
			best[key] = survivor{row.ID, cursor}
		}
	}
	keep := make(map[uint]bool, len(best))
	for _, kept := range best {
		keep[kept.id] = true
	}
	for _, row := range rows {
		if keep[row.ID] {
			continue
		}
		// Hard delete: these rows are garbage by definition, and leaving a
		// soft-deleted corpse serves no query in the codebase.
		if err := db.WithContext(ctx).Unscoped().Delete(&WatchChat{}, row.ID).Error; err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// EnsureWatchChatUniqueIndex is the boot-time migration behind the
// (user_id, chat_id) invariant: sweep legacy duplicates, then create the
// partial unique index (a no-op once it exists). Init calls it on every
// boot, and the v0→v1 importer after seeding imported watches.
func EnsureWatchChatUniqueIndex(ctx context.Context) error {
	removed, err := DedupWatchChats(ctx)
	if err != nil {
		return fmt.Errorf("dedup watch chats: %w", err)
	}
	if removed > 0 {
		log.FromContext(ctx).Warnf("Removed %d duplicate watch row(s) before ensuring the unique index", removed)
	}
	// Upstream beta.5 carried a FULL unique index on (user_id, chat_id);
	// upgraded installs keep it, and it collides with soft-deleted rows on
	// watch→unwatch→watch-again. Drop it in favor of the partial index.
	if err := db.WithContext(ctx).Exec("DROP INDEX IF EXISTS idx_watch_user_chat").Error; err != nil {
		return err
	}
	return db.WithContext(ctx).Exec(watchChatsActiveIndexDDL).Error
}
