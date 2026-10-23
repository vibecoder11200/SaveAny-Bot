package storagetypes

import (
	"context"
	"errors"

	"github.com/celestix/gotgproto/ext"
	"github.com/gotd/td/tg"
)

// ErrSkippedLarge reports that a storage intentionally dropped a file for
// exceeding its size limit. Callers must treat it as "not saved": no failure,
// no retry, and no dedup/ledger record — the content never arrived.
var ErrSkippedLarge = errors.New("file skipped: larger than storage limit")

// ErrResendPreflight wraps failures detected before the send RPC was issued
// (unresolvable destination peer, …). These are known to have delivered
// nothing, so falling back to the normal download+upload path is safe.
var ErrResendPreflight = errors.New("resend not possible")

// StorageLargeFileSkipper is implemented by storages that silently drop files
// above a size limit (SkipLarge). Knowing the limit up front lets callers
// skip the file before spending a download on it, and keeps the skip
// distinguishable from a real upload success.
type StorageLargeFileSkipper interface {
	// SkipLargeLimit returns the size above which the storage drops files
	// and true; ok is false when the storage does not skip large files.
	SkipLargeLimit(ctx context.Context) (limit int64, ok bool)
}

// ExistingMediaSender is implemented by storages that can re-send media
// already living on Telegram without downloading and re-uploading it.
// Telegram storage implements it via messages.sendMedia with an
// inputMediaDocument/inputMediaPhoto built from a fresh file location.
type ExistingMediaSender interface {
	// SendExistingMedia re-sends the given media into the storage's
	// destination under storagePath (used for bookkeeping only). sendCtx is
	// the client context that fetched the source media: access hashes and
	// file references are account-scoped, so the send must ride the same
	// account that read the source message.
	SendExistingMedia(ctx context.Context, sendCtx *ext.Context, media tg.InputMediaClass, storagePath, caption string) error
}
