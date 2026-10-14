package database

import (
	"context"
	"time"

	"github.com/charmbracelet/log"
	"gorm.io/gorm/clause"
)

// TelegramSavedFile indexes one file this bot has saved through a telegram
// storage. Telegram offers no way to ask whether a file already exists in a
// chat, so conflict detection and skip-on-recovery for that backend ride on
// this ledger instead. Files saved before the ledger existed, or placed into
// the chat by hand, are invisible to it.
type TelegramSavedFile struct {
	ID          uint   `gorm:"primaryKey"`
	StorageName string `gorm:"uniqueIndex:idx_telegram_saved_files"`
	StoragePath string `gorm:"uniqueIndex:idx_telegram_saved_files"`
	CreatedAt   time.Time
}

// RecordTelegramSaved marks paths as saved through the named telegram
// storage. Duplicate rows are ignored, so repeated recordings are safe.
func RecordTelegramSaved(ctx context.Context, storageName string, paths ...string) error {
	if db == nil || len(paths) == 0 {
		return nil
	}
	rows := make([]TelegramSavedFile, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		rows = append(rows, TelegramSavedFile{StorageName: storageName, StoragePath: p})
	}
	if len(rows) == 0 {
		return nil
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
		log.FromContext(ctx).Warnf("Failed to record telegram saved files: %v", err)
		return err
	}
	return nil
}

// TelegramSavedExists reports whether the exact path was saved through the
// named telegram storage before. A database failure reports false: a false
// negative may re-upload a duplicate, but never blocks a legitimate save.
func TelegramSavedExists(ctx context.Context, storageName, storagePath string) bool {
	if db == nil || storagePath == "" {
		return false
	}
	var count int64
	if err := db.WithContext(ctx).Model(&TelegramSavedFile{}).
		Where("storage_name = ? AND storage_path = ?", storageName, storagePath).
		Limit(1).Count(&count).Error; err != nil {
		log.FromContext(ctx).Warnf("Failed to query telegram saved files: %v", err)
		return false
	}
	return count > 0
}
