package telegram

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/duke-git/lancet/v2/slice"
	"github.com/duke-git/lancet/v2/validator"
	"github.com/gabriel-vasile/mimetype"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/config"
	storconfig "github.com/krau/SaveAny-Bot/config/storage"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/consts/tglimit"
	"github.com/krau/SaveAny-Bot/pkg/enums/ctxkey"
	storenum "github.com/krau/SaveAny-Bot/pkg/enums/storage"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
	"github.com/rs/xid"
	"golang.org/x/time/rate"
)

const (
	// https://core.telegram.org/api/config#upload-max-fileparts-default
	DefaultSplitSize         = 4000 * 524288 // 4000 * 512 KB
	MaxUploadFileSize        = 4000 * 524288 // 4000 * 512 KB
	PremiumMaxUploadFileSize = 8000 * 524288 // 8000 * 512 KB
)

type Telegram struct {
	config  storconfig.TelegramStorageConfig
	limiter *rate.Limiter
}

// album kind of a prepared media item. Telegram only groups photos with
// videos; plain documents form their own file albums, and MIXING a plain
// document into a photo/video album makes sendMultiMedia reject the whole
// request (MEDIA_INVALID/MEDIA_EMPTY).
const (
	mediaKindPhoto = "photo"
	mediaKindVideo = "video"
	mediaKindDoc   = "doc"
)

type preparedMedia struct {
	peer     tg.InputPeerClass
	uploader *uploader.Uploader
	media    message.MultiMediaOption
	kind     string
	// rawFile, filename, mime and captionOverride allow rebuilding the item
	// as a plain document when its group turns out to mix kinds.
	rawFile         tg.InputFileClass
	filename        string
	mime            string
	captionOverride *string
}

type batchMediaItem struct {
	item          storagetypes.BatchItem
	index         int
	chatID        int64
	albumEligible bool
	useSingleSave bool
}

func (t *Telegram) Init(ctx context.Context, cfg storconfig.StorageConfig) error {
	telegramConfig, ok := cfg.(*storconfig.TelegramStorageConfig)
	if !ok {
		return fmt.Errorf("failed to cast telegram config")
	}
	if err := telegramConfig.Validate(); err != nil {
		return err
	}
	t.config = *telegramConfig
	if t.config.RateLimit <= 0 || t.config.RateBurst <= 0 {
		t.config.RateLimit = 2
		t.config.RateBurst = 1
	}
	t.limiter = rate.NewLimiter(rate.Every(time.Duration(t.config.RateLimit)*time.Second), t.config.RateBurst)
	return nil
}

func (t *Telegram) Type() storenum.StorageType {
	return storenum.Telegram
}

func (t *Telegram) Name() string {
	return t.config.Name
}

// Exists consults the local index of files this bot has saved through this
// storage: Telegram offers no way to ask whether a file already exists in a
// chat, so the ledger is the only signal. Files saved before the index
// existed, or added to the chat by hand, are not detected.
func (t *Telegram) Exists(ctx context.Context, storagePath string) bool {
	return database.TelegramSavedExists(ctx, t.Name(), path.Clean(storagePath))
}

// recordSaved indexes paths this storage just saved successfully. Best
// effort: an indexing failure never fails an upload that already succeeded.
func (t *Telegram) recordSaved(ctx context.Context, paths ...string) {
	_ = database.RecordTelegramSaved(ctx, t.Name(), paths...)
}

// recordAfterSave passes the upload result through, indexing the path once
// the upload actually succeeded.
func (t *Telegram) recordAfterSave(ctx context.Context, err error, storagePath string) error {
	if err == nil {
		t.recordSaved(ctx, storagePath)
	}
	return err
}

