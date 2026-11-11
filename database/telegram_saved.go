package database

import (
	"context"
	"time"

	"github.com/charmbracelet/log"
	"gorm.io/gorm/clause"
)

// TelegramSavedFile indexes one file this bot has saved through a telegram
// storage; see model.go. Keyed by the storage's database ID — storages are
// identified by ID everywhere in v1, names are only a console label.
//
// The gorm uniqueIndex tag on the model covers (storage_id, storage_path):
// a plain unique index is correct here because neither key is ever empty.

// RecordTelegramSaved marks paths as saved through the storage with the
// given ID. Duplicate rows are ignored, so repeated recordings are safe.
func RecordTelegramSaved(ctx context.Context, storageID uint, paths ...string) error {
	if db == nil || len(paths) == 0 {
		return nil
	}
	rows := make([]TelegramSavedFile, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		rows = append(rows, TelegramSavedFile{StorageID: storageID, StoragePath: p, CreatedAt: time.Now().UTC()})
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
// storage with the given ID before. A database failure reports false: a
// false negative may re-upload a duplicate, but never blocks a legitimate
// save (fail-open).
func TelegramSavedExists(ctx context.Context, storageID uint, storagePath string) bool {
	if db == nil || storagePath == "" {
		return false
	}
	var count int64
	if err := db.WithContext(ctx).Model(&TelegramSavedFile{}).
		Where("storage_id = ? AND storage_path = ?", storageID, storagePath).
		Limit(1).Count(&count).Error; err != nil {
		log.FromContext(ctx).Warnf("Failed to query telegram saved files: %v", err)
		return false
	}
	return count > 0
}

// PurgeTelegramSavedByStorage drops every saved-path row for the storage
// with the given ID, returning the number of rows removed. Mirrors
// PurgeDedupByStorage for the path-based ledger so a hand-cleared chat can
// be re-saved cleanly. The wipe is storage-wide on purpose (one telegram
// storage maps to one destination chat); it is owner-gated at the caller.
func PurgeTelegramSavedByStorage(ctx context.Context, storageID uint) (int64, error) {
	if db == nil {
		return 0, nil
	}
	res := db.WithContext(ctx).
		Where("storage_id = ?", storageID).
		Delete(&TelegramSavedFile{})
	if res.Error != nil {
		log.FromContext(ctx).Warnf("Failed to purge saved-file rows for storage %d: %v", storageID, res.Error)
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
