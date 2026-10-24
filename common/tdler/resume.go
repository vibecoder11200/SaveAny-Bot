package tdler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/sync/errgroup"

	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/pkg/consts/tglimit"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

const maxChunkRetries = 20
const maxResumeRefreshes = 2

// errCDNRedirect reports a chunk served from a CDN. Raw upload.getFile
// requests cannot follow CDN redirects (they need gotd's re-hash dance), so
// the download falls back to the plain downloader, which handles them.
var errCDNRedirect = errors.New("CDN redirect")

// stemLocks serializes resumable downloads sharing one identity key: the
// task queue runs tasks concurrently, and two saves of the same file to the
// same destination must not interleave writes into one .part file.
var stemLocks sync.Map // string -> *sync.Mutex

func lockStem(stem string) func() {
	v, _ := stemLocks.LoadOrStore(stem, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// resumeBitmap records which partSize-aligned blocks of a download have been
// durably written, so an interrupted download can continue after a restart.
// The bitmap file is rewritten atomically after every completed block.
type resumeBitmap struct {
	PartSize int      `json:"part_size"`
	Size     int64    `json:"size"`
	Blocks   []uint64 `json:"blocks"`

	mu sync.Mutex
}

func newResumeBitmap(size int64) *resumeBitmap {
	b := &resumeBitmap{PartSize: tglimit.MaxPartSize, Size: size}
	b.ensureBlocks()
	return b
}

func (b *resumeBitmap) ensureBlocks() {
	if need := (b.blockCount() + 63) / 64; len(b.Blocks) < need {
		b.Blocks = make([]uint64, need)
	}
}

func (b *resumeBitmap) blockCount() int {
	return int((b.Size + int64(b.PartSize) - 1) / int64(b.PartSize))
}

func (b *resumeBitmap) isDone(block int) bool {
	return b.Blocks[block/64]&(1<<uint(block%64)) != 0
}

func (b *resumeBitmap) markDone(block int) {
	b.Blocks[block/64] |= 1 << uint(block%64)
}

func (b *resumeBitmap) complete() bool {
	for block := 0; block < b.blockCount(); block++ {
		if !b.isDone(block) {
			return false
		}
	}
	return true
}

func (b *resumeBitmap) missingBlocks() []int {
	missing := make([]int, 0, b.blockCount())
	for block := 0; block < b.blockCount(); block++ {
		if !b.isDone(block) {
			missing = append(missing, block)
		}
	}
	return missing
}

func loadResumeBitmap(path string) (*resumeBitmap, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read resume bitmap: %w", err)
	}
	var b resumeBitmap
	if err := json.Unmarshal(data, &b); err != nil {
		// Unparsable bitmap (external corruption): drop it and restart clean.
		_ = os.Remove(path)
		return nil, nil
	}
	if b.Size <= 0 || b.PartSize <= 0 {
		_ = os.Remove(path)
		return nil, nil
	}
	b.ensureBlocks()
	return &b, nil
}

func (b *resumeBitmap) saveLocked(path string) error {
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal resume bitmap: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write resume bitmap: %w", err)
	}
	return os.Rename(tmp, path)
}

func (b *resumeBitmap) markAndSave(block int, path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.markDone(block)
	return b.saveLocked(path)
}

