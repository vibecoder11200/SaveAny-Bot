package tfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/charmbracelet/log"
	"github.com/duke-git/lancet/v2/retry"
	"github.com/krau/SaveAny-Bot/common/tdler"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
	"github.com/krau/SaveAny-Bot/common/utils/ioutil"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/enums/ctxkey"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
	tfilepkg "github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"
)

func (t *Task) Execute(ctx context.Context) (err error) {
	defer func() { t.execErr = err }()
	logger := log.FromContext(ctx).WithPrefix(fmt.Sprintf("file[%s]", t.File.Name()))
	if t.Progress != nil {
		t.Progress.OnStart(ctx, t)
	}
	// Pre-upload hooks run above the stream dispatch: with config.Stream on,
	// anything wired only into the download/upload regions below never runs.
	// Order mirrors the fork: file-id dedup check -> tryResend -> SkipLarge
	// pre-check -> stream dispatch.
	storageID := storage.ID(t.Storage)
	// Overwrite (conflict strategy) means the user asked for a fresh copy:
	// dedup still hashes and records below, but never skips.
	overwrite, _ := t.Ctx.Value(ctxkey.OverwriteExisting).(bool)
	if t.UserID > 0 && !overwrite {
		if fid := t.File.FileUniqueID(); fid != "" && database.DedupHasFileID(ctx, t.UserID, storageID, fid) {
			logger.Infof("dedup skip (file-id %s): already saved to storage %d, path %s", fid, storageID, t.Path)
			t.skippedDedup = true
			if t.Progress != nil {
				t.Progress.OnDone(ctx, t, nil)
			}
			return nil
		}
	}
	if handled, rerr := t.tryResend(ctx, logger); handled {
		if t.Progress != nil {
			t.Progress.OnDone(ctx, t, rerr)
		}
		return rerr
	}
	// A storage that drops oversized files (SkipLarge) is checked before the
	// download: no bandwidth is spent on a file it will refuse.
	if skipper, ok := t.Storage.(storagetypes.StorageLargeFileSkipper); ok {
		if size := t.File.Size(); size > 0 {
			if limit, enabled := skipper.SkipLargeLimit(ctx); enabled && size > limit {
				logger.Warnf("File skipped: %d bytes exceeds storage limit %d (SkipLarge)", size, limit)
				t.skippedLarge = true
				if t.Progress != nil {
					t.Progress.OnDone(ctx, t, nil)
				}
				return nil
			}
		}
	}
	if t.stream {
		return executeStream(ctx, t)
	}

	logger.Info("Starting file download")
	// Guards: the watchdog cancels dlCtx when no bytes flow for the whole
	// stall timeout, the meter cancels it when absorbed flood-wait time
	// shows a crawl. dlCtx, not ctx: the download RPCs must observe that
	// cancellation.
	wdog, wctx := dlutil.NewStallWatchdog(ctx, dlutil.DownloadStallTimeout)
	defer wdog.Close()
	dlCtx, dlCancel := context.WithCancel(wctx)
	defer dlCancel()
	meter := dlutil.NewFloodWaitMeter(dlCtx, dlCancel)
	defer meter.Close()
	defer func() {
		if t.Progress != nil {
			t.Progress.OnDone(ctx, t, err)
		}
	}()
	if err := tdler.DownloadToCache(dlCtx, t.File, t.localPath, func(w io.WriterAt) io.WriterAt {
		return newWriterAt(dlCtx, w, t.Progress, t, wdog.Kick)
	}, meter); err != nil {
		return dlutil.DownloadFailure(err, wdog, meter)
	}
	logger.Infof("File downloaded successfully")
	if path.Ext(t.File.Name()) == "" && path.Ext(t.Path) == "" {
		ext := fsutil.DetectFileExt(t.localPath)
		if ext != "" {
			t.Path = t.Path + ext
		}
	}
	fileStat, err := os.Stat(t.localPath)
	if err != nil {
		return fmt.Errorf("failed to get file stat: %w", err)
	}
	if t.File.Size() > 0 && fileStat.Size() == 0 {
		return fmt.Errorf("downloaded file is empty (expected %d bytes)", t.File.Size())
	}
	// Content-hash dedup: identical bytes under a different name are dropped
	// here, after the download but before the upload. Overwrite skips the
	// check but the record below still indexes the upload. The hash layer is
	// N/A-by-design for stream mode: streamed uploads have no completed
	// local cache file to hash.
	if !overwrite {
		dedupSkip, hash := database.DedupSkipByHash(ctx, logger, t.UserID, storageID, t.localPath, t.Path)
		if dedupSkip {
			t.skippedDedup = true
			return nil // the deferred OnDone reports success
		}
		t.uploadHash = hash
	}
	vctx := context.WithValue(ctx, ctxkey.ContentLength, fileStat.Size())
	vctx = storagetypes.WithMessageMetadata(vctx, t.metadataJSON)
	if caption, ok := sourceCaption(t.File); ok {
		vctx = storagetypes.WithSourceCaption(vctx, caption)
	}
	savedPath := t.Path
	err = retry.Retry(func() error {
		file, err := os.Open(t.localPath)
		if err != nil {
			return fmt.Errorf("failed to open cache file: %w", err)
		}
		defer file.Close()
		uploadProgress, tracksUpload := t.Progress.(UploadProgressTracker)
		if !tracksUpload {
			if savedPath, err = storage.SaveWithPath(vctx, t.Storage, file, t.Path); err != nil {
				if errors.Is(err, storagetypes.ErrSkippedLarge) {
					// Policy skip, not a failure: stop retrying immediately
					// and let the caller know nothing was uploaded.
					t.skippedLarge = true
					return nil
				}
				return fmt.Errorf("failed to save file: %w", err)
			}
			return nil
		}

		uploadProgress.OnUploadStart(vctx, t, fileStat.Size())
		onProgress := func(uploaded, total int64) {
			uploadProgress.OnUploadProgress(vctx, t, uploaded, total)
		}
		if progressSaver, ok := t.Storage.(storage.StorageProgressSaver); ok {
			err = progressSaver.SaveWithProgress(vctx, file, t.Path, onProgress)
			savedPath = t.Path
		} else {
			var reader io.Reader = ioutil.NewProgressReader(file, fileStat.Size(), onProgress)
			savedPath, err = storage.SaveWithPath(vctx, t.Storage, reader, t.Path)
		}
		if err != nil {
			if errors.Is(err, storagetypes.ErrSkippedLarge) {
				// Policy skip, not a failure: stop retrying immediately and
				// let the caller know nothing was uploaded.
				t.skippedLarge = true
				return nil
			}
			return fmt.Errorf("failed to save file: %w", err)
		}
		return nil
	}, retry.RetryTimes(uint(config.C().Retry)), retry.Context(vctx))
	if err != nil {
		return fmt.Errorf("failed to save file after retries: %w", err)
	}
	if t.skippedLarge {
		// Nothing was uploaded: no ledger or dedup record — report
		// success-with-skip through the deferred OnDone.
		logger.Warnf("File skipped by storage (too large): %s", t.Path)
		return nil
	}
	if err := core.MarkTaskElementDone(ctx, t.ID, t.ElementID); err != nil {
		return fmt.Errorf("failed to persist task completion: %w", err)
	}
	t.recordDedupAfterUpload(ctx, logger, storageID)
	t.saveMetadataSidecar(ctx, savedPath)
	if err := os.Remove(t.localPath); err != nil {
		logger.Errorf("Failed to remove cache file: %v", err)
	}
	return nil
}