// SendExistingMedia re-sends media that already lives on Telegram into this
// storage's configured chat — no download, no re-upload. The destination is
// pinned to the configured chat id: the storage path is bookkeeping only and
// must never steer the message into an attacker-chosen chat. The send rides
// sendCtx, the account that fetched the source media, because access hashes
// and file references are account-scoped.
//
// The media/request structures carry access hashes and file references and
// must never be logged — only scalar fields of the outcome may be.
func (t *Telegram) SendExistingMedia(ctx context.Context, sendCtx *ext.Context, media tg.InputMediaClass, storagePath, caption string) error {
	storagePath = path.Clean(storagePath)
	if sendCtx == nil {
		return fmt.Errorf("%w: no client context to send from", storagetypes.ErrResendPreflight)
	}
	// RT-14a pins the destination to the configured chat; a storage path
	// that redirects to another chat cannot be represented by a resend, so
	// fall back to the upload path, which honors the redirect.
	if _, chatID := t.target(sendCtx, storagePath); chatID != t.config.ChatID {
		return fmt.Errorf("%w: storage path targets chat %d, resend only supports the configured chat %d", storagetypes.ErrResendPreflight, chatID, t.config.ChatID)
	}
	peer := tryGetInputPeer(sendCtx, t.config.ChatID)
	if peer == nil || peer.Zero() {
		return fmt.Errorf("%w: no input peer for chat %d on the sending account", storagetypes.ErrResendPreflight, t.config.ChatID)
	}
	if err := t.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit failed: %w", err)
	}
	var captionOpts []message.StyledTextOption
	if caption != "" {
		captionOpts = append(captionOpts, styling.Plain(caption))
	}
	_, err := t.sendBuilder(sendCtx.Sender.To(peer)).Media(ctx, message.Media(media, captionOpts...))
	if err != nil {
		return err
	}
	t.recordSaved(ctx, storagePath)
	return nil
}

func (t *Telegram) Save(ctx context.Context, r io.Reader, storagePath string) error {
	return t.save(ctx, r, storagePath, nil)
}

// SaveWithProgress saves a file while reporting Telegram-confirmed upload
// progress after each uploaded part.
func (t *Telegram) SaveWithProgress(
	ctx context.Context,
	r io.Reader,
	storagePath string,
	onProgress func(uploaded, total int64),
) error {
	size := contentLength(ctx)
	return t.save(ctx, r, storagePath, newUploadProgress(size, onProgress))
}

// SkipLargeLimit reports the size above which this storage drops uploads
// when SkipLarge is enabled, so callers can skip before downloading.
func (t *Telegram) SkipLargeLimit(ctx context.Context) (int64, bool) {
	if !t.config.SkipLarge {
		return 0, false
	}
	return maxUploadFileSize(tgutil.ExtFromContext(ctx)), true
}

