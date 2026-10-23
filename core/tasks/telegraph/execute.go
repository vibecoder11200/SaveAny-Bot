package telegraph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"

	"github.com/charmbracelet/log"
	"github.com/duke-git/lancet/v2/retry"
	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
	"github.com/krau/SaveAny-Bot/pkg/taskevent"
	"golang.org/x/sync/errgroup"
)

func (t *Task) Execute(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Infof("Starting Telegraph task %s", t.PhPath)
	if t.progress != nil {
		t.progress.OnStart(ctx, t)
	}
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(config.C().Workers)
	for i, pic := range t.Pics {
		eg.Go(func() error {
			err := t.processPic(gctx, pic, i)
			if err != nil {
				logger.Errorf("Error processing picture %s: %v", pic, err)
				return fmt.Errorf("failed to process picture %s: %w", pic, err)
			}
			downloaded := t.downloaded.Add(1)
			if t.progress != nil {
				t.progress.OnProgress(gctx, t)
			}
			taskevent.Emit(gctx, taskevent.Event{
				TaskID:          t.ID,
				Phase:           taskevent.PhaseProgress,
				TotalFiles:      t.totalpics,
				DownloadedFiles: int(downloaded),
			})
			return nil
		})
	}
	err := eg.Wait()
	if err != nil {
		logger.Errorf("Error during Telegraph task execution: %v", err)
	} else {
		logger.Infof("Telegraph task %s completed successfully", t.PhPath)
	}
	if t.progress != nil {
		t.progress.OnDone(ctx, t, err)
	}
	return err
}

func (t *Task) processPic(ctx context.Context, picUrl string, index int) error {
	logger := log.FromContext(ctx)
	retryOpts := []retry.Option{
		retry.Context(ctx),
		retry.RetryTimes(uint(config.C().Retry)),
	}
	err := retry.Retry(func() error {
		body, err := t.client.Download(ctx, picUrl)
		if err != nil {
			return fmt.Errorf("failed to download picture %s: %w", picUrl, err)
		}
		defer body.Close()
		filename := fmt.Sprintf("%d%s", index+1, path.Ext(picUrl))
		if t.cannotStream {
			cacheFile, err := fsutil.CreateFile(filepath.Join(config.C().Temp.BasePath,
				fmt.Sprintf("tph_%s_%s", t.TaskID(), filename),
			))
			if err != nil {
				return fmt.Errorf("failed to create cache file for picture %s: %w", filename, err)
			}
			defer func() {
				if err := cacheFile.CloseAndRemove(); err != nil {
					logger.Errorf("Failed to close and remove cache file for picture %s: %v", filename, err)
				}
			}()
			_, err = io.Copy(cacheFile, body)
			if err != nil {
				return fmt.Errorf("failed to copy picture %s to cache file: %w", filename, err)
			}
			_, err = cacheFile.Seek(0, 0)
			if err != nil {
				return fmt.Errorf("failed to seek cache file for picture %s: %w", filename, err)
			}
			var contentHash string
			if t.UserID > 0 {
				skip, hash, derr := database.DedupCheckHash(ctx, t.UserID, t.Stor.Name(), cacheFile.Name())
				switch {
				case derr != nil:
					logger.Warnf("dedup hash check failed (fail-open, continuing upload): %v", derr)
				case skip:
					logger.Infof("dedup skip (hash sha256:%s…): already saved to storage %s, path %s", hash[:min(12, len(hash))], t.Stor.Name(), filename)
					return nil
				default:
					contentHash = hash
				}
			}
			skippedLarge := false
			err = t.Stor.Save(ctx, cacheFile, path.Join(t.StorPath, filename))
			if errors.Is(err, storagetypes.ErrSkippedLarge) {
				logger.Warnf("File skipped by storage (too large): %s", filename)
				skippedLarge = true
				err = nil
			}
			if err != nil {
				return fmt.Errorf("failed to save picture %s: %w", filename, err)
			}
			if !skippedLarge && t.UserID > 0 && contentHash != "" {
				if derr := database.DedupRecordSaved(ctx, t.UserID, t.Stor.Name(), "", contentHash, 0, path.Join(t.StorPath, filename)); derr != nil {
					logger.Errorf("dedup record failed after upload (at-least-once duplicate risk): %v", derr)
				}
			}
			return nil
		}
		// Streamed bytes never touch this disk: no file to hash (RT-10).
		logger.Debug("dedup: stream mode, content-hash check not possible")
		err = t.Stor.Save(ctx, body, path.Join(t.StorPath, filename))
		if errors.Is(err, storagetypes.ErrSkippedLarge) {
			logger.Warnf("File skipped by storage (too large): %s", filename)
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to save picture %s: %w", filename, err)
		}
		return nil
	}, retryOpts...)
	return err
}
