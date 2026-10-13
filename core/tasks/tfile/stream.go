package tfile

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/charmbracelet/log"
	"github.com/krau/SaveAny-Bot/common/tdler"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	tfilepkg "github.com/krau/SaveAny-Bot/pkg/tfile"
	"golang.org/x/sync/errgroup"
)

func executeStream(ctx context.Context, task *Task) error {
	logger := log.FromContext(ctx).WithPrefix(fmt.Sprintf("file[%s]", task.File.Name()))

	// Streamed bytes go straight to storage, so there is no mid-stream retry;
	// refresh the reference up front instead. A vanished source fails now,
	// anything else falls back to the stored location.
	if msgFile, ok := task.File.(tfilepkg.TGFileMessage); ok && msgFile.Message() != nil {
		if err := tdler.RefreshFileLocation(ctx, msgFile); err != nil {
			if errors.Is(err, tdler.ErrSourceGone) {
				return fmt.Errorf("failed to download file: %w", err)
			}
			logger.Warnf("File reference refresh failed, trying the stored location: %v", err)
		}
	}

	pr, pw := io.Pipe()
	defer pr.Close()
	errg, uploadCtx := errgroup.WithContext(ctx)
	errg.Go(func() error {
		return task.Storage.Save(uploadCtx, pr, task.Path)
	})
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
		_, err := tdler.NewDownloaderWithMeter(task.File, meter).Stream(dlCtx, wr)
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
	return nil
}
