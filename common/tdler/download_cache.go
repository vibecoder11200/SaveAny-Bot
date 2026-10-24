package tdler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/charmbracelet/log"

	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// DownloadResumeKey builds the identity that keys a download's resume state:
// the media's stable id (or its chat:message fallback when absent) plus the
// destination, so the same file saved to two storages — or two paths —
// resumes independently, while re-running the same save resumes in place.
func DownloadResumeKey(file tfile.TGFile, storageName, destPath string) string {
	id := file.FileUniqueID()
	if id == "" {
		if msgFile, ok := file.(tfile.TGFileMessage); ok && msgFile.Message() != nil {
			msg := msgFile.Message()
			id = fmt.Sprintf("chat:%d:msg:%d", tgutil.ChatIdFromPeer(msg.GetPeerID()), msg.GetID())
		} else {
			id = "noid:" + file.Name()
		}
	}
	return id + "|" + storageName + "|" + destPath
}

// DownloadToCache downloads file to localPath. Files with a known size are
// downloaded resumable: bytes land in a .part file under the resume dir and
// an interrupted run — or an entirely restarted process — continues from the
// recorded bitmap instead of starting over. Photos (unknown size) and
// unusable resume state take the plain non-resumable path. wrap (may be nil)
// decorates the file being written for progress accounting and stall
// detection; it observes only the bytes fetched this run, not the ones
// already on disk from a prior run.
func DownloadToCache(
	ctx context.Context,
	logger *log.Logger,
	file tfile.TGFile,
	localPath, resumeKey string,
	meter *dlutil.FloodWaitMeter,
	wrap func(io.WriterAt) io.WriterAt,
) error {
	if file.Size() <= 0 {
		return downloadPlain(ctx, file, localPath, meter, wrap)
	}
	stem := resumeStem(resumeKey)
	unlock := lockStem(stem)
	defer unlock()

	// The cache filename embeds a per-run xid, so the completed file cannot
	// be found again after a restart; the durable state is the .part file
	// plus its bitmap, and the rename below finishes a resumed download.
	bitmapPath := filepath.Join(ResumeDir(), stem+".part.bitmap")
	partPath := filepath.Join(ResumeDir(), stem+".part")
	if err := os.MkdirAll(ResumeDir(), os.ModePerm); err != nil {
		logger.Warnf("Cannot create resume directory, downloading without resume: %v", err)
		return downloadPlain(ctx, file, localPath, meter, wrap)
	}
	part, err := os.OpenFile(partPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		logger.Warnf("Cannot open resume part file, downloading without resume: %v", err)
		return downloadPlain(ctx, file, localPath, meter, wrap)
	}
	var wrAt io.WriterAt = part
	if wrap != nil {
		wrAt = wrap(part)
	}
	err = DownloadResumable(ctx, file, wrAt, dlutil.BestThreads(file.Size(), config.C().Threads), bitmapPath, meter)
	closeErr := part.Close()
	if err != nil {
		// The .part and .bitmap files stay behind on purpose: they are the
		// resume state the next attempt picks up.
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("close resume part file: %w", closeErr)
	}
	st, statErr := os.Stat(partPath)
	if statErr != nil {
		return fmt.Errorf("stat resume part file: %w", statErr)
	}
	if st.Size() != file.Size() {
		return fmt.Errorf("incomplete download: part file holds %d bytes, expected %d", st.Size(), file.Size())
	}
	if err := os.MkdirAll(filepath.Dir(localPath), os.ModePerm); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	if err := os.Rename(partPath, localPath); err != nil {
		return fmt.Errorf("move completed download into cache: %w", err)
	}
	if err := RemoveResumeState(bitmapPath); err != nil {
		logger.Warnf("Resume state cleanup failed (harmless): %v", err)
	}
	return nil
}

// downloadPlain is the classic non-resumable path: one file at localPath,
// downloaded in one shot, removed again if the download fails.
func downloadPlain(
	ctx context.Context,
	file tfile.TGFile,
	localPath string,
	meter *dlutil.FloodWaitMeter,
	wrap func(io.WriterAt) io.WriterAt,
) error {
	localFile, err := fsutil.CreateFile(localPath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	var wrAt io.WriterAt = localFile
	if wrap != nil {
		wrAt = wrap(localFile)
	}
	dlErr := DownloadParallelWithRefresh(ctx, file, meter, ctx, wrAt)
	closeErr := localFile.Close()
	if dlErr != nil {
		_ = localFile.Remove()
		return dlErr
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close cache file: %w", closeErr)
	}
	return nil
}

func resumeStem(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:20]
}
