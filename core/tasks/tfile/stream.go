package tfile

import (
	"context"
	"fmt"
	"io"

	"github.com/charmbracelet/log"
	"github.com/krau/SaveAny-Bot/common/tdler"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"golang.org/x/sync/errgroup"
)

func executeStream(ctx context.Context, task *Task) error {
	logger := log.FromContext(ctx).WithPrefix(fmt.Sprintf("file[%s]", task.File.Name()))

	pr, pw := io.Pipe()
	defer pr.Close()
	errg, uploadCtx := errgroup.WithContext(ctx)
	errg.Go(func() error {
		return task.Storage.Save(uploadCtx, pr, task.Path)
	})
	wdog, wctx := dlutil.NewStallWatchdog(uploadCtx, dlutil.DownloadStallTimeout)
	defer wdog.Close()
	wr := newWriter(wctx, pw, task.Progress, task, wdog.Kick)
	errg.Go(func() error {
		defer pw.Close()
		logger.Info("Starting file download in stream mode")
		_, err := tdler.NewDownloader(task.File).Stream(wctx, wr)
		if err != nil {
			if wdog.Stalled() {
				logger.Errorf("Download stalled: no data received for %s", dlutil.DownloadStallTimeout)
			} else {
				logger.Errorf("Failed to download file: %v", err)
			}
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
