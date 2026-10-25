package database

import (
	"context"
	"strings"
	"time"
)

// HistoryPageSize is the fixed page size of /history listings.
const HistoryPageSize = 20

// SavedFileStats aggregates a user's ledger for /history stats.
type SavedFileStats struct {
	TotalCount int64
	TotalSize  int64
	ByStorage  []SavedFileStorageStats
	ByDay      []SavedFileDayStats
}

type SavedFileStorageStats struct {
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
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(in)
}

// ListSavedFiles returns one page of the user's saved-file ledger, newest
// first, optionally filtered by a literal substring of the storage path or
// storage name. q empty = plain listing.
func ListSavedFiles(ctx context.Context, userID int64, q string, page int) ([]SavedFileDedup, int64, error) {
	if page < 1 {
		page = 1
	}
	query := db.WithContext(ctx).Model(&SavedFileDedup{}).Where("user_id = ?", userID)
	if q != "" {
		like := "%" + escapeLike(q) + "%"
		query = query.Where("(storage_path LIKE ? ESCAPE '\\' OR storage_name LIKE ? ESCAPE '\\')", like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []SavedFileDedup
	if err := query.
		Order("created_at DESC, id DESC").
		Offset((page - 1) * HistoryPageSize).
		Limit(HistoryPageSize).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// GetSavedFileStats aggregates the user's ledger: totals, per-storage and
// per-day (last 14 days) counts and sizes.
func GetSavedFileStats(ctx context.Context, userID int64) (SavedFileStats, error) {
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
		Where("user_id = ?", userID).
		Select("storage_name AS storage_name, COUNT(*) AS count, COALESCE(SUM(size),0) AS size").
		Group("storage_name").
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