func (t *Telegram) save(ctx context.Context, r io.Reader, storagePath string, progress *uploadProgress) error {
	storagePath = path.Clean(storagePath)
	captionOverride := sourceCaptionOverride(ctx)
	tctx := tgutil.ExtFromContext(ctx)
	if tctx == nil {
		return fmt.Errorf("failed to get telegram context")
	}
	size := contentLength(ctx)
	maxUploadSize := maxUploadFileSize(tctx)
	if t.config.SkipLarge && size > maxUploadSize {
		log.FromContext(ctx).Warnf("Skipping file larger than Telegram limit (%d bytes): %d bytes", maxUploadSize, size)
		// Sentinel, not nil: callers must know nothing was uploaded, so no
		// ledger or dedup record is written for this file.
		return storagetypes.ErrSkippedLarge
	}
	splitSize := t.splitSize(maxUploadSize)
	if size > splitSize {
		filename, chatID := t.target(tctx, storagePath)
		if filename == "" {
			if rs, ok := r.(io.ReadSeeker); ok {
				mtype, err := mimetype.DetectReader(rs)
				if err != nil {
					return fmt.Errorf("failed to detect mimetype: %w", err)
				}
				filename = xid.New().String() + mtype.Extension()
				if _, err := rs.Seek(0, io.SeekStart); err != nil {
					return fmt.Errorf("failed to seek reader: %w", err)
				}
			}
		}
		upler := t.newUploader(tctx, size, progress)
		peer := tryGetInputPeer(tctx, chatID)
		if peer == nil || peer.Zero() {
			return fmt.Errorf("failed to get input peer for chat ID %d", chatID)
		}
		if err := t.limiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limit failed: %w", err)
		}
		if t.config.SplitLargeVideo {
			rs, ok := r.(io.ReadSeeker)
			if ok {
				mtype, detectErr := mimetype.DetectReader(rs)
				if _, seekErr := rs.Seek(0, io.SeekStart); seekErr != nil {
					return fmt.Errorf("failed to seek large file after mimetype detection: %w", seekErr)
				}
				if detectErr != nil {
					log.FromContext(ctx).Warnf("Failed to detect large file type, falling back to ZIP split: %s", detectErr)
				} else if strings.HasPrefix(mtype.String(), "video/") {
					parts, cleanup, splitErr := createLosslessVideoParts(
						ctx,
						rs,
						filename,
						size,
						splitSize,
					)
					if splitErr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						log.FromContext(ctx).Warnf("Lossless video split failed, falling back to ZIP split: %s", splitErr)
					} else {
						defer cleanup()
						log.FromContext(ctx).Infof("Uploading oversized video as %d lossless-playable parts", len(parts))
						for _, part := range parts {
							log.FromContext(ctx).Infof("Prepared lossless video part %s (%d bytes)", part.Name, part.Size)
						}
						return t.recordAfterSave(ctx, t.uploadLosslessVideoParts(ctx, tctx, storagePath, parts, captionOverride, progress), storagePath)
					}
					if _, seekErr := rs.Seek(0, io.SeekStart); seekErr != nil {
						return fmt.Errorf("failed to seek large video before ZIP fallback: %w", seekErr)
					}
				}
			}
		}
		return t.recordAfterSave(ctx, t.splitUpload(tctx, r, filename, upler, peer, size, splitSize, progress), storagePath)
	}

	if err := t.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit failed: %w", err)
	}
	prepared, err := t.prepareMedia(ctx, tctx, r, storagePath, size, nil, progress)
	if err != nil {
		return err
	}
	_, err = t.sendBuilder(tctx.Sender.
		WithUploader(prepared.uploader).
		To(prepared.peer)).
		Media(ctx, prepared.media)
	return t.recordAfterSave(ctx, err, storagePath)
}

func contentLength(ctx context.Context) int64 {
	if length := ctx.Value(ctxkey.ContentLength); length != nil {
		if size, ok := length.(int64); ok {
			return size
		}
	}
	return -1
}

func sourceCaptionOverride(ctx context.Context) *string {
	caption, ok := storagetypes.SourceCaptionFromContext(ctx)
	if !ok {
		return nil
	}
	return &caption
}

func maxUploadFileSize(tctx *ext.Context) int64 {
	if tctx != nil && tctx.Self != nil && !tctx.Self.GetBot() && tctx.Self.GetPremium() {
		return PremiumMaxUploadFileSize
	}
	return MaxUploadFileSize
}

func (t *Telegram) splitSize(maxUploadSize int64) int64 {
	splitSize := t.config.SplitSizeMB * 1024 * 1024
	if splitSize <= 0 {
		return maxUploadSize
	}
	return min(splitSize, maxUploadSize)
}

func (t *Telegram) target(tctx *ext.Context, storagePath string) (string, int64) {
	// 去除前导斜杠并分隔路径, 当 len(parts):
	// ==0, 存储到配置文件中的 chat_id, 随机文件名
	// ==1, 视作只有文件名, 存储到配置文件中的 chat_id
	// >=2, parts[0]: 视作要存储到的 chat_id, 最后一项为 filename
	parts := slice.Compact(strings.Split(strings.TrimPrefix(storagePath, "/"), "/"))
	filename := ""
	chatID := t.config.ChatID
	if len(parts) >= 1 {
		filename = parts[len(parts)-1]
	}
	if len(parts) >= 2 && validator.IsAlphaNumeric(parts[0]) {
		cid, err := tgutil.ParseChatID(tctx, parts[0])
		if err != nil {
			log.FromContext(tctx).Warnf("Failed to parse chat ID from path, using configured chat_id: %s", err)
			cid = chatID
		}
		chatID = cid
	}
	return filename, chatID
}

