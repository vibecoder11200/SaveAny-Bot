package tdler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/pkg/consts/tglimit"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// patternData builds deterministic file content: byte i is i%251, so any
// corruption, hole or misplaced chunk is detectable by rescanning the file.
func patternData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}

// fakeClient serves a whole in-memory file over upload.getFile and records
// every offset it was asked for. failFor, when set, can reject a request
// (keyed by the request's sequence number) to simulate mid-download errors.
type fakeClient struct {
	mu       sync.Mutex
	data     []byte
	requests []int64
	failFor  func(nth int, offset int64) error
}

func (c *fakeClient) UploadGetFile(ctx context.Context, req *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failFor != nil {
		if err := c.failFor(len(c.requests), req.Offset); err != nil {
			return nil, err
		}
	}
	c.requests = append(c.requests, req.Offset)
	if req.Offset < 0 || req.Offset >= int64(len(c.data)) {
		return nil, tgerr.New(400, "OFFSET_INVALID")
	}
	end := int(req.Offset) + req.Limit
	if end > len(c.data) {
		end = len(c.data)
	}
	return &tg.UploadFile{Bytes: c.data[req.Offset:end]}, nil
}

func (c *fakeClient) UploadGetFileHashes(ctx context.Context, req *tg.UploadGetFileHashesRequest) ([]tg.FileHash, error) {
	return nil, nil
}

func (c *fakeClient) UploadReuploadCDNFile(ctx context.Context, req *tg.UploadReuploadCDNFileRequest) ([]tg.FileHash, error) {
	return nil, nil
}

func (c *fakeClient) UploadGetCDNFileHashes(ctx context.Context, req *tg.UploadGetCDNFileHashesRequest) ([]tg.FileHash, error) {
	return nil, nil
}

func (c *fakeClient) UploadGetWebFile(ctx context.Context, req *tg.UploadGetWebFileRequest) (*tg.UploadWebFile, error) {
	return nil, nil
}

var _ downloader.Client = (*fakeClient)(nil)

func (c *fakeClient) requestedOffsets() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.requests...)
}

func newTestFile(t *testing.T, client downloader.Client, size int64) tfile.TGFile {
	t.Helper()
	return tfile.NewTGFile(&tg.InputDocumentFileLocation{}, client, size, "test.bin")
}

