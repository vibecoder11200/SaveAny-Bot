package shortcut

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/client/bot/handlers/utils/ruleutil"
	"github.com/krau/SaveAny-Bot/config"
	storcfg "github.com/krau/SaveAny-Bot/config/storage"
	storenum "github.com/krau/SaveAny-Bot/pkg/enums/storage"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
	"github.com/krau/SaveAny-Bot/storage"
)

// fakeStorage is a storage.Storage whose Exists answers from a path set.
type fakeStorage struct {
	name     string
	existing map[string]bool
	saved    []string
}

func (f *fakeStorage) Init(ctx context.Context, cfg storcfg.StorageConfig) error {
	return nil
}
func (f *fakeStorage) Type() storenum.StorageType { return storenum.Local }
func (f *fakeStorage) Name() string               { return f.name }
func (f *fakeStorage) Save(ctx context.Context, r io.Reader, storagePath string) error {
	f.saved = append(f.saved, storagePath)
	return nil
}
func (f *fakeStorage) Exists(ctx context.Context, storagePath string) bool {
	return f.existing[storagePath]
}

var _ storage.Storage = (*fakeStorage)(nil)

// testFile builds a TGFileMessage carrying a message with the given grouped
// id and a filename attribute.
func testFile(t *testing.T, name string, groupID int64) tfile.TGFileMessage {
	t.Helper()
	doc := &tg.Document{
		ID:         100,
		Size:       10,
		MimeType:   "video/mp4",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}},
	}
	media := &tg.MessageMediaDocument{Document: doc}
	msg := &tg.Message{Message: "caption"}
	if groupID != 0 {
		msg.SetGroupedID(groupID)
	}
	file, err := tfile.FromMediaMessage(media, downloader.Client(nil), msg)
	if err != nil {
		t.Fatalf("FromMediaMessage: %v", err)
	}
	return file
}

func setupPartitionEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[db]\npath = '" + filepath.Join(dir, "test.db") + "'\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := config.Init(context.Background(), cfgPath); err != nil {
		t.Fatalf("config init: %v", err)
	}
}

// noRuleTo mirrors the production applyRule fallback: no rule matched, so
// the caller's base dir comes back.
func noRuleTo(dir string) func(tfile.TGFileMessage) (string, ruleutil.MatchedDirPath) {
	return func(tfile.TGFileMessage) (string, ruleutil.MatchedDirPath) {
		return "", ruleutil.MatchedDirPath(dir)
	}
}

func fixedAlbumNamer(now time.Time) func(storage.Storage, string, string) string {
	return func(s storage.Storage, dir, first string) string {
		return albumDirNameAt(context.Background(), s, dir, first, now)
	}
}

var fixedNow = time.Date(2026, 10, 24, 12, 30, 56, 0, time.UTC)