func (t *Telegram) newUploader(tctx *ext.Context, size int64, progress *uploadProgress) *uploader.Uploader {
	upler := uploader.NewUploader(tctx.Raw).
		WithPartSize(tglimit.MaxUploadPartSize).
		WithThreads(dlutil.BestThreads(size, config.C().Threads))
	if progress != nil {
		upler = upler.WithProgress(progress)
	}
	return upler
}

func mediaCaption(filename string, override *string) []message.StyledTextOption {
	if override == nil {
		return []message.StyledTextOption{styling.Plain(filename)}
	}
	if *override == "" {
		return nil
	}
	return []message.StyledTextOption{styling.Plain(*override)}
}

func (t *Telegram) prepareMedia(
	ctx context.Context,
	tctx *ext.Context,
	r io.Reader,
	storagePath string,
	size int64,
	captionOverride *string,
	progress *uploadProgress,
) (*preparedMedia, error) {
	storagePath = path.Clean(storagePath)
	filename, chatID := t.target(tctx, storagePath)
	upler := t.newUploader(tctx, size, progress)
	peer := tryGetInputPeer(tctx, chatID)
	if peer == nil || peer.Zero() {
		return nil, fmt.Errorf("failed to get input peer for chat ID %d", chatID)
	}

	rs, seekable := r.(io.ReadSeeker)
	var mtype *mimetype.MIME
	if seekable {
		var err error
		mtype, err = mimetype.DetectReader(rs)
		if err != nil {
			return nil, fmt.Errorf("failed to detect mimetype: %w", err)
		}
		if filename == "" {
			filename = xid.New().String() + mtype.Extension()
		}
		if _, err := rs.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to seek reader: %w", err)
		}
	}

	var file tg.InputFileClass
	var err error
	if size <= 0 {
		file, err = upler.FromReader(ctx, filename, r)
	} else {
		file, err = upler.Upload(ctx, uploader.NewUpload(filename, r, size))
	}
	if err != nil {
		return nil, fmt.Errorf("failed to upload file to telegram: %w", err)
	}
	caption := mediaCaption(filename, captionOverride)
	forceFile := t.config.ForceFile
	if mtype != nil && strings.HasPrefix(mtype.String(), "image/") && size >= tglimit.MaxPhotoSize {
		forceFile = true
	}
	doc := message.UploadedDocument(file, caption...).
		Filename(filename).
		ForceFile(forceFile)
	if mtype != nil {
		doc = doc.MIME(mtype.String())
	}
	var media message.MultiMediaOption = doc
	kind := mediaKindDoc
	if mtype != nil && rs != nil {
		switch mtypeStr := mtype.String(); {
		case strings.HasPrefix(mtypeStr, "video/"):
			thumb, err := extractThumbFrame(rs)
			if err == nil {
				thumb, err := upler.FromBytes(ctx, "thumb.jpg", thumb)
				if err == nil {
					doc = doc.Thumb(thumb)
				}
			}
			_, seekErr := rs.Seek(0, io.SeekStart)
			var info *VideoMetadata
			if seekErr != nil {
				err = seekErr
			} else if mtypeStr == "video/mp4" {
				info, err = getMP4Meta(rs)
				if err != nil || !usableVideoMeta(info) {
					// gomedia failed OR produced degenerate values (HEVC,
					// odd boxes, zero dimensions in tkhd): ffprobe reads the
					// actual streams and handles every container and codec.
					rs.Seek(0, io.SeekStart)
					if probe, perr := getVideoMetadata(rs); perr == nil {
						info, err = probe, nil
					}
				}
			} else {
				info, err = getVideoMetadata(rs)
			}
			// A video attribute with zero duration or dimensions makes
			// Telegram reject the whole send (MEDIA_INVALID); without usable
			// metadata the file goes out as a plain document instead.
			if err == nil && usableVideoMeta(info) {
				media = doc.Video().
					Duration(time.Duration(info.Duration)*time.Second).
					Resolution(info.Width, info.Height).
					SupportsStreaming()
				kind = mediaKindVideo
			} else {
				media = doc
				kind = mediaKindDoc
			}
		case strings.HasPrefix(mtypeStr, "audio/"):
			media = doc.Audio().Title(filename)
		case mtypeStr == "image/jpeg" || mtypeStr == "image/png":
			// Telegram's photo slot is jpeg-class: exotic stills (HEIC, AVIF,
			// TIFF...) sent as InputMediaUploadedPhoto fail with
			// PHOTO_EXT_INVALID — those must travel as documents.
			media = message.UploadedPhoto(file, caption...)
			kind = mediaKindPhoto
		}
	}
	mimeStr := ""
	if mtype != nil {
		mimeStr = mtype.String()
	}
	return &preparedMedia{
		peer:            peer,
		uploader:        upler,
		media:           media,
		kind:            kind,
		rawFile:         file,
		filename:        filename,
		mime:            mimeStr,
		captionOverride: captionOverride,
	}, nil
}

