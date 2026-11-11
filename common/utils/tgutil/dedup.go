package tgutil

import (
	"context"

	"github.com/charmbracelet/log"

	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// RecordResendDedup records the dedup row for a file that was RESENT from
// Telegram straight into a storage (no download, no upload): the file id is
// recorded with an empty content hash, because the bytes never touched this
// disk. Recording is idempotent against the partial unique indexes.
// Best-effort: a failure is logged (at-least-once duplicate risk) and does
// not fail the task.
func RecordResendDedup(ctx context.Context, logger *log.Logger, userID uint, storageID uint, file tfile.TGFile, path string) {
	if userID == 0 || storageID == 0 {
		return
	}
	srcChat, srcMsg := SourceFromTGFile(file)
	if err := database.RecordDedupWithSource(ctx, userID, storageID,
		file.FileUniqueID(), "", file.Size(), path, srcChat, srcMsg); err != nil {
		logger.Errorf("dedup record failed after resend (at-least-once duplicate risk): %v", err)
	}
}
