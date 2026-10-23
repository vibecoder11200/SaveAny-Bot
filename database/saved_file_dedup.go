package database

import (
	"context"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"gorm.io/gorm/clause"

	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
)

// SavedFileDedup indexes saved file content per (user, storage): one row per
// file whose Telegram file id or sha256 content hash was recorded at save
// time. Unlike the path-based telegram_saved_files ledger, it survives
// renames and name collisions, so a re-save of identical content can be
// blocked before any download or upload happens.
type SavedFileDedup struct {
	ID           uint   `gorm:"primaryKey"`
	UserID       int64  `gorm:"index:idx_dedup_scope"`
	StorageName  string `gorm:"index:idx_dedup_scope;index:idx_dedup_fileid;index:idx_dedup_hash"`
	FileUniqueID string `gorm:"index:idx_dedup_fileid"` // "" when the source is not Telegram media (or a resend: no hash either)
	ContentHash  string `gorm:"index:idx_dedup_hash"`   // "" for resends: the bytes never touched this disk
	Size         int64
	StoragePath  string
	CreatedAt    time.Time
}

// dedupNoUserWarned once-guards the INFO log for user-less tasks: the HTTP
// API and non-Telegram task types have no user, and their saves must not
// share a user_id=0 dedup namespace — dedup is silently disabled for them.
var dedupNoUserWarned sync.Once

func dedupEnabled(ctx context.Context, userID int64) bool {
	if userID <= 0 {
		dedupNoUserWarned.Do(func() {
			log.FromContext(ctx).Info("Task has no user ID; content dedup disabled for it")
		})
		return false
	}
	if db == nil {
		return false
	}
	return true
}

// DedupHasFileID reports whether a Telegram file with this file-id was
// already saved to the named storage by the user. Fail-open: a database
// error reports false — a false negative may re-upload a duplicate, but
// never blocks a legitimate save.
func DedupHasFileID(ctx context.Context, userID int64, storageName, fileUniqueID string) bool {
	if fileUniqueID == "" || !dedupEnabled(ctx, userID) {
		return false
	}
	var count int64
	if err := db.WithContext(ctx).Model(&SavedFileDedup{}).
		Where("user_id = ? AND storage_name = ? AND file_unique_id = ? AND file_unique_id != ''", userID, storageName, fileUniqueID).
		Limit(1).Count(&count).Error; err != nil {
		log.FromContext(ctx).Warnf("Failed to query dedup by file-id: %v", err)
		return false
	}
	return count > 0
}

// DedupHasHash reports whether content with this sha256 was already saved to
// the named storage by the user. Fail-open like DedupHasFileID.
func DedupHasHash(ctx context.Context, userID int64, storageName, contentHash string) bool {
	if contentHash == "" || !dedupEnabled(ctx, userID) {
		return false
	}
	var count int64
	if err := db.WithContext(ctx).Model(&SavedFileDedup{}).
		Where("user_id = ? AND storage_name = ? AND content_hash = ? AND content_hash != ''", userID, storageName, contentHash).
		Limit(1).Count(&count).Error; err != nil {
		log.FromContext(ctx).Warnf("Failed to query dedup by hash: %v", err)
		return false
	}
	return count > 0
}

// RecordDedup indexes a saved file in the dedup ledger. Recording the same
// file twice is a no-op, not a duplicate row.
func RecordDedup(ctx context.Context, userID int64, storageName, fileUniqueID, contentHash string, size int64, storagePath string) error {
	if !dedupEnabled(ctx, userID) {
		return nil
	}
	if (fileUniqueID == "" && contentHash == "") ||
		(fileUniqueID != "" && DedupHasFileID(ctx, userID, storageName, fileUniqueID)) ||
		(contentHash != "" && DedupHasHash(ctx, userID, storageName, contentHash)) {
		return nil
	}
	row := SavedFileDedup{
		UserID:       userID,
		StorageName:  storageName,
		FileUniqueID: fileUniqueID,
		ContentHash:  contentHash,
		Size:         size,
		StoragePath:  storagePath,
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		log.FromContext(ctx).Warnf("Failed to record dedup entry: %v", err)
		return err
	}
	return nil
}

// DedupCheckHash hashes a downloaded cache file and reports whether identical
// content was already saved to the named storage by the user. The hash is
// returned alongside so the caller can record it after a successful upload
// without hashing twice.
func DedupCheckHash(ctx context.Context, userID int64, storageName, localPath string) (skip bool, hash string, err error) {
	if !dedupEnabled(ctx, userID) {
		return false, "", nil
	}
	hash, err = fsutil.Sha256File(localPath)
	if err != nil {
		return false, "", err
	}
	return DedupHasHash(ctx, userID, storageName, hash), hash, nil
}

// DedupRecordSaved records a successfully saved file in the dedup ledger.
func DedupRecordSaved(ctx context.Context, userID int64, storageName, fileUniqueID, contentHash string, size int64, storagePath string) error {
	return RecordDedup(ctx, userID, storageName, fileUniqueID, contentHash, size, storagePath)
}