// SaveBatch preserves each source photo/video group as a Telegram album.
func (t *Telegram) SaveBatch(ctx context.Context, items []storagetypes.BatchItem) error {
	return t.saveBatch(ctx, items, nil)
}

// SaveBatchWithProgress preserves source media groups while reporting native
// Telegram upload progress for each input item.
func (t *Telegram) SaveBatchWithProgress(
	ctx context.Context,
	items []storagetypes.BatchItem,
	onProgress func(index int, uploaded, total int64),
) error {
	return t.saveBatch(ctx, items, onProgress)
}

func (t *Telegram) saveBatch(
	ctx context.Context,
	items []storagetypes.BatchItem,
	onProgress func(index int, uploaded, total int64),
) error {
	tctx := tgutil.ExtFromContext(ctx)
	if tctx == nil {
		return fmt.Errorf("failed to get telegram context")
	}

	inspected := make([]batchMediaItem, 0, len(items))
	for index, item := range items {
		mediaItem, err := t.inspectBatchItem(tctx, item)
		if err != nil {
			return err
		}
		mediaItem.index = index
		inspected = append(inspected, mediaItem)
	}
	for _, group := range planMediaGroups(inspected) {
		if err := t.saveMediaGroup(ctx, tctx, group, onProgress); err != nil {
			return err
		}
	}
	return nil
}

func (t *Telegram) inspectBatchItem(tctx *ext.Context, item storagetypes.BatchItem) (batchMediaItem, error) {
	_, chatID := t.target(tctx, path.Clean(item.StoragePath))
	result := batchMediaItem{item: item, chatID: chatID}
	maxUploadSize := maxUploadFileSize(tctx)
	if (t.config.SkipLarge && item.Size > maxUploadSize) ||
		item.Size > t.splitSize(maxUploadSize) {
		result.useSingleSave = true
		return result, nil
	}
	if _, err := item.Reader.Seek(0, io.SeekStart); err != nil {
		return result, fmt.Errorf("failed to seek batch item before mimetype detection: %w", err)
	}
	mtype, err := mimetype.DetectReader(item.Reader)
	if err != nil {
		return result, fmt.Errorf("failed to detect batch item mimetype: %w", err)
	}
	if _, err := item.Reader.Seek(0, io.SeekStart); err != nil {
		return result, fmt.Errorf("failed to seek batch item: %w", err)
	}
	mtypeStr := mtype.String()
	forceFile := t.config.ForceFile || strings.HasPrefix(mtypeStr, "image/") && item.Size >= tglimit.MaxPhotoSize
	// Same media-kind rule as prepareMedia: only jpeg-class stills may join
	// photo/video albums; other images travel as single documents.
	result.albumEligible = !forceFile && (strings.HasPrefix(mtypeStr, "video/") ||
		mtypeStr == "image/jpeg" || mtypeStr == "image/png")
	return result, nil
}

