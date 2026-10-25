package database

import (
	"context"
	"fmt"

	"github.com/charmbracelet/log"
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

// ListAllWatchChats returns every watch row, for the boot catch-up walk.
func ListAllWatchChats(ctx context.Context) ([]*WatchChat, error) {
	var watchChats []*WatchChat
	err := db.WithContext(ctx).Find(&watchChats).Error
	if err != nil {
		return nil, err
	}
	return watchChats, nil
}

// CountWatchChats reports the total number of watch rows (all users) for
// the /status readout.
func CountWatchChats(ctx context.Context) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Model(&WatchChat{}).Count(&count).Error
	return count, err
}

// TouchWatchCursor advances a watch's last-seen message ID, never lowering it:
// the guarded WHERE makes the write a no-op when a concurrent live event or a
// backfill already recorded a newer (or equal) ID. First-boot init and every
// advance funnel through this one guard.
func TouchWatchCursor(ctx context.Context, watchID uint, messageID int64) error {
	return db.WithContext(ctx).Model(&WatchChat{}).
		Where("id = ? AND (last_message_id IS NULL OR last_message_id < ?)", watchID, messageID).
		Update("last_message_id", messageID).Error
}

// CurrentWatchCursor reads a watch's cursor; ok is false while it is unset.
func CurrentWatchCursor(ctx context.Context, watchID uint) (cursor int64, ok bool, err error) {
	var row WatchChat
	if err := db.WithContext(ctx).Select("last_message_id").First(&row, watchID).Error; err != nil {
		return 0, false, err
	}
	if row.LastMessageID == nil {
		return 0, false, nil
	}
	return *row.LastMessageID, true, nil
}

// watchChatsActiveIndexDDL creates a PARTIAL unique index: among live (not
// soft-deleted) watch rows, one row per (user_id, chat_id). Raw SQL because
// no GORM tag expresses the WHERE predicate, and a full unique index would
// collide with soft-deleted rows (watch→unwatch→watch-again).
const watchChatsActiveIndexDDL = "CREATE UNIQUE INDEX IF NOT EXISTS idx_watch_chats_user_chat_active ON watch_chats(user_id, chat_id) WHERE deleted_at IS NULL"

// DedupWatchChats removes live duplicate (user_id, chat_id) rows so the
// active-watches unique index can be created on upgrade. Among duplicates the
// row with the highest cursor survives (ties: the newest id) — the guarded
// cursor never lowers, so the survivor keeps every duplicate's backfill
// progress. Soft-deleted rows are skipped, matching the partial index.
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
// partial unique index (a no-op once it exists). Init calls it on every boot.
func EnsureWatchChatUniqueIndex(ctx context.Context) error {
	removed, err := DedupWatchChats(ctx)
	if err != nil {
		return fmt.Errorf("dedup watch chats: %w", err)
	}
	if removed > 0 {
		log.FromContext(ctx).Warnf("Removed %d duplicate watch row(s) before ensuring the unique index", removed)
	}
	return db.WithContext(ctx).Exec(watchChatsActiveIndexDDL).Error
}
