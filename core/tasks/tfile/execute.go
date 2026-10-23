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
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/enums/ctxkey"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
	tfilepkg "github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"
)

func (t *Task) Execute(ctx context.Context) error {
	logger := log.FromContext(ctx).WithPrefix(fmt.Sprintf("file[%s]", t.File.Name()))
	if t.Progress != nil {
		t.Progress.OnStart(ctx, t)
	}
	if t.SkipIfExists {
		if t.Storage.Exists(ctx, t.Path) {
			// The file is this task's own progress from before the restart:
			// keep it and continue, whatever the configured conflict
			// strategy would do (rename would create file(1).ext duplicates).
			logger.Infof("File already exists, skipping: %s", t.Path)
			if t.Progress != nil {
				t.Progress.OnDone(ctx, t, nil)
			}
			return nil
		}
	}
	if t.UserID > 0 && !t.DisableDedup {
		if fid := t.File.FileUniqueID(); fid != "" && database.DedupHasFileID(ctx, t.UserID, t.Storage.Name(), fid) {
			logger.Infof("dedup skip (file-id %s): already saved to storage %s, path %s", fid, t.Storage.Name(), t.Path)
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
	localFile, err := fsutil.CreateFile(t.localPath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	defer func() {
		if err := localFile.CloseAndRemove(); err != nil {
			logger.Errorf("Failed to close local file: %v", err)
		}
	}()
	wdog, wctx := dlutil.NewStallWatchdog(ctx, dlutil.DownloadStallTimeout)
	defer wdog.Close()
	dlCtx, dlCancel := context.WithCancel(wctx)
	defer dlCancel()
	meter := dlutil.NewFloodWaitMeter(dlCtx, dlCancel)
	defer meter.Close()
	wrAt := newWriterAt(dlCtx, localFile, t.Progress, t, wdog.Kick)

	defer func() {
		if t.Progress != nil {
			t.Progress.OnDone(ctx, t, err)
		}
	}()
	err = tdler.DownloadParallelWithRefresh(ctx, t.File, meter, dlCtx, wrAt)
	if err != nil {
		if wdog.Stalled() {
			return fmt.Errorf("download stalled: no data received for %s", dlutil.DownloadStallTimeout)
		}
		if meter.Crawled() {
			return dlutil.CrawlError(meter.Absorbed())
		}
		return fmt.Errorf("failed to download file: %w", err)
	}
	logger.Infof("File downloaded successfully")
	if path.Ext(t.File.Name()) == "" {
		ext := fsutil.DetectFileExt(t.localPath)
		if ext != "" {
			t.Path = t.Path + ext
		}
	}
	var fileStat os.FileInfo
	fileStat, err = os.Stat(t.localPath)
	if err != nil {
		return fmt.Errorf("failed to get file stat: %w", err)
	}
	// Content-hash dedup: identical bytes under a different name are dropped
	// here, after the download but before the upload.
	if t.UserID > 0 && !t.DisableDedup {
		skip, hash, herr := database.DedupCheckHash(ctx, t.UserID, t.Storage.Name(), t.localPath)
		if herr != nil {
			logger.Warnf("dedup hash check failed (fail-open, continuing upload): %v", herr)
		} else if skip {
			logger.Infof("dedup skip (hash sha256:%s…): already saved to storage %s, path %s", hash[:min(12, len(hash))], t.Storage.Name(), t.Path)
			return nil // the deferred OnDone reports success
		} else {
			t.uploadHash = hash
		}
	}
	vctx := context.WithValue(ctx, ctxkey.ContentLength, fileStat.Size())
	if caption, ok := sourceCaption(t.File); ok {
		vctx = storagetypes.WithSourceCaption(vctx, caption)
	}
	skippedLarge := false
	err = retry.Retry(func() error {
		file, err := os.Open(t.localPath)
		if err != nil {
			return fmt.Errorf("failed to open cache file: %w", err)
		}
		defer file.Close()
		uploadProgress, tracksUpload := t.Progress.(UploadProgressTracker)
		var saveErr error
		if !tracksUpload {
			saveErr = t.Storage.Save(vctx, file, t.Path)
		} else {
			uploadProgress.OnUploadStart(vctx, t, fileStat.Size())
			onProgress := func(uploaded, total int64) {
				uploadProgress.OnUploadProgress(vctx, t, uploaded, total)
			}
			if progressSaver, ok := t.Storage.(storage.StorageProgressSaver); ok {
				saveErr = progressSaver.SaveWithProgress(vctx, file, t.Path, onProgress)
			} else {
				var reader io.Reader = ioutil.NewProgressReader(file, fileStat.Size(), onProgress)
				saveErr = t.Storage.Save(vctx, reader, t.Path)
			}
		}
		if errors.Is(saveErr, storagetypes.ErrSkippedLarge) {
			// Policy skip, not a failure: stop retrying immediately and let
			// the caller know nothing was uploaded.
			skippedLarge = true
			return nil
		}
		if saveErr != nil {
			return fmt.Errorf("failed to save file: %w", saveErr)
		}
		return nil
	}, retry.RetryTimes(uint(config.C().Retry)), retry.Context(vctx))
	if err != nil {
		return fmt.Errorf("failed to save file after retries: %w", err)
	}
	if skippedLarge {
		// Nothing was uploaded: no ledger or dedup record — report
		// success-with-skip through the deferred OnDone.
		logger.Warnf("File skipped by storage (too large): %s", t.Path)
		return nil
	}
	if t.UserID > 0 && (t.File.FileUniqueID() != "" || t.uploadHash != "") {
		if derr := database.DedupRecordSaved(ctx, t.UserID, t.Storage.Name(), t.File.FileUniqueID(), t.uploadHash, fileStat.Size(), t.Path); derr != nil {
			logger.Errorf("dedup record failed after upload (at-least-once duplicate risk): %v", derr)
		}
	}
	return nil
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
		if derr := database.RecordDedup(ctx, t.UserID, t.Storage.Name(), t.File.FileUniqueID(), "", t.File.Size(), t.Path); derr != nil {
			logger.Errorf("dedup record failed after resend (at-least-once duplicate risk): %v", derr)
		}
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

func sourceCaption(file tfilepkg.TGFile) (string, bool) {
	messageFile, ok := file.(tfilepkg.TGFileMessage)
	if !ok || messageFile.Message() == nil {
		return "", false
	}
	return messageFile.Message().GetMessage(), true
}