func planMediaGroups(items []batchMediaItem) [][]batchMediaItem {
	groups := make([][]batchMediaItem, 0, len(items))
	for i := 0; i < len(items); {
		item := items[i]
		if item.useSingleSave || !item.albumEligible || item.item.SourceGroupKey == "" {
			groups = append(groups, items[i:i+1])
			i++
			continue
		}
		end := i + 1
		for end < len(items) && end-i < tglimit.MaxAlbumItems {
			next := items[end]
			if next.useSingleSave || !next.albumEligible || next.chatID != item.chatID || next.item.SourceGroupKey != item.item.SourceGroupKey {
				break
			}
			end++
		}
		groups = append(groups, items[i:end])
		i = end
	}
	return groups
}

func batchItemUploadProgress(
	mediaItem batchMediaItem,
	onProgress func(index int, uploaded, total int64),
) *uploadProgress {
	if onProgress == nil {
		return nil
	}
	return newUploadProgress(mediaItem.item.Size, func(uploaded, total int64) {
		onProgress(mediaItem.index, uploaded, total)
	})
}

func (t *Telegram) saveMediaGroup(
	ctx context.Context,
	tctx *ext.Context,
	group []batchMediaItem,
	onProgress func(index int, uploaded, total int64),
) error {
	if len(group) == 1 && group[0].useSingleSave {
		mediaItem := group[0]
		item := mediaItem.item
		if _, err := item.Reader.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("failed to seek batch item: %w", err)
		}
		itemCtx := context.WithValue(ctx, ctxkey.ContentLength, item.Size)
		if item.PreserveCaption {
			itemCtx = storagetypes.WithSourceCaption(itemCtx, item.Caption)
		}
		if onProgress == nil {
			return t.Save(itemCtx, item.Reader, item.StoragePath)
		}
		return t.SaveWithProgress(itemCtx, item.Reader, item.StoragePath, func(uploaded, total int64) {
			onProgress(mediaItem.index, uploaded, total)
		})
	}

	var (
		prepared []preparedMedia
		err      error
	)
	// Retry counts TOTAL attempts, and even a Retry below 1 must allow the
	// first one.
	maxAttempts := max(config.C().Retry, 1)
	for attempt := 1; ; attempt++ {
		prepared, err = t.trySendGroup(ctx, tctx, group, onProgress)
		if err == nil {
			return nil
		}
		// A deterministic media rejection cannot be retried into success —
		// another round of re-uploads would only burn bandwidth.
		if isMediaRejectedByTelegram(err) {
			break
		}
		if attempt >= maxAttempts {
			return err
		}
	}
	if len(group) == 1 || len(prepared) == 0 {
		return err
	}
	// Telegram rejected the album as a whole: the group send is atomic,
	// nothing was delivered, so the items go out one by one — one
	// problematic file must not doom its whole group. The uploaded file
	// handles are reused, so the fallback sends no bytes again; losing the
	// album grouping beats losing the files.
	log.FromContext(ctx).Warnf("Album send rejected by Telegram (%v), falling back to %d individual message(s)", err, len(prepared))
	savedPaths := groupStoragePaths(group)
	for _, p := range prepared {
		builder := t.sendBuilder(tctx.Sender.WithUploader(p.uploader).To(p.peer))
		if _, serr := builder.Media(ctx, p.media); serr != nil {
			return fmt.Errorf("album send rejected (%v); single send of %s failed too: %w", err, p.filename, serr)
		}
	}
	t.recordSaved(ctx, savedPaths...)
	return nil
}

// groupStoragePaths returns the cleaned ledger paths of a group's items, in
// order.
func groupStoragePaths(group []batchMediaItem) []string {
	paths := make([]string, len(group))
	for i, mediaItem := range group {
		paths[i] = path.Clean(mediaItem.item.StoragePath)
	}
	return paths
}

