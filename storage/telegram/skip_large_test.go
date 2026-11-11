package telegram

import (
	"bytes"
	"context"
	"errors"
	"testing"

	stor_config "github.com/krau/SaveAny-Bot/config/storage"
	"github.com/krau/SaveAny-Bot/pkg/enums/ctxkey"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
)

func skipLargeStorage(t *testing.T, skipLarge bool) *Telegram {
	t.Helper()
	cfg := &stor_config.TelegramStorageConfig{
		BaseConfig: stor_config.BaseConfig{Name: "skip-test"},
		ChatID:     -1001234,
		SkipLarge:  skipLarge,
	}
	st := &Telegram{}
	if err := st.Init(context.Background(), cfg); err != nil {
		t.Fatalf("init telegram storage: %v", err)
	}
	return st
}

// TestSkipLargeLimit exposes the policy limit to callers so a file the
// storage would refuse is never downloaded.
func TestSkipLargeLimit(t *testing.T) {
	on := skipLargeStorage(t, true)
	limit, ok := on.SkipLargeLimit(context.Background())
	if !ok || limit != MaxUploadFileSize {
		t.Fatalf("SkipLargeLimit(skip on) = %d, %v; want %d, true", limit, ok, MaxUploadFileSize)
	}
	off := skipLargeStorage(t, false)
	if _, ok := off.SkipLargeLimit(context.Background()); ok {
		t.Fatal("SkipLargeLimit(skip off) must report not-skipping")
	}
}

// TestSaveWithContextSkippedLargeSentinel: with SkipLarge on and a file over
// the account limit, the storage returns the ErrSkippedLarge sentinel instead
// of a silent nil success — callers must know nothing was uploaded.
func TestSaveWithContextSkippedLargeSentinel(t *testing.T) {
	st := skipLargeStorage(t, true)
	ctx := context.WithValue(context.Background(), ctxkey.ContentLength, int64(MaxUploadFileSize+1))
	err := st.saveWithContext(ctx, nil, bytes.NewReader(make([]byte, 16)), "big.bin", nil)
	if !errors.Is(err, storagetypes.ErrSkippedLarge) {
		t.Fatalf("saveWithContext(oversize, SkipLarge) = %v; want ErrSkippedLarge", err)
	}
}

// TestInspectBatchItemSkipLargeIsRoutingOnly: the batch planner must keep
// treating "SkipLarge on + oversize" as a routing decision (single save,
// which returns the sentinel) and must NOT conflate it with the splitSize
// branch: a file between splitSize and the account limit still saves via
// single-path splitting, never a skip.
func TestInspectBatchItemSkipLargeIsRoutingOnly(t *testing.T) {
	st := skipLargeStorage(t, true)
	// splitSize defaults to the account limit; set a small one to exercise
	// the splitSize band.
	st.config.SplitSizeMB = 1 // 1MB splitSize
	splitSize := st.splitSize(MaxUploadFileSize)

	between := splitSize + 1024 // splitSize < size <= maxUploadSize
	if between > MaxUploadFileSize {
		t.Fatalf("test setup: %d exceeds the account limit", between)
	}
	item := storagetypes.BatchItem{
		StoragePath: "b.bin",
		Size:        between,
		Reader:      bytes.NewReader(make([]byte, 16)),
	}
	routed, err := st.inspectBatchItem(nil, item)
	if err != nil {
		t.Fatalf("inspectBatchItem: %v", err)
	}
	if !routed.useSingleSave {
		t.Fatal("file larger than splitSize must route to the single save path")
	}
	// The single save path itself must NOT skip it: the policy predicate
	// fires only past the account limit.
	if st.skipsByPolicy(nil, between) {
		t.Fatalf("size %d (splitSize %d, max %d) must still save via split, not skip", between, splitSize, MaxUploadFileSize)
	}
	if !st.skipsByPolicy(nil, MaxUploadFileSize+1) {
		t.Fatal("size beyond the account limit with SkipLarge on must skip")
	}
	off := skipLargeStorage(t, false)
	if off.skipsByPolicy(nil, MaxUploadFileSize+1) {
		t.Fatal("SkipLarge off must never skip (saves with split instead)")
	}
}