func isRetryableTimeout(err error) bool {
	if tgerr.Is(err, tg.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Transient net timeouts happen on unstable proxy links; gotd's own
	// retry loop treats them like TIMEOUT.
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// resumableDownload carries the per-download state shared by every chunk
// fetch: the flood-wait meter and the file-reference refresh budget.
type resumableDownload struct {
	file  tfile.TGFile
	meter *dlutil.FloodWaitMeter

	// refreshMu serializes file-reference refreshes across the concurrent
	// chunk goroutines: one refresh RPC serves every goroutine still holding
	// the stale location, and the budget counts refreshes, not expiry
	// events.
	refreshMu sync.Mutex
	refreshes int
}

// fetchChunk downloads one partSize-aligned chunk, retrying flood waits and
// transient timeouts like gotd's downloader does, and refreshing an expired
// file reference in place (up to maxResumeRefreshes per download).
func (d *resumableDownload) fetchChunk(ctx context.Context, offset int64, limit int) ([]byte, error) {
	client := eofAwareClient{Client: d.file.Dler(), size: d.file.Size(), meter: d.meter}
	req := &tg.UploadGetFileRequest{
		Location: d.file.Location(),
		Offset:   offset,
		Limit:    limit,
		Precise:  true,
	}
	timeoutRetries := 0
	for {
		res, err := client.UploadGetFile(ctx, req)
		if err == nil {
			switch r := res.(type) {
			case *tg.UploadFile:
				if len(r.Bytes) > limit {
					return nil, fmt.Errorf("get chunk at %d: server returned %d bytes, limit %d", offset, len(r.Bytes), limit)
				}
				return r.Bytes, nil
			case *tg.UploadFileCDNRedirect:
				return nil, fmt.Errorf("%w (dc %d)", errCDNRedirect, r.DCID)
			default:
				return nil, fmt.Errorf("unexpected upload.getFile response %T", res)
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if IsFileReferenceExpired(err) {
			msgFile, ok := d.file.(tfile.TGFileMessage)
			if !ok || msgFile.Message() == nil {
				return nil, fmt.Errorf("get chunk at %d (no source message to refresh): %w", offset, err)
			}
			d.refreshMu.Lock()
			if d.file.Location() != req.Location {
				// A sibling goroutine refreshed while this request was in
				// flight; reuse its result without spending budget.
				d.refreshMu.Unlock()
				req.Location = d.file.Location()
				continue
			}
			if d.refreshes >= maxResumeRefreshes {
				d.refreshMu.Unlock()
				return nil, fmt.Errorf("get chunk at %d: %w", offset, err)
			}
			d.refreshes++
			log.FromContext(ctx).Warnf("File reference expired mid-resume, refreshing (attempt %d/%d)", d.refreshes, maxResumeRefreshes)
			rerr := RefreshFileLocation(ctx, msgFile)
			d.refreshMu.Unlock()
			if rerr != nil {
				return nil, fmt.Errorf("get chunk at %d: %w (reference refresh: %v)", offset, err, rerr)
			}
			req.Location = d.file.Location()
			continue
		}
		flood, ferr := tgerr.FloodWait(ctx, err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if flood {
			// FloodWait already slept the mandated duration; retry.
			continue
		}
		if isRetryableTimeout(ferr) {
			timeoutRetries++
			if timeoutRetries >= maxChunkRetries {
				return nil, fmt.Errorf("get chunk at %d: retry limit reached: %w", offset, ferr)
			}
			continue
		}
		return nil, fmt.Errorf("get chunk at %d: %w", offset, ferr)
	}
}

// DownloadResumable downloads file to w in partSize chunks, skipping blocks
// already recorded as complete in bitmapPath and persisting every completed
// block so an interrupted download can resume. A missing or incompatible
// bitmap starts a full download. Requires a known, non-zero file size.
func DownloadResumable(
	ctx context.Context,
	file tfile.TGFile,
	w io.WriterAt,
	threads int,
	bitmapPath string,
	meter *dlutil.FloodWaitMeter,
) error {
	if file.Size() <= 0 {
		return fmt.Errorf("resumable download requires a known size")
	}
	if threads < 1 {
		threads = 1
	}
	bm, err := loadResumeBitmap(bitmapPath)
	if err != nil {
		return err
	}
	// The bitmap describes the .part file (bitmapPath minus ".bitmap"): if
	// that file is gone or empty the marked bytes are lost and the bitmap
	// must reset, otherwise resume would ship a holey file.
	if bm != nil {
		if stat, err := os.Stat(partPathOf(bitmapPath)); err != nil || stat.Size() == 0 {
			if err := ignoreMissing(os.Remove(bitmapPath)); err != nil {
				return fmt.Errorf("reset stale resume bitmap: %w", err)
			}
			bm = nil
		}
	}
	if bm == nil || bm.PartSize != tglimit.MaxPartSize || bm.Size != file.Size() {
		// Bitmap missing or incompatible: truncate the stale part file so
		// leftover tail bytes cannot fail the final size check.
		if err := ignoreMissing(os.Truncate(partPathOf(bitmapPath), 0)); err != nil {
			return fmt.Errorf("truncate stale part file: %w", err)
		}
		bm = newResumeBitmap(file.Size())
		if err := bm.saveLocked(bitmapPath); err != nil {
			return err
		}
	}
	missing := bm.missingBlocks()
	if total := bm.blockCount(); len(missing) < total {
		log.FromContext(ctx).Infof("Resuming interrupted download: %d of %d blocks already on disk", total-len(missing), total)
	}
	if len(missing) == 0 {
		return nil
	}

	d := &resumableDownload{file: file, meter: meter}
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(threads)
	for _, block := range missing {
		eg.Go(func() error {
			offset := int64(block) * int64(bm.PartSize)
			data, err := d.fetchChunk(gctx, offset, bm.PartSize)
			if err != nil {
				return err
			}
			expected := int64(bm.PartSize)
			if rem := bm.Size - offset; rem < expected {
				expected = rem
			}
			if int64(len(data)) != expected {
				return fmt.Errorf("short chunk at %d: got %d bytes, expected %d", offset, len(data), expected)
			}
			// Block bytes land before the bitmap marks them done, so a crash
			// can only lose a block, never fake one. No fsync: this guards
			// process crashes, not power loss.
			if _, err := w.WriteAt(data, offset); err != nil {
				return fmt.Errorf("write chunk at offset %d: %w", offset, err)
			}
			return bm.markAndSave(block, bitmapPath)
		})
	}
	if err := eg.Wait(); err != nil {
		if errors.Is(err, errCDNRedirect) {
			// CDN-hosted files cannot resume over raw upload.getFile; fall
			// back to gotd's downloader (it handles the redirect dance) with
			// a full re-download of this file.
			log.FromContext(ctx).Warn("File served from CDN, falling back to plain downloader")
			if err := ignoreMissing(os.Truncate(partPathOf(bitmapPath), 0)); err != nil {
				return fmt.Errorf("truncate part before CDN fallback: %w", err)
			}
			if err := RemoveResumeState(bitmapPath); err != nil {
				return err
			}
			if err := DownloadParallelWithRefresh(ctx, file, meter, ctx, w); err != nil {
				return fmt.Errorf("plain download after CDN redirect: %w", err)
			}
			return nil
		}
		return err
	}
	if !bm.complete() {
		return fmt.Errorf("download finished with missing blocks")
	}
	return nil
}

func partPathOf(bitmapPath string) string {
	return strings.TrimSuffix(bitmapPath, ".bitmap")
}

// ignoreMissing maps a "file does not exist" error to nil: removing or
// truncating an already-absent resume file is success, not a failure.
func ignoreMissing(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// RemoveResumeState deletes the bitmap files of a completed download.
func RemoveResumeState(bitmapPath string) error {
	if err := ignoreMissing(os.Remove(bitmapPath)); err != nil {
		return fmt.Errorf("remove resume bitmap: %w", err)
	}
	if err := ignoreMissing(os.Remove(bitmapPath + ".tmp")); err != nil {
		return fmt.Errorf("remove resume bitmap temp: %w", err)
	}
	return nil
}

// ResumeDirName names the resume subdirectory inside the cache base.
const ResumeDirName = ".resume"

// ResumeDir is the directory holding resumable .part/.bitmap state. It lives
// INSIDE the cache base so the final rename onto a cache file never crosses
// a filesystem boundary — the cache dir is commonly a bind mount, its
// parent is not, and os.Rename across them fails with EXDEV. The startup
// cache clean deliberately spares this subdirectory (cmd/run.go).
func ResumeDir() string {
	base := strings.TrimRight(config.C().Temp.BasePath, "/\\")
	if base == "" {
		base = "."
	}
	return filepath.Join(base, ResumeDirName)
}