// trySendGroup prepares (uploads) every item of the group and sends them as
// one album — or as a single message for one-item groups. It returns the
// prepared items even on send failure, so callers can reuse the already
// uploaded handles for a per-item fallback.
func (t *Telegram) trySendGroup(
	ctx context.Context,
	tctx *ext.Context,
	group []batchMediaItem,
	onProgress func(index int, uploaded, total int64),
) ([]preparedMedia, error) {
	if err := t.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit failed: %w", err)
	}

	prepared := make([]preparedMedia, 0, len(group))
	for _, mediaItem := range group {
		item := mediaItem.item
		if _, err := item.Reader.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to seek batch item: %w", err)
		}
		var captionOverride *string
		if item.PreserveCaption {
			captionOverride = &item.Caption
		}
		progress := batchItemUploadProgress(mediaItem, onProgress)
		media, err := t.prepareMedia(ctx, tctx, item.Reader, item.StoragePath, item.Size, captionOverride, progress)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, *media)
	}

	savedPaths := groupStoragePaths(group)
	builder := t.sendBuilder(tctx.Sender.WithUploader(prepared[0].uploader).To(prepared[0].peer))
	if len(prepared) == 1 {
		_, err := builder.Media(ctx, prepared[0].media)
		if err == nil {
			t.recordSaved(ctx, savedPaths...)
		}
		return prepared, err
	}
	// Telegram groups photos with videos, and documents with documents —
	// but never a plain document into a photo/video album. A video whose
	// metadata could not be read goes out as a plain document; when such
	// an item lands in a media group, the whole group degrades to a pure
	// file album instead of being rejected wholesale.
	if albumMixesDocWithMedia(prepared) {
		log.FromContext(ctx).Warnf("Album mixes plain documents with photos/videos (%s) — sending the whole group as a file album", describeAlbum(prepared))
		for i := range prepared {
			fileDoc := message.File(prepared[i].rawFile, mediaCaption(prepared[i].filename, prepared[i].captionOverride)...).
				Filename(prepared[i].filename)
			if prepared[i].mime != "" {
				fileDoc = fileDoc.MIME(prepared[i].mime)
			}
			prepared[i].media = fileDoc
		}
	}
	media := make([]message.MultiMediaOption, len(prepared))
	for i := range prepared {
		media[i] = prepared[i].media
	}
	if _, err := builder.Album(ctx, media[0], media[1:]...); err != nil {
		return prepared, fmt.Errorf("failed to send media album (%s): %w", describeAlbum(prepared), err)
	}
	t.recordSaved(ctx, savedPaths...)
	return prepared, nil
}

// isMediaRejectedByTelegram reports whether err is Telegram refusing the
// media itself (not a transport/peer problem) — the deterministic cases
// where the album request was rejected atomically and per-item sends are
// still worth trying.
func isMediaRejectedByTelegram(err error) bool {
	return tgerr.Is(err, "MEDIA_INVALID") ||
		tgerr.Is(err, "MEDIA_EMPTY") ||
		tgerr.Is(err, "PHOTO_EXT_INVALID") ||
		tgerr.Is(err, "PHOTO_INVALID_DIMENSIONS") ||
		tgerr.Is(err, "VIDEO_INVALID")
}

// albumMixesDocWithMedia reports whether the group combines a plain document
// with photos or attribute-carrying videos — a combination Telegram's
// sendMultiMedia rejects outright.
func albumMixesDocWithMedia(prepared []preparedMedia) bool {
	hasDoc, hasMedia := false, false
	for _, p := range prepared {
		if p.kind == mediaKindDoc {
			hasDoc = true
		} else {
			hasMedia = true
		}
	}
	return hasDoc && hasMedia
}

// describeAlbum renders a compact per-item composition for diagnostics.
func describeAlbum(prepared []preparedMedia) string {
	parts := make([]string, 0, len(prepared))
	for _, p := range prepared {
		parts = append(parts, p.kind+"("+p.filename+")")
	}
	return strings.Join(parts, ", ")
}

func (t *Telegram) CannotStream() string {
	return "Telegram storage must use a ReaderSeeker"
}

