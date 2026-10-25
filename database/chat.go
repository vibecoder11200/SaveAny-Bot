package database

import "context"

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