func writeInitialPart(t *testing.T, path string, data []byte, doneBlocks ...int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("create part file: %v", err)
	}
	for _, block := range doneBlocks {
		start := block * tglimit.MaxPartSize
		end := min(start+tglimit.MaxPartSize, len(data))
		if _, err := f.WriteAt(data[start:end], int64(start)); err != nil {
			t.Fatalf("seed part block %d: %v", block, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close part file: %v", err)
	}
}

func writeInitialBitmap(t *testing.T, path string, size int64, doneBlocks ...int) {
	t.Helper()
	bm := newResumeBitmap(size)
	for _, block := range doneBlocks {
		bm.markDone(block)
	}
	data, err := json.Marshal(bm)
	if err != nil {
		t.Fatalf("marshal bitmap: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write bitmap: %v", err)
	}
}

func mustContent(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestDownloadResumableFull(t *testing.T) {
	dir := t.TempDir()
	data := patternData(2*1024*1024 + 512*1024) // 3 blocks, last one short
	client := &fakeClient{data: data}
	bitmap := filepath.Join(dir, "x.part.bitmap")
	part := filepath.Join(dir, "x.part")

	err := DownloadResumable(context.Background(), newTestFile(t, client, int64(len(data))), mustPartWriter(t, part), 2, bitmap, nil)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if got := mustContent(t, part); string(got) != string(data) {
		t.Fatalf("downloaded content mismatch: %d bytes", len(got))
	}
	// A full bitmap is left on disk; removal is DownloadToCache's job.
	bm, err := loadResumeBitmap(bitmap)
	if err != nil || bm == nil || !bm.complete() {
		t.Fatalf("bitmap after download = %+v, %v", bm, err)
	}
}

// mustPartWriter opens (creating if needed) the .part file the download
// writes into — mirroring what DownloadToCache does.
func mustPartWriter(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("open part file: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestDownloadResumableSkipsDoneBlocks(t *testing.T) {
	dir := t.TempDir()
	data := patternData(2*1024*1024 + 512*1024)
	client := &fakeClient{data: data}
	// Block 0 must not be fetched again — asking for it fails the test.
	client.failFor = func(nth int, offset int64) error {
		if offset == 0 {
			return fmt.Errorf("refetched completed block at offset %d", offset)
		}
		return nil
	}
	bitmap := filepath.Join(dir, "x.part.bitmap")
	part := filepath.Join(dir, "x.part")
	writeInitialPart(t, part, data, 0)
	writeInitialBitmap(t, bitmap, int64(len(data)), 0)

	err := DownloadResumable(context.Background(), newTestFile(t, client, int64(len(data))), mustPartWriter(t, part), 2, bitmap, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := mustContent(t, part); string(got) != string(data) {
		t.Fatalf("resumed content mismatch: %d bytes", len(got))
	}
}

func TestDownloadResumableResumesAfterFailure(t *testing.T) {
	dir := t.TempDir()
	data := patternData(3*1024*1024 + 100*1024) // 4 blocks
	client := &fakeClient{data: data}
	// Single-threaded so block order is deterministic: blocks 0 and 1
	// succeed, then the download dies.
	client.failFor = func(nth int, offset int64) error {
		if nth >= 2 {
			return tgerr.New(500, "INTERNAL")
		}
		return nil
	}
	bitmap := filepath.Join(dir, "x.part.bitmap")
	part := filepath.Join(dir, "x.part")

	first := DownloadResumable(context.Background(), newTestFile(t, client, int64(len(data))), mustPartWriter(t, part), 1, bitmap, nil)
	if first == nil {
		t.Fatal("first download unexpectedly succeeded")
	}
	bm, err := loadResumeBitmap(bitmap)
	if err != nil || bm == nil || !bm.isDone(0) || !bm.isDone(1) {
		t.Fatalf("bitmap after interrupted download = %+v, %v", bm, err)
	}

	// The retry must not refetch the two blocks already on disk.
	client.failFor = func(nth int, offset int64) error {
		if offset == 0 || offset == 1024*1024 {
			return fmt.Errorf("refetched completed block at offset %d", offset)
		}
		return nil
	}
	if err := DownloadResumable(context.Background(), newTestFile(t, client, int64(len(data))), mustPartWriter(t, part), 1, bitmap, nil); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if got := mustContent(t, part); string(got) != string(data) {
		t.Fatalf("resumed content mismatch: %d bytes", len(got))
	}
}

func TestDownloadResumableResetsIncompatibleBitmap(t *testing.T) {
	dir := t.TempDir()
	data := patternData(2 * 1024 * 1024)
	client := &fakeClient{data: data}
	bitmap := filepath.Join(dir, "x.part.bitmap")
	part := filepath.Join(dir, "x.part")
	// A bitmap for a different (smaller) size must not survive: the tail of
	// the stale .part file would corrupt the new download.
	writeInitialPart(t, part, data[:1024*1024], 0, 1)
	writeInitialBitmap(t, bitmap, 1024*1024, 0, 1)

	if err := DownloadResumable(context.Background(), newTestFile(t, client, int64(len(data))), mustPartWriter(t, part), 2, bitmap, nil); err != nil {
		t.Fatalf("restart download: %v", err)
	}
	if got := mustContent(t, part); string(got) != string(data) {
		t.Fatalf("restarted content mismatch: %d bytes", len(got))
	}
	if offsets := client.requestedOffsets(); len(offsets) < 2 {
		t.Fatalf("incompatible bitmap must trigger a full refetch, got requests %v", offsets)
	}
}

func TestDownloadResumableResetsBitmapWhenPartMissing(t *testing.T) {
	dir := t.TempDir()
	data := patternData(2 * 1024 * 1024)
	client := &fakeClient{data: data}
	bitmap := filepath.Join(dir, "x.part.bitmap")
	part := filepath.Join(dir, "x.part")
	// Bitmap claims everything is done, but the .part file is gone: claiming
	// reuse would ship nothing.
	writeInitialBitmap(t, bitmap, int64(len(data)), 0, 1)

	if err := DownloadResumable(context.Background(), newTestFile(t, client, int64(len(data))), mustPartWriter(t, part), 2, bitmap, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got := mustContent(t, part); string(got) != string(data) {
		t.Fatalf("content mismatch: %d bytes", len(got))
	}
	if offsets := client.requestedOffsets(); len(offsets) < 2 {
		t.Fatalf("missing part file must trigger a full refetch, got requests %v", offsets)
	}
}

func TestDownloadResumableFileReferenceExpiredWithoutMessage(t *testing.T) {
	dir := t.TempDir()
	client := &fakeClient{data: patternData(1024 * 1024)}
	client.failFor = func(nth int, offset int64) error {
		return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
	}
	bitmap := filepath.Join(dir, "x.part.bitmap")

	err := DownloadResumable(context.Background(), newTestFile(t, client, 1024*1024), mustPartWriter(t, filepath.Join(dir, "x.part")), 1, bitmap, nil)
	if err == nil {
		t.Fatal("expected failure on unrefreshable file reference")
	}
}

func setupResumeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[temp]\nbase_path = '" + filepath.ToSlash(filepath.Join(dir, "cache")) + "'\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := config.Init(context.Background(), cfgPath); err != nil {
		t.Fatalf("config init: %v", err)
	}
	return filepath.Join(dir, "cache")
}

func TestDownloadToCacheMovesPartAndCleansState(t *testing.T) {
	cacheBase := setupResumeConfig(t)
	data := patternData(2*1024*1024 + 512*1024)
	client := &fakeClient{data: data}
	file := newTestFile(t, client, int64(len(data)))
	localPath := filepath.Join(cacheBase, "xid_test.bin")
	logger := log.New(io.Discard)

	if err := DownloadToCache(context.Background(), logger, file, localPath, "doc:1|default|/dest/test.bin", nil, nil); err != nil {
		t.Fatalf("download to cache: %v", err)
	}
	if got := mustContent(t, localPath); string(got) != string(data) {
		t.Fatalf("cached content mismatch: %d bytes", len(got))
	}
	if entries, err := os.ReadDir(ResumeDir()); err != nil || len(entries) != 0 {
		t.Fatalf("resume dir should be empty after success, has %d entries (%v)", len(entries), err)
	}
}

func TestDownloadToCacheReusesCompletedPartAfterRestart(t *testing.T) {
	cacheBase := setupResumeConfig(t)
	data := patternData(2 * 1024 * 1024)
	// A prior run downloaded everything, then the process died before the
	// rename; the server is now unreachable — only the recorded state can
	// finish the job.
	if err := os.MkdirAll(ResumeDir(), 0o755); err != nil {
		t.Fatalf("mkdir resume dir: %v", err)
	}
	partPath := filepath.Join(ResumeDir(), resumeStem("doc:1|default|/dest/test.bin")+".part")
	bitmapPath := partPath + ".bitmap"
	writeInitialPart(t, partPath, data, 0, 1)
	writeInitialBitmap(t, bitmapPath, int64(len(data)), 0, 1)

	client := &fakeClient{data: data}
	client.failFor = func(nth int, offset int64) error {
		return fmt.Errorf("server unreachable")
	}
	localPath := filepath.Join(cacheBase, "xid2_test.bin")

	if err := DownloadToCache(context.Background(), log.New(io.Discard), newTestFile(t, client, int64(len(data))), localPath, "doc:1|default|/dest/test.bin", nil, nil); err != nil {
		t.Fatalf("reuse completed part: %v", err)
	}
	if got := mustContent(t, localPath); string(got) != string(data) {
		t.Fatalf("reused content mismatch: %d bytes", len(got))
	}
	if len(client.requestedOffsets()) != 0 {
		t.Fatalf("no bytes should be fetched on reuse, got %v", client.requestedOffsets())
	}
}

func TestResumeDirIsSiblingOfCacheBase(t *testing.T) {
	cacheBase := setupResumeConfig(t)
	want := filepath.Clean(cacheBase + "-resume")
	if got := filepath.Clean(ResumeDir()); got != want {
		t.Fatalf("ResumeDir = %q, want sibling %q", got, want)
	}
	// The default config value carries a trailing slash; the resume dir must
	// still be a sibling, not a subdirectory the boot clean would wipe.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[temp]\nbase_path = '" + filepath.ToSlash(cacheBase) + "/'\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := config.Init(context.Background(), cfgPath); err != nil {
		t.Fatalf("config init: %v", err)
	}
	if got := filepath.Clean(ResumeDir()); got != want {
		t.Fatalf("ResumeDir with trailing slash = %q, want sibling %q", got, want)
	}
}

func TestDownloadResumeKey(t *testing.T) {
	a := &fakeFile{fid: "doc:42", name: "a.bin"}
	b := &fakeFile{fid: "doc:42", name: "a.bin"}
	if got1, got2 := DownloadResumeKey(a, "default", "/dest/a.bin"), DownloadResumeKey(b, "default", "/dest/a.bin"); got1 != got2 {
		t.Fatalf("same identity must produce the same key: %q vs %q", got1, got2)
	}
	base := DownloadResumeKey(a, "default", "/dest/a.bin")
	if got := DownloadResumeKey(a, "other", "/dest/a.bin"); got == base {
		t.Fatal("different storage must produce a different key")
	}
	if got := DownloadResumeKey(a, "default", "/dest/b.bin"); got == base {
		t.Fatal("different destination must produce a different key")
	}
	if got := DownloadResumeKey(&fakeFile{fid: "doc:43", name: "a.bin"}, "default", "/dest/a.bin"); got == base {
		t.Fatal("different file id must produce a different key")
	}
}

type fakeFile struct {
	name string
	fid  string
}

func (f *fakeFile) Location() tg.InputFileLocationClass { return nil }
func (f *fakeFile) Dler() downloader.Client             { return nil }
func (f *fakeFile) Size() int64                         { return 0 }
func (f *fakeFile) Name() string                        { return f.name }
func (f *fakeFile) SetName(name string)                 { f.name = name }
func (f *fakeFile) FileUniqueID() string                { return f.fid }

var _ tfile.TGFile = (*fakeFile)(nil)
