package tdler

import (
	"context"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/client/relay"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/common/utils/rpclimit"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/pkg/consts/tglimit"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

func NewDownloader(ctx context.Context, file tfile.TGFile) (*downloader.Builder, func(context.Context)) {
	return NewDownloaderWithMeter(ctx, file, nil)
}

// NewDownloaderWithMeter is NewDownloader plus a flood-wait meter that
// observes every part RPC; a nil meter behaves exactly like NewDownloader.
func NewDownloaderWithMeter(ctx context.Context, file tfile.TGFile, meter *dlutil.FloodWaitMeter) (*downloader.Builder, func(context.Context)) {
	file, cleanup := relay.Prepare(ctx, file)
	return newDownloader(file, meter), cleanup
}

func newDownloader(file tfile.TGFile, meter *dlutil.FloodWaitMeter) *downloader.Builder {
	return downloader.NewDownloader().WithPartSize(tglimit.MaxPartSize).
		Download(eofAwareClient{Client: rpclimit.WrapDownloader(file.Dler()), size: file.Size(), meter: meter}, file.Location()).
		WithThreads(downloadThreads(file))
}

func downloadThreads(file tfile.TGFile) int {
	cfg := config.C()
	if cfg.DownloadThreads > 0 {
		return cfg.DownloadThreads
	}
	threads := dlutil.BestThreads(file.Size(), cfg.Threads)
	if scalable, ok := file.(interface{ WorkerCount() int }); ok {
		// A relay file distributes chunks across helpers. Give each helper the
		// same per-worker concurrency; the global transfer budget still caps the
		// real number of in-flight RPCs in rpclimit.
		threads *= max(scalable.WorkerCount(), 1)
	}
	if cfg.MaxDownloadThreads > 0 {
		threads = min(threads, cfg.MaxDownloadThreads)
	}
	return max(threads, 1)
}

// eofAwareClient answers upload.getFile requests at or past the end of the
// file with an empty chunk. gotd's downloader is size-unaware: for files
// whose size is an exact multiple of the part size it issues one final
// request at offset == size and expects an empty chunk, but Telegram rejects
// it with 400 OFFSET_INVALID and the whole download fails.
type eofAwareClient struct {
	downloader.Client
	size  int64
	meter *dlutil.FloodWaitMeter
}

func (c eofAwareClient) UploadGetFile(ctx context.Context, req *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	if c.size > 0 && req.Offset >= c.size {
		return &tg.UploadFile{}, nil
	}
	if c.meter == nil {
		return c.Client.UploadGetFile(ctx, req)
	}
	start := time.Now()
	file, err := c.Client.UploadGetFile(ctx, req)
	c.meter.Observe(time.Since(start), err)
	return file, err
}

// meteringClient counts part-RPC durations into a FloodWaitMeter for the
// resumable chunk fetcher, which talks to the raw client instead of the
// downloader builder. A nil meter returns the client unchanged.
func meteringClient(client downloader.Client, meter *dlutil.FloodWaitMeter) downloader.Client {
	if meter == nil {
		return client
	}
	return eofAwareClient{Client: client, meter: meter}
}
