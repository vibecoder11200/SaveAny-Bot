package tdler

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/pkg/consts/tglimit"
)

// guardFakeFile is a minimal tfile.TGFile for the guard/resume test; the
// document location carries an identity the resume bitmap accepts.
type guardFakeFile struct {
	location tg.InputFileLocationClass
	dler     downloader.Client
	size     int64
}

func (f *guardFakeFile) Location() tg.InputFileLocationClass { return f.location }
func (f *guardFakeFile) Dler() downloader.Client             { return f.dler }
func (f *guardFakeFile) Size() int64                         { return f.size }
func (f *guardFakeFile) Name() string                        { return "f.bin" }
func (f *guardFakeFile) SetName(string)                      {}
func (f *guardFakeFile) FileUniqueID() string                { return "doc:42" }

// stubChunkClient serves deterministic chunks and counts per-offset calls;
// offsets listed in blockSet park until the context is canceled, then fail.
type stubChunkClient struct {
	mu        sync.Mutex
	calls     map[int64]int
	blockSet  map[int64]bool
	started   chan struct{}
	startOnce sync.Once
}

func (c *stubChunkClient) UploadGetFile(ctx context.Context, req *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	c.mu.Lock()
	c.calls[req.Offset]++
	block := c.blockSet[req.Offset]
	c.mu.Unlock()
	if c.started != nil {
		c.startOnce.Do(func() { close(c.started) })
	}
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	chunk := make([]byte, req.Limit)
	seed := byte(req.Offset/int64(req.Limit)) + 1
	for i := range chunk {
		chunk[i] = seed
	}
	return &tg.UploadFile{Bytes: chunk}, nil
}

func (c *stubChunkClient) UploadGetCDNFileHashes(ctx context.Context, req *tg.UploadGetCDNFileHashesRequest) ([]tg.FileHash, error) {
	return nil, nil
}

func (c *stubChunkClient) UploadGetFileHashes(ctx context.Context, req *tg.UploadGetFileHashesRequest) ([]tg.FileHash, error) {
	return nil, nil
}

func (c *stubChunkClient) UploadReuploadCDNFile(ctx context.Context, req *tg.UploadReuploadCDNFileRequest) ([]tg.FileHash, error) {
	return nil, nil
}

func (c *stubChunkClient) UploadGetWebFile(ctx context.Context, req *tg.UploadGetWebFileRequest) (*tg.UploadWebFile, error) {
	return nil, nil
}

func (c *stubChunkClient) callCount(offset int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[offset]
}

// newGuardFakeFile builds the test file: size = 2 full blocks of partSize.
func newGuardFakeFile(c downloader.Client, partSize int64) *guardFakeFile {
	return &guardFakeFile{
		location: &tg.InputDocumentFileLocation{ID: 42, AccessHash: 1},
		dler:     c,
		size:     2 * partSize,
	}
}

func TestDownloadResumable_CancelThenResume(t *testing.T) {
	partSize := int64(tglimit.MaxPartSize)
	dir := t.TempDir()
	partPath := filepath.Join(dir, "f.part")
	bitmapPath := partPath + ".bitmap"

	// Leg 1: block 0 succeeds, block 1 parks until the test cancels. The
	// bitmap must still be flushed with block 0 marked and the .part file
	// must hold block 0's bytes.
	stub := &stubChunkClient{
		calls:    map[int64]int{},
		blockSet: map[int64]bool{partSize: true},
		started:  make(chan struct{}),
	}
	file := newGuardFakeFile(stub, partSize)

	partFile, openErr := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0o644)
	if openErr != nil {
		t.Fatalf("open part file: %v", openErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stub.started
		// Give block 0 time to land before cancelling.
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := DownloadResumable(ctx, file, partFile, 1, bitmapPath, nil)
	if err == nil {
		t.Fatal("expected cancellation error from the first leg")
	}
	if err := partFile.Close(); err != nil {
		t.Fatalf("close part file: %v", err)
	}

	partData, readErr := os.ReadFile(partPath)
	if readErr != nil {
		t.Fatalf("read .part: %v", readErr)
	}
	want0 := byte(1)
	if int64(len(partData)) < partSize || partData[0] != want0 {
		t.Fatalf("block 0 bytes not persisted after cancel (len=%d)", len(partData))
	}

	// Leg 2: a healthy client resumes. Block 0 must NOT be re-fetched.
	stub2 := &stubChunkClient{calls: map[int64]int{}}
	file2 := newGuardFakeFile(stub2, partSize)
	resumed, openErr := os.OpenFile(partPath, os.O_RDWR, 0o644)
	if openErr != nil {
		t.Fatalf("reopen part file: %v", openErr)
	}
	defer resumed.Close()
	if err := DownloadResumable(context.Background(), file2, resumed, 1, bitmapPath, nil); err != nil {
		t.Fatalf("resume leg failed: %v", err)
	}
	if got := stub2.callCount(0); got != 0 {
		t.Fatalf("block 0 re-fetched on resume (%d calls), resume state not reused", got)
	}
	if got := stub2.callCount(partSize); got != 1 {
		t.Fatalf("block 1 fetched %d times on resume, want exactly 1", got)
	}
	final, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatalf("read final .part: %v", err)
	}
	if int64(len(final)) != 2*partSize {
		t.Fatalf("final size %d, want %d", len(final), 2*partSize)
	}
	if final[0] != 1 || final[partSize] != 2 {
		t.Fatal("final block contents wrong after resume")
	}
}
