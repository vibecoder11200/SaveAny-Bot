package tfile

import (
	"context"
	"fmt"
	"io"

	"github.com/charmbracelet/log"
	"github.com/krau/SaveAny-Bot/common/tdler"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
	"github.com/krau/SaveAny-Bot/storage"
	"golang.org/x/sync/errgroup"
)

func executeStream(ctx context.Context, task *Task) error {
	logger := log.FromContext(ctx).WithPrefix(fmt.Sprintf("file[%s]", task.File.Name()))

	pr, pw := io.Pipe()
	defer pr.Close()
	errg, uploadCtx := errgroup.WithContext(ctx)
	uploadCtx = storagetypes.WithMessageMetadata(uploadCtx, task.metadataJSON)
	if caption, ok := sourceCaption(task.File); ok {
		uploadCtx = storagetypes.WithSourceCaption(uploadCtx, caption)
	}
	savedPath := task.Path
	errg.Go(func() error {
		var err error
		savedPath, err = storage.SaveWithPath(uploadCtx, task.Storage, pr, task.Path)
		return err
	})
	// Guards: watchdog + meter derive dlCtx from the upload side's context
	// so a guard cancellation fails the download RPCs, and the pipe error
	// then fails the waiting upload.
	wdog, wctx := dlutil.NewStallWatchdog(uploadCtx, dlutil.DownloadStallTimeout)
	defer wdog.Close()
	dlCtx, dlCancel := context.WithCancel(wctx)
	defer dlCancel()
	meter := dlutil.NewFloodWaitMeter(dlCtx, dlCancel)
	defer meter.Close()
	wr := newWriter(dlCtx, pw, task.Progress, task, wdog.Kick)
	errg.Go(func() error {
		defer pw.Close()
		logger.Info("Starting file download in stream mode")
		downloader, cleanup := tdler.NewDownloaderWithMeter(ctx, task.File, meter)
		if cleanup != nil {
			defer cleanup(ctx)
		}
		_, err := downloader.Stream(dlCtx, wr)
		if err != nil {
			if wdog.Stalled() {
				err = fmt.Errorf("download stalled: no data received for %s", dlutil.DownloadStallTimeout)
			} else if meter.Crawled() {
				err = dlutil.CrawlError(meter.Absorbed())
			} else {
				err = fmt.Errorf("failed to download file: %w", err)
			}
			logger.Errorf("%v", err)
			pw.CloseWithError(err)
		}
		return err
	})
	var err error
	defer func() {
		if task.Progress != nil {
			task.Progress.OnDone(ctx, task, err)
		}
	}()
	if err = errg.Wait(); err != nil {
		return err
	}
	logger.Info("File downloaded successfully in stream mode")
	// Record-after-upload runs in BOTH paths (locked stream decision): the
	// file-id pre-check above the dispatch and this recording are what keep
	// stream dedup useful; the hash layer is N/A here by design — streamed
	// uploads have no completed local cache file to hash.
	task.recordDedupAfterUpload(ctx, logger, storage.ID(task.Storage))
	task.saveMetadataSidecar(ctx, savedPath)
	if err := core.MarkTaskElementDone(ctx, task.ID, task.ElementID); err != nil {
		return fmt.Errorf("failed to persist task completion: %w", err)
	}
	return nil
}