// sendBuilder narrows a located request builder to the sender every send
// path uses, routing it into the configured forum topic when one is set: a
// topic is addressed by replying to its root message id (see
// core.telegram.org/api/forum). With no topic configured the builder is
// untouched, so non-forum chats keep the flat-chat behavior.
func (t *Telegram) sendBuilder(rb *message.RequestBuilder) *message.Builder {
	if t.config.TopicID == 0 {
		return &rb.Builder
	}
	return rb.Reply(t.config.TopicID)
}

func (t *Telegram) splitUpload(
	ctx *ext.Context,
	r io.Reader,
	filename string,
	upler *uploader.Uploader,
	peer tg.InputPeerClass,
	fileSize, splitSize int64,
	progress *uploadProgress,
) error {
	tempId := xid.New().String()
	outputBase := filepath.Join(config.C().Temp.BasePath, tempId, strings.Split(filename, ".")[0])
	defer func() {
		if err := os.RemoveAll(filepath.Join(config.C().Temp.BasePath, tempId)); err != nil {
			log.FromContext(ctx).Warnf("Failed to cleanup temp split files: %s", err)
		}
	}()
	if err := CreateSplitZip(ctx, r, fileSize, filename, outputBase, splitSize); err != nil {
		return fmt.Errorf("failed to create split zip: %w", err)
	}
	matched, err := filepath.Glob(outputBase + ".z*")
	if err != nil {
		return fmt.Errorf("failed to glob split files: %w", err)
	}
	inputFiles := make([]tg.InputFileClass, 0, len(matched))
	if progress != nil {
		var uploadSize int64
		for _, partPath := range matched {
			partInfo, err := os.Stat(partPath)
			if err != nil {
				return fmt.Errorf("failed to stat split part %s: %w", partPath, err)
			}
			uploadSize += partInfo.Size()
		}
		progress.reset(uploadSize)
	}
	for _, partPath := range matched {
		// 串行上传, 不然容易被tg风控
		err = func() error {
			partFile, err := os.Open(partPath)
			if err != nil {
				return fmt.Errorf("failed to open split part %s: %w", partPath, err)
			}
			defer partFile.Close()
			partInfo, err := partFile.Stat()
			if err != nil {
				return fmt.Errorf("failed to stat split part %s: %w", partPath, err)
			}
			partFileSize := partInfo.Size()
			partName := filepath.Base(partPath)
			partInputFile, err := upler.Upload(ctx, uploader.NewUpload(partName, partFile, partFileSize))
			if err != nil {
				return fmt.Errorf("failed to upload split part %s: %w", partPath, err)
			}
			inputFiles = append(inputFiles, partInputFile)
			return nil
		}()
		if err != nil {
			return fmt.Errorf("failed to upload split part %s: %w", partPath, err)
		}
	}
	if len(inputFiles) == 1 {
		// shoud not happen as we already check fileSize > splitSize
		doc := message.UploadedDocument(inputFiles[0]).
			Filename(filepath.Base(matched[0])).
			ForceFile(true).
			MIME("application/zip")
		_, err = t.sendBuilder(ctx.Sender.
			WithUploader(upler).
			To(peer)).
			Media(ctx, doc)
		return err
	}

	multiMedia := make([]message.MultiMediaOption, 0, len(inputFiles))
	for i, inputFile := range inputFiles {
		doc := message.UploadedDocument(inputFile).
			Filename(filepath.Base(matched[i])).
			MIME("application/zip")
		multiMedia = append(multiMedia, doc)
	}

	sender := ctx.Sender

	if len(multiMedia) <= tglimit.MaxAlbumItems {
		_, err = t.sendBuilder(sender.WithUploader(upler).
			To(peer)).
			Album(ctx, multiMedia[0], multiMedia[1:]...)
		return err
	}

	for i := 0; i < len(multiMedia); i += tglimit.MaxAlbumItems {
		end := min(i+tglimit.MaxAlbumItems, len(multiMedia))
		batch := multiMedia[i:end]
		_, err = t.sendBuilder(sender.WithUploader(upler).
			To(peer)).
			Album(ctx, batch[0], batch[1:]...)
		if err != nil {
			return fmt.Errorf("failed to send album batch: %w", err)
		}
	}
	return nil

}