func TestPartitionForceGroupingAlbumSubfolderWithoutRules(t *testing.T) {
	setupPartitionEnv(t)
	stor := &fakeStorage{name: "local1"}
	files := []tfile.TGFileMessage{
		testFile(t, "trip1.jpg", 42),
		testFile(t, "trip2.jpg", 42),
	}
	deps := batchPartitionDeps{
		ctx: context.Background(), strategy: "rename",
		baseStor: stor, baseDir: "watched",
		forceGroup:   true,
		applyRule:    noRuleTo("watched"),
		resolveStor:  func(string) (storage.Storage, error) { return stor, nil },
		exists:       func(_ storage.Storage, p string) bool { return stor.Exists(context.Background(), p) },
		dedupHas:     func(string, string) bool { return false },
		albumNameFor: fixedAlbumNamer(fixedNow),
	}
	elems, _, _, _, err := partitionBatchFiles(files, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(elems) != 2 {
		t.Fatalf("elems = %d, want 2", len(elems))
	}
	// Album subfolder named after the first file, even though no rule
	// returned the NEW-FOR-ALBUM sentinel (ForceGrouping).
	if filepath.ToSlash(elems[0].Path) != "watched/trip1/trip1.jpg" {
		t.Errorf("first element path = %q, want watched/trip1/trip1.jpg", elems[0].Path)
	}
	if filepath.ToSlash(elems[1].Path) != "watched/trip1/trip2.jpg" {
		t.Errorf("second element path = %q, want watched/trip1/trip2.jpg", elems[1].Path)
	}
}

func TestPartitionOverrideSkipsRules(t *testing.T) {
	setupPartitionEnv(t)
	stor := &fakeStorage{name: "local1"}
	otherStor := &fakeStorage{name: "local2"}
	files := []tfile.TGFileMessage{testFile(t, "a.mp4", 0)}
	deps := batchPartitionDeps{
		ctx: context.Background(), strategy: "rename",
		baseStor: stor, baseDir: "default",
		override: &BatchTarget{Stor: otherStor, DirPath: "override/dir"},
		// A rule that would match everything must be ignored.
		applyRule: func(tfile.TGFileMessage) (string, ruleutil.MatchedDirPath) {
			return "local1", ruleutil.MatchedDirPath("rule/dir")
		},
		resolveStor:  func(string) (storage.Storage, error) { return stor, nil },
		exists:       func(_ storage.Storage, p string) bool { return stor.Exists(context.Background(), p) },
		dedupHas:     func(string, string) bool { return false },
		albumNameFor: fixedAlbumNamer(fixedNow),
	}
	elems, _, _, _, err := partitionBatchFiles(files, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(elems) != 1 {
		t.Fatalf("elems = %d, want 1", len(elems))
	}
	if elems[0].Storage.Name() != "local2" {
		t.Errorf("storage = %s, want override storage local2", elems[0].Storage.Name())
	}
	if filepath.ToSlash(elems[0].Path) != "override/dir/a.mp4" {
		t.Errorf("path = %q, want override/dir/a.mp4", elems[0].Path)
	}
}

func TestPartitionSingleMemberAlbumKept(t *testing.T) {
	setupPartitionEnv(t)
	stor := &fakeStorage{name: "local1"}
	// A media group reduced to one member must be kept as a plain file,
	// never dropped.
	files := []tfile.TGFileMessage{testFile(t, "solo.jpg", 7)}
	deps := batchPartitionDeps{
		ctx: context.Background(), strategy: "rename",
		baseStor: stor, baseDir: "watched",
		forceGroup:   true,
		applyRule:    noRuleTo("watched"),
		resolveStor:  func(string) (storage.Storage, error) { return stor, nil },
		exists:       func(_ storage.Storage, p string) bool { return stor.Exists(context.Background(), p) },
		dedupHas:     func(string, string) bool { return false },
		albumNameFor: func(storage.Storage, string, string) string { return "unused" },
	}
	elems, _, _, _, err := partitionBatchFiles(files, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(elems) != 1 {
		t.Fatalf("elems = %d, want 1 (single member must be kept)", len(elems))
	}
	if filepath.ToSlash(elems[0].Path) != "watched/solo.jpg" {
		t.Errorf("path = %q, want watched/solo.jpg (flat, no album dir)", elems[0].Path)
	}
}

func TestPartitionAlbumCollisionSuffix(t *testing.T) {
	setupPartitionEnv(t)
	stor := &fakeStorage{name: "local1", existing: map[string]bool{
		"watched/trip1": true,
	}}
	files := []tfile.TGFileMessage{
		testFile(t, "trip1.jpg", 42),
		testFile(t, "trip2.jpg", 42),
	}
	deps := batchPartitionDeps{
		ctx: context.Background(), strategy: "rename",
		baseStor: stor, baseDir: "watched",
		forceGroup:   true,
		applyRule:    noRuleTo("watched"),
		resolveStor:  func(string) (storage.Storage, error) { return stor, nil },
		exists:       func(_ storage.Storage, p string) bool { return stor.Exists(context.Background(), p) },
		dedupHas:     func(string, string) bool { return false },
		albumNameFor: fixedAlbumNamer(fixedNow),
	}
	elems, _, _, _, err := partitionBatchFiles(files, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(elems) != 2 {
		t.Fatalf("elems = %d, want 2", len(elems))
	}
	// Existing album dir collides: -HHMMSS suffix applies.
	if filepath.ToSlash(elems[0].Path) != "watched/trip1-123056/trip1.jpg" {
		t.Errorf("path = %q, want watched/trip1-123056/trip1.jpg", elems[0].Path)
	}
}

func TestPartitionAskUnreachableWhenPinned(t *testing.T) {
	setupPartitionEnv(t)
	stor := &fakeStorage{name: "local1", existing: map[string]bool{
		"watched/dup.jpg": true,
	}}
	files := []tfile.TGFileMessage{testFile(t, "dup.jpg", 0)}
	deps := batchPartitionDeps{
		ctx: context.Background(),
		// Watch flushes pin a non-interactive strategy: an existing file is
		// skipped outright, never queued as a conflict awaiting an answer.
		strategy:     "skip",
		baseStor:     stor,
		baseDir:      "watched",
		applyRule:    noRuleTo("watched"),
		resolveStor:  func(string) (storage.Storage, error) { return stor, nil },
		exists:       func(_ storage.Storage, p string) bool { return stor.Exists(context.Background(), p) },
		dedupHas:     func(string, string) bool { return false },
		albumNameFor: fixedAlbumNamer(fixedNow),
	}
	elems, skipped, conflicts, _, err := partitionBatchFiles(files, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %v, want none (ASK must be unreachable for pinned strategies)", conflicts)
	}
	if len(elems) != 0 {
		t.Errorf("elems = %d, want 0 (existing file skipped)", len(elems))
	}
	if len(skipped) != 1 {
		t.Errorf("skipped = %v, want the existing file", skipped)
	}
}

func TestAlbumDirName(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		first    string
		existing map[string]bool
		want     string
	}{
		{name: "base name", first: "IMG_1234.jpg", want: "IMG_1234"},
		{name: "no ext", first: "video", want: "video"},
		{name: "collision suffix", first: "album.jpg", existing: map[string]bool{"base/album": true}, want: "album-123056"},
		{name: "double collision falls back", first: "album.jpg", existing: map[string]bool{"base/album": true, "base/album-123056": true}, want: "album-20261024-123056"},
		{name: "unusable name falls back", first: ".mp4", want: "album-20261024-123056"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stor := &fakeStorage{name: "s", existing: tc.existing}
			got := albumDirNameAt(ctx, stor, "base", tc.first, fixedNow)
			if got != tc.want {
				t.Errorf("albumDirNameAt(%q) = %q, want %q", tc.first, got, tc.want)
			}
		})
	}
}
