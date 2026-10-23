package batchtfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/database"
	storenum "github.com/krau/SaveAny-Bot/pkg/enums/storage"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"

	storcfg "github.com/krau/SaveAny-Bot/config/storage"
	"io"
)

// fakeBatchStorage records SaveBatch calls without touching any backend.
type fakeBatchStorage struct {
	name       string
	savedPaths []string
}

func (f *fakeBatchStorage) Init(context.Context, storcfg.StorageConfig) error { return nil }
func (f *fakeBatchStorage) Type() storenum.StorageType                        { return storenum.Local }
func (f *fakeBatchStorage) Name() string {
	if f.name == "" {
		return "FakeBatch"
	}
	return f.name
}
func (f *fakeBatchStorage) Save(_ context.Context, _ io.Reader, _ string) error {
	return nil
}
func (f *fakeBatchStorage) Exists(context.Context, string) bool { return false }
func (f *fakeBatchStorage) SaveBatch(_ context.Context, items []storagetypes.BatchItem) error {
	for _, item := range items {
		f.savedPaths = append(f.savedPaths, item.StoragePath)
	}
	return nil
}

var (
	_ storage.Storage           = (*fakeBatchStorage)(nil)
	_ storage.StorageBatchSaver = (*fakeBatchStorage)(nil)
)

// fakeFile is a TGFile stand-in with a preset file-id and no real media.
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

func setupDedupTestDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[db]\npath = '" + filepath.Join(dir, "batch_dedup_test.db") + "'\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := config.Init(context.Background(), cfgPath); err != nil {
		t.Fatalf("config init: %v", err)
	}
	database.Init(context.Background())
	t.Cleanup(func() {
		_ = database.CloseDB()
	})
}

func itemPhaseByID(t *testing.T, task *Task, id string) ItemPhase {
	t.Helper()
	for _, item := range task.Items() {
		if item.ID == id {
			return item.Phase
		}
	}
	t.Fatalf("item %s not found", id)
	return 0
}

func toElemValues(elems []*TaskElement) []TaskElement {
	values := make([]TaskElement, 0, len(elems))
	for _, elem := range elems {
		values = append(values, *elem)
	}
	return values
}

// RT-3: an album element deduped mid-batch must drop out of the upload
// pipeline via the errSkippedExists sentinel — never poison its siblings or
// fail the group.
func TestAlbumWithMiddleItemDeduped(t *testing.T) {
	setupDedupTestDB(t)
	ctx := context.Background()
	stor := &fakeBatchStorage{}

	elems := make([]*TaskElement, 5)
	for i := range elems {
		localPath := filepath.Join(t.TempDir(), "elem.bin")
		if err := os.WriteFile(localPath, []byte{byte(i)}, 0o644); err != nil {
			t.Fatalf("write cache file: %v", err)
		}
		elems[i] = &TaskElement{
			ID:        string(rune('a' + i)),
			Storage:   stor,
			Path:      filepath.Join("album", string(rune('a'+i))+".bin"),
			File:      &fakeFile{name: string(rune('a'+i)) + ".bin"},
			localPath: localPath,
		}
	}
	task := NewBatchTGFileTask("t1", ctx, toElemValues(elems), &recordingTracker{}, true)
	task.UserID = 1

	results := make([]downloadResult, len(elems))
	for i, elem := range elems {
		results[i] = downloadResult{elem: elem}
	}
	// Simulate element #3: its dedup check inside downloadElement marked the
	// item deduplicated and returned the skip sentinel.
	task.markItemDeduped(elems[2].ID)
	results[2] = downloadResult{elem: elems[2], err: errSkippedExists}

	if err := task.uploadBatchResults(ctx, results); err != nil {
		t.Fatalf("uploadBatchResults: %v", err)
	}

	if len(stor.savedPaths) != 4 {
		t.Fatalf("uploaded %d items, want 4 (deduped one excluded): %v", len(stor.savedPaths), stor.savedPaths)
	}
	for i, elem := range elems {
		phase := itemPhaseByID(t, task, elem.ID)
		if i == 2 {
			if phase != ItemPhaseDeduped {
				t.Fatalf("middle item phase = %v, want deduped", phase)
			}
			continue
		}
		if phase != ItemPhaseCompleted {
			t.Fatalf("item %s phase = %v, want completed", elem.ID, phase)
		}
	}
	if got := task.DedupedCount(); got != 1 {
		t.Fatalf("DedupedCount = %d, want 1", got)
	}
}

