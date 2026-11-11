package database

import (
	"context"
	"sync"

	"github.com/charmbracelet/log"
	"gorm.io/gorm/clause"

	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
)

// dedupNoUserWarned once-guards the INFO log for user-less tasks: the HTTP
// API and non-Telegram task types have no user, and their saves must not
// share a user_id=0 dedup namespace — dedup is silently disabled for them,
// and no user_id=0 row can ever exist because every writer gates on
// dedupEnabled.
var dedupNoUserWarned sync.Once

// dedupEnabled reports whether dedup checks and records apply to this task
// scope. userID is the users.id of the requesting user; 0 (API-created and
// non-Telegram tasks) disables dedup entirely — intended behavior.
func dedupEnabled(ctx context.Context, userID uint) bool {
	if userID == 0 {
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
// already saved to the storage by the user. Fail-open: a database error
// reports false — a false negative may re-upload a duplicate, but never
// blocks a legitimate save.
func DedupHasFileID(ctx context.Context, userID uint, storageID uint, fileUniqueID string) bool {
	if fileUniqueID == "" || !dedupEnabled(ctx, userID) {
		return false
	}
	var count int64
	if err := db.WithContext(ctx).Model(&SavedFileDedup{}).
		Where("user_id = ? AND storage_id = ? AND file_unique_id = ? AND file_unique_id != ''", userID, storageID, fileUniqueID).
		Limit(1).Count(&count).Error; err != nil {
		log.FromContext(ctx).Warnf("Failed to query dedup by file-id: %v", err)
		return false
	}
	return count > 0
}

// DedupHasHash reports whether content with this sha256 was already saved to
// the storage by the user. Fail-open like DedupHasFileID.
func DedupHasHash(ctx context.Context, userID uint, storageID uint, contentHash string) bool {
	if contentHash == "" || !dedupEnabled(ctx, userID) {
		return false
	}
	var count int64
	if err := db.WithContext(ctx).Model(&SavedFileDedup{}).
		Where("user_id = ? AND storage_id = ? AND content_hash = ? AND content_hash != ''", userID, storageID, contentHash).
		Limit(1).Count(&count).Error; err != nil {
		log.FromContext(ctx).Warnf("Failed to query dedup by hash: %v", err)
		return false
	}
	return count > 0
}

// RecordDedup indexes a saved file in the dedup ledger. Recording the same
// file twice is a no-op, not a duplicate row (OnConflict DoNothing against
// the migration-2 partial unique indexes).
//
// Recording is additionally gated on storageID > 0: a task whose storage
// could not be resolved to a database record must never write ledger rows —
// they would be scoped to a nonexistent storage and poison future checks.
func RecordDedup(ctx context.Context, userID uint, storageID uint, fileUniqueID, contentHash string, size int64, storagePath string) error {
	return RecordDedupWithSource(ctx, userID, storageID, fileUniqueID, contentHash, size, storagePath, 0, 0)
}

// RecordDedupWithSource is RecordDedup with the Telegram source message
// attached (0,0 = unknown), so /history can show where a file came from.
func RecordDedupWithSource(ctx context.Context, userID uint, storageID uint, fileUniqueID, contentHash string, size int64, storagePath string, chatID int64, messageID int64) error {
	if !dedupEnabled(ctx, userID) || storageID == 0 {
		return nil
	}
	if fileUniqueID == "" && contentHash == "" {
		return nil
	}
	row := SavedFileDedup{
		UserID:       userID,
		StorageID:    storageID,
		FileUniqueID: fileUniqueID,
		ContentHash:  contentHash,
		Size:         size,
		StoragePath:  storagePath,
		ChatID:       chatID,
		MessageID:    messageID,
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		log.FromContext(ctx).Warnf("Failed to record dedup entry: %v", err)
		return err
	}
	return nil
}

// DedupCheckHash hashes a downloaded cache file and reports whether identical
// content was already saved to the storage by the user. The hash is returned
// alongside so the caller can record it after a successful upload without
// hashing twice.
func DedupCheckHash(ctx context.Context, userID uint, storageID uint, localPath string) (skip bool, hash string, err error) {
	if !dedupEnabled(ctx, userID) {
		return false, "", nil
	}
	hash, err = fsutil.Sha256File(localPath)
	if err != nil {
		return false, "", err
	}
	return DedupHasHash(ctx, userID, storageID, hash), hash, nil
}

// DedupSkipByHash is the pre-upload content-hash gate shared by the save
// tasks: it hashes the downloaded cache file, logs the outcome with the
// standard fail-open wording, and returns skip=true when the save should be
// dropped. On a miss the hash is returned so the caller can record it after a
// successful upload without hashing twice. userID==0 is a no-op, keeping the
// "Task has no user ID" log out of packages that never dedup.
func DedupSkipByHash(ctx context.Context, logger *log.Logger, userID uint, storageID uint, localPath, displayPath string) (skip bool, hash string) {
	if userID == 0 {
		return false, ""
	}
	skip, hash, err := DedupCheckHash(ctx, userID, storageID, localPath)
	switch {
	case err != nil:
		// Fail-open: an unreadable file or ledger error never blocks the save.
		logger.Warnf("dedup hash check failed (fail-open, continuing upload): %v", err)
		return false, ""
	case skip:
		logger.Infof("dedup skip (hash sha256:%s…): already saved to storage %d, path %s", hash[:min(12, len(hash))], storageID, displayPath)
		return true, ""
	}
	return false, hash
}

// RecordDedupAfterUpload records a genuinely uploaded file in the dedup
// ledger, logging a failure as an error: the upload already happened, so
// at-least-once semantics say the next identical save may create a duplicate.
// User-less tasks and files with neither a file-id nor a hash are no-ops.
func RecordDedupAfterUpload(ctx context.Context, logger *log.Logger, userID uint, storageID uint, fileUniqueID, contentHash string, size int64, storagePath string) {
	RecordDedupAfterUploadWithSource(ctx, logger, userID, storageID, fileUniqueID, contentHash, size, storagePath, 0, 0)
}

// RecordDedupAfterUploadWithSource is RecordDedupAfterUpload with the
// Telegram source message attached (0,0 = unknown).
func RecordDedupAfterUploadWithSource(ctx context.Context, logger *log.Logger, userID uint, storageID uint, fileUniqueID, contentHash string, size int64, storagePath string, chatID int64, messageID int64) {
	if userID == 0 || storageID == 0 || (fileUniqueID == "" && contentHash == "") {
		return
	}
	if err := RecordDedupWithSource(ctx, userID, storageID, fileUniqueID, contentHash, size, storagePath, chatID, messageID); err != nil {
		logger.Errorf("dedup record failed after upload (at-least-once duplicate risk): %v", err)
	}
}

// PurgeDedupByStorage drops every dedup row the user has for the storage,
// returning the number of rows removed. Used after the user clears a storage
// chat by hand: the ledger would otherwise keep blocking re-saves of content
// it believes is already there.
func PurgeDedupByStorage(ctx context.Context, userID uint, storageID uint) (int64, error) {
	if db == nil {
		return 0, nil
	}
	res := db.WithContext(ctx).
		Where("user_id = ? AND storage_id = ?", userID, storageID).
		Delete(&SavedFileDedup{})
	if res.Error != nil {
		log.FromContext(ctx).Warnf("Failed to purge dedup rows for storage %d: %v", storageID, res.Error)
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
