package database

import (
	"context"
	"strings"
	"time"
)

// HistoryPageSize is the fixed page size of /history listings.
const HistoryPageSize = 20

// SavedFileEntry is one history row: the ledger record plus the storage
// name resolved for display (empty when the storage was deleted).
type SavedFileEntry struct {
	SavedFileDedup
	StorageName string
}

// SavedFileStats aggregates a user's ledger for /history stats.
type SavedFileStats struct {
	TotalCount int64
	TotalSize  int64
	ByStorage  []SavedFileStorageStats
	ByDay      []SavedFileDayStats
}

type SavedFileStorageStats struct {
	StorageID   uint
	StorageName string
	Count       int64
	Size        int64
}

type SavedFileDayStats struct {
	Day   string // YYYY-MM-DD
	Count int64
	Size  int64
}

// historyDaysBack bounds the per-day aggregation window.
const historyDaysBack = 14

// escapeLike escapes LIKE wildcards so user search text matches literally;
// callers must pair it with ESCAPE '\'.
func escapeLike(in string) string {
	replacer := strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(in)
}

// ListSavedFiles returns one page of the user's saved-file ledger, newest
// first, optionally filtered by a literal substring of the storage path or
// storage name. q empty = plain listing. Every row is scoped to the
// requesting user's id — no cross-user leak is possible by construction.
func ListSavedFiles(ctx context.Context, userID uint, q string, page int) ([]SavedFileEntry, int64, error) {
	if page < 1 {
		page = 1
	}
	// Name resolution rides a LEFT JOIN so rows whose storage was deleted
	// still appear (with an empty name) instead of vanishing.
	base := db.WithContext(ctx).Model(&SavedFileDedup{}).
		Select("saved_file_dedups.*, storages.name AS storage_name").
		Joins("LEFT JOIN storages ON storages.id = saved_file_dedups.storage_id").
		Where("saved_file_dedups.user_id = ?", userID)
	if q != "" {
		like := "%" + escapeLike(q) + "%"
		base = base.Where(`(saved_file_dedups.storage_path LIKE ? ESCAPE '\' OR storages.name LIKE ? ESCAPE '\')`, like, like)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []SavedFileEntry
	if err := base.
		Order("saved_file_dedups.created_at DESC, saved_file_dedups.id DESC").
		Offset((page - 1) * HistoryPageSize).
		Limit(HistoryPageSize).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// GetSavedFileStats aggregates the user's ledger: totals, per-storage and
// per-day (last 14 days) counts and sizes.
func GetSavedFileStats(ctx context.Context, userID uint) (SavedFileStats, error) {
	var stats SavedFileStats
	// The slice fields would read as associations to GORM — every scan
	// targets a dedicated flat struct.
	var totals struct {
		TotalCount int64
		TotalSize  int64
	}
	if err := db.WithContext(ctx).Model(&SavedFileDedup{}).
		Where("user_id = ?", userID).
		Select("COUNT(*) AS total_count, COALESCE(SUM(size),0) AS total_size").
		Scan(&totals).Error; err != nil {
		return stats, err
	}
	stats.TotalCount, stats.TotalSize = totals.TotalCount, totals.TotalSize
	if err := db.WithContext(ctx).Model(&SavedFileDedup{}).
		Select("saved_file_dedups.storage_id AS storage_id, COALESCE(storages.name,'') AS storage_name, COUNT(*) AS count, COALESCE(SUM(saved_file_dedups.size),0) AS size").
		Joins("LEFT JOIN storages ON storages.id = saved_file_dedups.storage_id").
		Where("saved_file_dedups.user_id = ?", userID).
		Group("saved_file_dedups.storage_id, storages.name").
		Order("count DESC").
		Scan(&stats.ByStorage).Error; err != nil {
		return stats, err
	}
	since := time.Now().AddDate(0, 0, -historyDaysBack).Format("2006-01-02")
	if err := db.WithContext(ctx).Model(&SavedFileDedup{}).
		Where("user_id = ? AND date(created_at) >= ?", userID, since).
		Select("date(created_at) AS day, COUNT(*) AS count, COALESCE(SUM(size),0) AS size").
		Group("day").
		Order("day DESC").
		Scan(&stats.ByDay).Error; err != nil {
		return stats, err
	}
	return stats, nil
}
