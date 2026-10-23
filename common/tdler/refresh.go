package tdler

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/gotd/td/tgerr"
	userclient "github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// ErrSourceGone reports that the source message of a file no longer exists —
// deleted from the chat, or stripped of its media. No retry can fix this.
var ErrSourceGone = errors.New("source message no longer exists")

// IsFileReferenceExpired reports whether err is Telegram rejecting a stale
// file_reference (400 FILE_REFERENCE_EXPIRED). References live roughly two
// hours after the source message was fetched, so any element that waited
// longer than that in a slow batch hits this on its first chunk.
func IsFileReferenceExpired(err error) bool {
	return tgerr.Is(err, "FILE_REFERENCE_EXPIRED")
}

// SendCtxForFile picks the client context that must carry operations tied to
// a file's media — reference refresh, resend — because access hashes and file
// references are account-scoped: files built on the userbot must be operated
// on through the userbot, the bot often cannot read that chat at all.
func SendCtxForFile(ctx context.Context, file tfile.TGFile) *ext.Context {
	if uctx := userclient.GetCtx(); uctx != nil && file.Dler() == uctx.Raw {
		return uctx
	}
	return tgutil.ExtFromContext(ctx)
}

// RefreshFileLocation re-fetches the file's source message and swaps in a
// fresh file_reference, in place. The fetch rides the client the file was
// built with: files downloaded through the userbot must be re-fetched through
// it too — the bot often cannot read that chat at all.
func RefreshFileLocation(ctx context.Context, file tfile.TGFileMessage) error {
	msg := file.Message()
	if msg == nil {
		return errors.New("file has no source message to refresh")
	}
	fetchCtx := SendCtxForFile(ctx, file)
	if fetchCtx == nil {
		return errors.New("no client context available to refresh the file reference")
	}
	fetched, err := tgutil.GetMessagesByIDs(fetchCtx, tgutil.ChatIdFromPeer(msg.GetPeerID()), []int{msg.GetID()})
	if err != nil {
		return fmt.Errorf("refresh fetch: %w", err)
	}
	fresh, ok := fetched[msg.GetID()]
	if !ok {
		return ErrSourceGone
	}
	media, ok := fresh.GetMedia()
	if !ok {
		return fmt.Errorf("%w: message %d carries no media", ErrSourceGone, msg.GetID())
	}
	return file.RefreshLocation(media)
}

// DownloadParallelWithRefresh runs a parallel download and, when Telegram
// rejects the file's now-stale reference, refreshes it from the source
// message and restarts the download from the top, up to maxRefreshes times —
// a slow link can outlive one fresh reference (they last ~2 hours). The
// retry must rebuild the downloader because the location is captured at
// build time. Progress bytes of aborted attempts are counted again —
// cosmetic only.
func DownloadParallelWithRefresh(ctx context.Context, file tfile.TGFile, meter *dlutil.FloodWaitMeter, dlCtx context.Context, wrAt io.WriterAt) error {
	const maxRefreshes = 2
	var err error
	for refresh := 0; ; refresh++ {
		_, err = NewDownloaderWithMeter(file, meter).Parallel(dlCtx, wrAt)
		if err == nil || !IsFileReferenceExpired(err) {
			return err
		}
		msgFile, ok := file.(tfile.TGFileMessage)
		if !ok || refresh >= maxRefreshes {
			return err
		}
		log.FromContext(ctx).Warnf("File reference expired mid-download, refreshing and retrying (attempt %d/%d): %v", refresh+1, maxRefreshes, err)
		if rerr := RefreshFileLocation(ctx, msgFile); rerr != nil {
			return fmt.Errorf("failed to download file: %w (reference refresh: %v)", err, rerr)
		}
	}
}