// recordDedupAfterUpload indexes a genuinely uploaded file in the dedup
// ledger (both plain and stream paths); overwrite still records — the
// content is there either way.
func (t *Task) recordDedupAfterUpload(ctx context.Context, logger *log.Logger, storageID uint) {
	if t.UserID == 0 || storageID == 0 {
		return
	}
	srcChat, srcMsg := tgutil.SourceFromTGFile(t.File)
	database.RecordDedupAfterUploadWithSource(ctx, logger, t.UserID, storageID,
		t.File.FileUniqueID(), t.uploadHash, t.File.Size(), t.Path, srcChat, srcMsg)
}

func sourceCaption(file tfilepkg.TGFile) (string, bool) {
	messageFile, ok := file.(tfilepkg.TGFileMessage)
	if !ok || messageFile.Message() == nil {
		return "", false
	}
	return messageFile.Message().GetMessage(), true
}

// tryResend re-sends the file's Telegram source media straight to a
// supporting storage — no download, no upload. handled reports that the
// task's fate was decided (completed, or terminally failed on an ambiguous
// send); fallback-eligible failures return handled=false so the caller
// proceeds with the classic download+upload path.
func (t *Task) tryResend(ctx context.Context, logger *log.Logger) (handled bool, err error) {
	sender, ok := t.Storage.(storagetypes.ExistingMediaSender)
	if !ok {
		return false, nil
	}
	msgFile, ok := t.File.(tfilepkg.TGFileMessage)
	if !ok {
		return false, nil
	}
	media, ok := tgutil.ResendInputMedia(msgFile)
	if !ok {
		return false, nil
	}
	sendCtx := tdler.SendCtxForFile(ctx, t.File)
	if sendCtx == nil {
		return false, nil
	}
	// Caption parity with the upload path: source caption when present, else
	// the destination filename.
	caption := path.Base(t.Path)
	if source, ok := sourceCaption(t.File); ok {
		caption = source
	}
	rerr := sender.SendExistingMedia(ctx, sendCtx, media, t.Path, caption)
	if rerr == nil {
		logger.Info("resend ok (no download/upload)")
		// A resend records the file id with an empty content hash — the
		// bytes never touched this disk. Recording is idempotent against the
		// partial unique indexes.
		tgutil.RecordResendDedup(ctx, logger, t.UserID, storage.ID(t.Storage), t.File, t.Path)
		return true, nil
	}
	if tgutil.ResendCanFallback(rerr) {
		logger.Infof("resend failed (fallback to download+upload): %v", rerr)
		return false, nil
	}
	// Ambiguous outcome: the message may have been delivered. Failing the
	// task and retrying later is always safe; re-uploading is not.
	logger.Errorf("resend failed (ambiguous, task failed): %v", rerr)
	return true, fmt.Errorf("resend failed (ambiguous): %w", rerr)
}
