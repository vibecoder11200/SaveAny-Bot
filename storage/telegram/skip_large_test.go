package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/celestix/gotgproto/ext"
	storcfg "github.com/krau/SaveAny-Bot/config/storage"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/pkg/enums/ctxkey"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
)

// withPlainTGCtx injects a minimal ext context so save() reaches its size
// checks instead of bailing on the missing telegram context.
func withPlainTGCtx(ctx context.Context) context.Context {
	return tgutil.ExtWithContext(ctx, new(ext.Context))
}

// RT-8: with SkipLarge enabled, an oversized file must surface the
// ErrSkippedLarge sentinel — not a nil "success" that would poison the dedup
// and name ledgers with a file that was never uploaded.
func TestSkipLargeReturnsSentinel(t *testing.T) {
	stor := new(Telegram)
	cfg := &storcfg.TelegramStorageConfig{
		BaseConfig: storcfg.BaseConfig{Name: "tgsentinel"},
		ChatID:     1,
		SkipLarge:  true,
	}
	if err := stor.Init(context.Background(), cfg); err != nil {
		t.Fatalf("init: %v", err)
	}
	ctx := withPlainTGCtx(context.WithValue(context.Background(), ctxkey.ContentLength, int64(MaxUploadFileSize+1)))
	err := stor.Save(ctx, strings.NewReader("x"), "a/b.bin")
	if !errors.Is(err, storagetypes.ErrSkippedLarge) {
		t.Fatalf("oversized Save err = %v, want ErrSkippedLarge", err)
	}

	// The capability reports the same limit the storage enforces.
	limit, ok := stor.SkipLargeLimit(context.Background())
	if !ok || limit != MaxUploadFileSize {
		t.Fatalf("SkipLargeLimit = (%d, %v), want (%d, true)", limit, ok, MaxUploadFileSize)
	}
}

// With SkipLarge off the storage never claims a limit.
func TestSkipLargeLimitDisabled(t *testing.T) {
	stor := new(Telegram)
	cfg := &storcfg.TelegramStorageConfig{BaseConfig: storcfg.BaseConfig{Name: "tgoff"}, ChatID: 1}
	if err := stor.Init(context.Background(), cfg); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, ok := stor.SkipLargeLimit(context.Background()); ok {
		t.Fatal("SkipLargeLimit must be disabled without SkipLarge")
	}
}