// The dedup check helper must mark and report hits, and respect the
// per-storage and per-user scopes.
func TestDedupSkipByFileID(t *testing.T) {
	setupDedupTestDB(t)
	ctx := context.Background()
	logger := log.FromContext(ctx)

	stor := &fakeBatchStorage{}
	elem := &TaskElement{
		ID:      "x1",
		Storage: stor,
		Path:    "dir/one.bin",
		File:    &fakeFile{name: "one.bin", fid: "doc:77"},
	}
	task := NewBatchTGFileTask("t2", ctx, []TaskElement{*elem}, &recordingTracker{}, true)
	task.UserID = 1

	if task.dedupSkipByFileID(ctx, logger, elem) {
		t.Fatal("unknown file-id must not dedup")
	}
	if err := database.RecordDedup(ctx, 1, "FakeBatch", "doc:77", "", 1, "other/renamed.bin"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !task.dedupSkipByFileID(ctx, logger, elem) {
		t.Fatal("recorded file-id must dedup despite the renamed ledger path")
	}
	if phase := itemPhaseByID(t, task, "x1"); phase != ItemPhaseDeduped {
		t.Fatalf("phase = %v, want deduped", phase)
	}

	// Same file-id in another storage must not dedup (per-storage semantics).
	stor2 := &fakeBatchStorage{name: "FakeBatch2"}
	elem2 := &TaskElement{ID: "x2", Storage: stor2, Path: "dir/two.bin", File: &fakeFile{name: "two.bin", fid: "doc:77"}}
	task2 := NewBatchTGFileTask("t3", ctx, []TaskElement{*elem2}, &recordingTracker{}, true)
	task2.UserID = 1
	if task2.dedupSkipByFileID(ctx, logger, elem2) {
		t.Fatal("file-id in a different storage must not dedup")
	}

	// User-less tasks never dedup (RT-5).
	task3 := NewBatchTGFileTask("t4", ctx, []TaskElement{*elem}, &recordingTracker{}, true)
	task3.UserID = 0
	if task3.dedupSkipByFileID(ctx, logger, elem) {
		t.Fatal("user-less task must never dedup")
	}
}

// All elements skipped (recovery re-run over already-saved files) is a clean
// no-op, not an error.
func TestUploadBatchResultsAllSkipped(t *testing.T) {
	setupDedupTestDB(t)
	ctx := context.Background()
	stor := &fakeBatchStorage{}
	elems := make([]*TaskElement, 2)
	for i := range elems {
		elems[i] = &TaskElement{
			ID:        string(rune('a' + i)),
			Storage:   stor,
			Path:      filepath.Join("dir", string(rune('a'+i))+".bin"),
			File:      &fakeFile{name: string(rune('a'+i)) + ".bin"},
			localPath: filepath.Join(t.TempDir(), "missing.bin"),
		}
	}
	task := NewBatchTGFileTask("t5", ctx, toElemValues(elems), &recordingTracker{}, true)
	task.UserID = 1
	results := []downloadResult{
		{elem: elems[0], err: errSkippedExists},
		{elem: elems[1], err: errSkippedExists},
	}
	if err := task.uploadBatchResults(ctx, results); err != nil {
		t.Fatalf("all-skipped must be a no-op, got: %v", err)
	}
	if len(stor.savedPaths) != 0 {
		t.Fatalf("nothing should upload, got %v", stor.savedPaths)
	}
}

// RT-9: hash-deduping items 2-3 of a 4-item album after download must keep
// the progress attribution aligned — exactly items 1 and 4 reach the batch
// upload, and the upload callback maps progress onto those two elements only.
func TestAlbumHashDedupLockstepFilter(t *testing.T) {
	setupDedupTestDB(t)
	ctx := context.Background()
	stor := &fakeBatchStorage{}

	elems := make([]*TaskElement, 4)
	for i := range elems {
		localPath := filepath.Join(t.TempDir(), "elem.bin")
		if err := os.WriteFile(localPath, []byte{byte(10 + i)}, 0o644); err != nil {
			t.Fatalf("write cache file: %v", err)
		}
		elems[i] = &TaskElement{
			ID:        string(rune('a' + i)),
			Storage:   stor,
			Path:      filepath.Join("album", string(rune('a'+i))+".bin"),
			File:      &fakeFile{name: string(rune('a'+i)) + ".bin"},
			localPath: localPath,
		}
		// Pre-record items 2 and 3 ("b", "c") as already saved.
		if i == 1 || i == 2 {
			skip, _, err := database.DedupCheckHash(ctx, 1, stor.Name(), localPath)
			if err != nil || skip {
				t.Fatalf("precheck %d: skip=%v err=%v", i, skip, err)
			}
			hash, err := fsutil.Sha256File(localPath)
			if err != nil {
				t.Fatalf("hash %d: %v", i, err)
			}
			if err := database.RecordDedup(ctx, 1, stor.Name(), "", hash, 1, elems[i].Path); err != nil {
				t.Fatalf("record %d: %v", i, err)
			}
		}
	}
	task := NewBatchTGFileTask("t6", ctx, toElemValues(elems), &recordingTracker{}, true)
	task.UserID = 1

	results := make([]downloadResult, len(elems))
	for i, elem := range elems {
		results[i] = downloadResult{elem: elem}
	}
	if err := task.uploadBatchResults(ctx, results); err != nil {
		t.Fatalf("uploadBatchResults: %v", err)
	}

	want := []string{elems[0].Path, elems[3].Path}
	if len(stor.savedPaths) != 2 || stor.savedPaths[0] != want[0] || stor.savedPaths[1] != want[1] {
		t.Fatalf("uploaded %v, want exactly %v", stor.savedPaths, want)
	}
	for i, elem := range elems {
		phase := itemPhaseByID(t, task, elem.ID)
		wantPhase := ItemPhaseCompleted
		if i == 1 || i == 2 {
			wantPhase = ItemPhaseDeduped
		}
		if phase != wantPhase {
			t.Fatalf("item %s phase = %v, want %v", elem.ID, phase, wantPhase)
		}
	}
	// Items 1 and 4 were uploaded: their hashes must now be recorded so a
	// future save dedups on content.
	for _, i := range []int{0, 3} {
		skip, _, err := database.DedupCheckHash(ctx, 1, stor.Name(), elems[i].localPath)
		if err != nil || !skip {
			t.Fatalf("post-upload record check %d: skip=%v err=%v", i, skip, err)
		}
	}
}

// fakeSkipperStorage drops files above a fixed limit (SkipLarge policy).
type fakeSkipperStorage struct {
	*fakeBatchStorage
	limit int64
}

func (f *fakeSkipperStorage) SkipLargeLimit(context.Context) (int64, bool) {
	return f.limit, true
}

func TestSkipLargeElem(t *testing.T) {
	setupDedupTestDB(t)
	ctx := context.Background()
	logger := log.FromContext(ctx)

	base := &fakeBatchStorage{}
	stor := &fakeSkipperStorage{fakeBatchStorage: base, limit: 100}
	elem := &TaskElement{
		ID:      "big1",
		Storage: stor,
		Path:    "dir/big.bin",
		File:    &sizeFile{fakeFile: fakeFile{name: "big.bin"}, size: 101},
	}
	task := NewBatchTGFileTask("t7", ctx, []TaskElement{*elem}, &recordingTracker{}, true)
	task.UserID = 1

	if !task.skipLargeElem(ctx, logger, elem) {
		t.Fatal("oversized element must be skipped pre-download")
	}
	if phase := itemPhaseByID(t, task, "big1"); phase != ItemPhaseSkipped {
		t.Fatalf("phase = %v, want skipped", phase)
	}

	// Within the limit: no skip.
	elem2 := &TaskElement{
		ID:      "ok1",
		Storage: stor,
		Path:    "dir/ok.bin",
		File:    &sizeFile{fakeFile: fakeFile{name: "ok.bin"}, size: 50},
	}
	task2 := NewBatchTGFileTask("t8", ctx, []TaskElement{*elem2}, &recordingTracker{}, true)
	task2.UserID = 1
	if task2.skipLargeElem(ctx, logger, elem2) {
		t.Fatal("element within the limit must not be skipped")
	}

	// Unknown size: let the storage decide at upload time.
	elem3 := &TaskElement{
		ID:      "unk1",
		Storage: stor,
		Path:    "dir/unk.bin",
		File:    &fakeFile{name: "unk.bin"},
	}
	task3 := NewBatchTGFileTask("t9", ctx, []TaskElement{*elem3}, &recordingTracker{}, true)
	task3.UserID = 1
	if task3.skipLargeElem(ctx, logger, elem3) {
		t.Fatal("unknown-size element must not be pre-skipped")
	}
}

// sizeFile is a fakeFile with a real size.
type sizeFile struct {
	fakeFile
	size int64
}

func (f *sizeFile) Size() int64 { return f.size }

// fakeResendStorage is a batch storage whose resend outcome is scripted.
type fakeResendStorage struct {
	*fakeBatchStorage
	sendErr error
	sent    int
}

func (f *fakeResendStorage) SendExistingMedia(ctx context.Context, sendCtx *ext.Context, media tg.InputMediaClass, storagePath, caption string) error {
	f.sent++
	return f.sendErr
}

// fakeMsgFile carries a document location plus its source message, the shape
// tryResend needs.
type fakeMsgFile struct {
	fakeFile
	location *tg.InputDocumentFileLocation
	message  *tg.Message
}

func (f *fakeMsgFile) Location() tg.InputFileLocationClass { return f.location }
func (f *fakeMsgFile) Message() *tg.Message                { return f.message }
func (f *fakeMsgFile) RefreshLocation(tg.MessageMediaClass) error {
	return nil
}

var _ tfile.TGFileMessage = (*fakeMsgFile)(nil)

func resendTestElem(stor storage.Storage) *TaskElement {
	return &TaskElement{
		ID:      "r1",
		Storage: stor,
		Path:    "dir/r.mp4",
		File: &fakeMsgFile{
			fakeFile: fakeFile{name: "r.mp4", fid: "doc:9"},
			location: &tg.InputDocumentFileLocation{ID: 9, AccessHash: 99, FileReference: []byte("ref")},
			message:  &tg.Message{ID: 7},
		},
	}
}

func TestTryResendOutcomes(t *testing.T) {
	setupDedupTestDB(t)
	logger := log.FromContext(context.Background())

	// Success: item completed, ledger gains the file-id row.
	stor := &fakeResendStorage{fakeBatchStorage: &fakeBatchStorage{}}
	elem := resendTestElem(stor)
	task := NewBatchTGFileTask("r-ok", context.Background(), []TaskElement{*elem}, &recordingTracker{}, true)
	task.UserID = 1
	ctx := tgutil.ExtWithContext(context.Background(), new(ext.Context))

	handled, err := task.tryResend(ctx, logger, elem)
	if !handled || err != nil {
		t.Fatalf("success resend: handled=%v err=%v", handled, err)
	}
	if stor.sent != 1 {
		t.Fatalf("sent = %d, want 1", stor.sent)
	}
	if phase := itemPhaseByID(t, task, "r1"); phase != ItemPhaseCompleted {
		t.Fatalf("phase = %v, want completed", phase)
	}
	if !database.DedupHasFileID(ctx, 1, stor.Name(), "doc:9") {
		t.Fatal("resend success must record the file-id in the dedup ledger")
	}

	// Fallback-eligible failure: falls through to the download+upload path,
	// item untouched.
	storFB := &fakeResendStorage{fakeBatchStorage: &fakeBatchStorage{name: "FakeResendFB"}, sendErr: fmt.Errorf("%w: no peer", storagetypes.ErrResendPreflight)}
	elemFB := resendTestElem(storFB)
	taskFB := NewBatchTGFileTask("r-fb", context.Background(), []TaskElement{*elemFB}, &recordingTracker{}, true)
	taskFB.UserID = 1
	handled, err = taskFB.tryResend(ctx, logger, elemFB)
	if handled || err != nil {
		t.Fatalf("fallback resend: handled=%v err=%v", handled, err)
	}
	if phase := itemPhaseByID(t, taskFB, "r1"); phase != ItemPhaseWaiting {
		t.Fatalf("phase = %v, want waiting (fallback leaves the item alone)", phase)
	}

	// Ambiguous failure: element terminally failed and accounted for. Distinct
	// storage name: the ledger rows above must not satisfy this check.
	storAmb := &fakeResendStorage{fakeBatchStorage: &fakeBatchStorage{name: "FakeResendAmb"}, sendErr: errors.New("connection reset mid-send")}
	elemAmb := resendTestElem(storAmb)
	taskAmb := NewBatchTGFileTask("r-amb", context.Background(), []TaskElement{*elemAmb}, &recordingTracker{}, true)
	taskAmb.UserID = 1
	handled, err = taskAmb.tryResend(ctx, logger, elemAmb)
	if !handled || err == nil {
		t.Fatalf("ambiguous resend: handled=%v err=%v", handled, err)
	}
	if phase := itemPhaseByID(t, taskAmb, "r1"); phase != ItemPhaseFailed {
		t.Fatalf("phase = %v, want failed (ambiguous resend must be accounted)", phase)
	}
	if database.DedupHasFileID(ctx, 1, storAmb.Name(), "doc:9") {
		t.Fatal("failed resend must not record into the ledger")
	}
}

// Overwrite tasks bypass the file-id dedup even on a ledger hit.
func TestDisableDedupBypassesFileID(t *testing.T) {
	setupDedupTestDB(t)
	ctx := context.Background()
	logger := log.FromContext(ctx)

	stor := &fakeBatchStorage{}
	elem := &TaskElement{
		ID:      "ow1",
		Storage: stor,
		Path:    "dir/ow.bin",
		File:    &fakeFile{name: "ow.bin", fid: "doc:55"},
	}
	if err := database.RecordDedup(ctx, 1, stor.Name(), "doc:55", "", 1, "old/path.bin"); err != nil {
		t.Fatalf("record: %v", err)
	}

	task := NewBatchTGFileTask("ow-on", ctx, []TaskElement{*elem}, &recordingTracker{}, true)
	task.UserID = 1
	task.DisableDedup = true
	if task.dedupSkipByFileID(ctx, logger, elem) {
		t.Fatal("overwrite task must not be deduped by file-id")
	}

	task2 := NewBatchTGFileTask("ow-off", ctx, []TaskElement{*elem}, &recordingTracker{}, true)
	task2.UserID = 1
	if !task2.dedupSkipByFileID(ctx, logger, elem) {
		t.Fatal("non-overwrite task must still dedup by file-id")
	}
}
