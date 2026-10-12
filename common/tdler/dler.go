package tdler

import (
	"context"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/pkg/consts/tglimit"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

func NewDownloader(file tfile.TGFile) *downloader.Builder {
	return newDownloader(file, nil)
}

// NewDownloaderWithMeter is NewDownloader plus a flood-wait meter that
// observes every part RPC; a nil meter behaves exactly like NewDownloader.
func NewDownloaderWithMeter(file tfile.TGFile, meter *dlutil.FloodWaitMeter) *downloader.Builder {
	return newDownloader(file, meter)
}

func newDownloader(file tfile.TGFile, meter *dlutil.FloodWaitMeter) *downloader.Builder {
	return downloader.NewDownloader().WithPartSize(tglimit.MaxPartSize).
		Download(eofAwareClient{Client: file.Dler(), size: file.Size(), meter: meter}, file.Location()).
		WithThreads(dlutil.BestThreads(file.Size(), config.C().Threads))
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
