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
	// Source of the saved file for /history: 0 = unknown (legacy rows and
	// non-Telegram sources). Populated only by the TG task paths.
	ChatID    int64 `gorm:"index"`
	MessageID int
	CreatedAt time.Time
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
	return RecordDedupWithSource(ctx, userID, storageName, fileUniqueID, contentHash, size, storagePath, 0, 0)
}

// RecordDedupWithSource is RecordDedup with the Telegram source message
// attached (0,0 = unknown), so /history can show where a file came from.
func RecordDedupWithSource(ctx context.Context, userID int64, storageName, fileUniqueID, contentHash string, size int64, storagePath string, chatID int64, messageID int) error {
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

// DedupSkipByHash is the pre-upload content-hash gate shared by the save
// tasks: it hashes the downloaded cache file, logs the outcome with the
// standard fail-open wording, and returns skip=true when the save should be
// dropped. On a miss the hash is returned so the caller can record it after a
// successful upload without hashing twice. userID<=0 is a no-op, keeping the
// "Task has no user ID" log out of packages that never dedup.
func DedupSkipByHash(ctx context.Context, logger *log.Logger, userID int64, storageName, localPath, displayPath string) (skip bool, hash string) {
	if userID <= 0 {
		return false, ""
	}
	skip, hash, err := DedupCheckHash(ctx, userID, storageName, localPath)
	switch {
	case err != nil:
		// Fail-open: an unreadable file or ledger error never blocks the save.
		logger.Warnf("dedup hash check failed (fail-open, continuing upload): %v", err)
		return false, ""
	case skip:
		logger.Infof("dedup skip (hash sha256:%s…): already saved to storage %s, path %s", hash[:min(12, len(hash))], storageName, displayPath)
		return true, ""
	}
	return false, hash
}

// RecordDedupAfterUpload records a genuinely uploaded file in the dedup
// ledger, logging a failure as an error: the upload already happened, so
// at-least-once semantics say the next identical save may create a duplicate.
// User-less tasks and files with neither a file-id nor a hash are no-ops.
func RecordDedupAfterUpload(ctx context.Context, logger *log.Logger, userID int64, storageName, fileUniqueID, contentHash string, size int64, storagePath string) {
	RecordDedupAfterUploadWithSource(ctx, logger, userID, storageName, fileUniqueID, contentHash, size, storagePath, 0, 0)
}

// RecordDedupAfterUploadWithSource is RecordDedupAfterUpload with the
// Telegram source message attached (0,0 = unknown).
func RecordDedupAfterUploadWithSource(ctx context.Context, logger *log.Logger, userID int64, storageName, fileUniqueID, contentHash string, size int64, storagePath string, chatID int64, messageID int) {
	if userID <= 0 || (fileUniqueID == "" && contentHash == "") {
		return
	}
	if err := RecordDedupWithSource(ctx, userID, storageName, fileUniqueID, contentHash, size, storagePath, chatID, messageID); err != nil {
		logger.Errorf("dedup record failed after upload (at-least-once duplicate risk): %v", err)
	}
}

// PurgeDedupByStorage drops every dedup row the user has for the named
// storage, returning the number of rows removed. Used after the user clears
// a storage chat by hand: the ledger would otherwise keep blocking re-saves
// of content it believes is already there.
func PurgeDedupByStorage(ctx context.Context, userID int64, storageName string) (int64, error) {
	if db == nil {
		return 0, nil
	}
	res := db.WithContext(ctx).
		Where("user_id = ? AND storage_name = ?", userID, storageName).
		Delete(&SavedFileDedup{})
	if res.Error != nil {
		log.FromContext(ctx).Warnf("Failed to purge dedup rows for storage %s: %v", storageName, res.Error)
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
