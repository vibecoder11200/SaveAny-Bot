# Phase 3 — Lớp 0: resend-by-id TG→TG (không tải, không upload)

## Context Links

- [MTProto docs — resend bằng inputMediaDocument/Photo](https://core.telegram.org/api/files): "Non-protected media may be resent to other chats, without reuploading"
- `storage/telegram/telegram.go` — `t.target()` (:247 tách filename+chatID từ storagePath), `tryGetInputPeer`, `t.limiter`, `recordSaved`, `tctx.Raw`
- `core/tasks/batchtfile/execute.go` — điểm sau `refreshElemFile` (file_reference vừa được làm mới — chính là thứ `InputMediaDocument` cần)
- Phase 2 — resend chỉ chạy cho file KHÔNG bị dedup chặn

## Overview

- **Priority**: P1 — thay đổi lớn nhất về hiệu năng (mirror kênh 30GB: bandwidth → ~KB)
- **Status**: Pending
- Khi lưu file TG vào storage Telegram: gửi lại media gốc bằng `messages.sendMedia` thay vì tải về + upload. Fallback toàn vẹn: mọi lỗi → đường tải+upload hiện tại.

## Key Insights

- **[RT-1 — CRITICAL] KHÔNG đọc media từ `msg.GetMedia()`** — message giữ FileReference từ lúc tạo task (hàng giờ tuổi trong batch dài) vì `RefreshLocation` chỉ update `f.location`. Build `InputMediaDocument`/`InputMediaPhoto` từ **`file.Location()`** (`*tg.InputDocumentFileLocation`/`*tg.InputPhotoFileLocation` đã chứa ID + AccessHash + FileReference tươi ngay sau refresh). Message chỉ dùng cho gate `GetNoForwards()`.
- **[RT-2 — CRITICAL] Gửi qua ĐÚNG client đã fetch file**: access-hash + file-reference gắn theo account. Rule có sẵn ở `refresh.go:38-41` (`file.Dler() == uctx.Raw → dùng userbot`): resend truyền `*ext.Context` của client đó vào storage như tham số. Client đó phải với được chat đích (peer resolve fail → fallback). Phase 6 verify riêng: userbot phải có quyền gửi vào chat storage (nếu bot là admin mà userbot không phải → resend luôn fallback; cân nhắc add userbot admin).
- Điều kiện nguồn: message có cờ `NoForwards` → không được resend → fallback. Chat-level protected trả lỗi khi gửi → phân loại lỗi (RT-7).
- Resend bỏ qua hoàn toàn: cache file, thumbnail ffmpeg, phân loại media của `prepareMedia`, giới hạn 2GB/4GB (không upload!), split. Caption giữ logic hiện tại (filename hoặc SourceCaption override từ ctx).
- **[RT-6] Không detect extension được cho path resend** (không có bytes) — chấp nhận lệch: dedup dựa fid/hash (fid vẫn chặn lần sau), `StoragePath` trong ledger dedup chỉ informational.
- **[RT-14a] Đích resend PIN về `t.config.ChatID`** — KHÔNG dùng chat-override từ path segment đầu (vá lỗ hổng redirect filename `../<chatid>/x.mp4` trong đường gửi mới; đường upload cũ giữ nguyên behavior, ghi follow-up sanitize `file.Name()`).
- File resend KHÔNG có content hash (không tải) — ledger ghi `FileUniqueID` + hash rỗng. **[RT-14b] KHÔNG BAO GIỜ log struct `InputMediaDocument`/request** (chứa AccessHash/FileReference) — chỉ log scalar.
- **[RT-7] Fallback chọn lọc, KHÔNG catch-all**: chỉ fallback khi (a) fail trước RPC (peer==nil, noforwards, media không build được), (b) tgerr code biết: `FILE_REFERENCE_EXPIRED`, `MEDIA_EMPTY`, `CHAT_FORWARDS_RESTRICTED`, lỗi peer/input. Lỗi transport/lạ → **fail element** (retry/journal machinery lo) — vì "đã gửi mà mất response" rồi upload lại = trùng chắc chắn. **[RT-14c] `RecordDedup` lỗi phải log ERROR** (fail-open cho save, to cho operator); gửi thành công nhưng crash trước ledger → recovery gửi lại trùng (at-least-once) — ghi rõ phase 6.

## Requirements

- Interface task→storage không được phá `storage.Storage` — định nghĩa interface nhỏ riêng, type-assert ở task.
- Fallback phải là hành vi mặc định an toàn: resend lỗi → log INFO kèm lý do → chạy đường cũ, task KHÔNG fail.
- Tôn trọng rate limiter của storage (`t.limiter.Wait`).
- SkipLarge config KHÔNG áp cho resend (không có upload → không có giới hạn).

## Architecture

```go
// pkg/storagetypes (mới, file resend.go)
type ExistingMediaSender interface {
    // sendCtx là client ĐÃ FETCH file (RT-2); media build từ file.Location() (RT-1).
    SendExistingMedia(ctx context.Context, sendCtx *ext.Context, media tg.InputMediaClass, storagePath, caption string) error
}

// storage/telegram — implement
func (t *Telegram) SendExistingMedia(ctx context.Context, sendCtx *ext.Context, media tg.InputMediaClass, storagePath, caption string) error {
    filename := path.Base(path.Clean(storagePath))          // RT-14a: đích luôn là config chat
    _ = filename
    peer := tryGetInputPeer(sendCtx, t.config.ChatID)       // RT-14a: pin, không parse path
    if peer == nil || peer.Zero() { return fmt.Errorf("no input peer for chat %d", t.config.ChatID) }
    if err := t.limiter.Wait(ctx); err != nil { return err }
    _, err := sendCtx.Raw.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
        Peer: peer, Media: media, Message: caption,
    })
    if err == nil { t.recordSaved(ctx, path.Clean(storagePath)) }
    return err
}
```

Task side — trong `processElement` (và `tfile/execute.go`), sau dedup check, TRƯỚC `CreateFile`:

```go
if sender, ok := any(elem.Storage).(storagetypes.ExistingMediaSender); ok {
    if media, ok := buildInputMediaFromLocation(elem.File); ok { // từ file.Location(); msg chỉ để gate NoForwards (RT-1)
        err := sender.SendExistingMedia(ctx, sendCtxFor(elem.File), media, elem.Path, captionFor(elem))
        if err == nil {
            if rerr := database.RecordDedup(ctx, t.UserID, elem.Storage.Name(), elem.File.FileUniqueID(), "", size, elem.Path); rerr != nil {
                logger.Errorf("dedup record failed after resend (at-least-once risk): %v", rerr) // RT-14c
            }
            t.markItemCompleted(elem.ID); t.notifyStateChange(ctx)
            logger.Info("resend ok (no download/upload)")
            return nil
        }
        if isResendFallbackErr(err) { // RT-7: chỉ nhóm lỗi biết chắc "chưa gửi"
            logger.Infof("resend failed (fallback): %v", err)
        } else {
            return fmt.Errorf("resend failed (ambiguous): %w", err) // fail element → retry machinery
        }
    }
}
// → rơi xuống đường tải+upload hiện tại
```

`sendCtxFor(file)`: `uctx := userclient.GetCtx(); if uctx != nil && file.Dler() == uctx.Raw { return uctx }; return tgutil.ExtFromContext(ctx)` (mirror refresh.go:38-41).
`buildInputMediaFromLocation(TGFile)`: type-switch `file.Location()` → `*tg.InputDocumentFileLocation` → `tg.InputMediaDocument{ID, AccessHash, FileReference}`; `*tg.InputPhotoFileLocation` → `tg.InputMediaPhoto{...}`; KHÔNG dùng message media. Gate `file.Message().GetNoForwards()` trước.
`isResendFallbackErr(err)`: `errIs(ctx, peer==nil...)` + `tgerr.Is(err, "FILE_REFERENCE_EXPIRED"|"MEDIA_EMPTY"|"CHAT_FORWARDS_RESTRICTED")` + peer/input error classes; còn lại → ambiguous.

## Related Code Files

- Tạo: `pkg/storagetypes/resend.go`, `common/utils/tgutil/inputmedia.go` (buildInputMedia + test)
- Sửa: `storage/telegram/telegram.go` (SendExistingMedia), `core/tasks/batchtfile/execute.go`, `core/tasks/tfile/execute.go`

## Implementation Steps

1. Interface `ExistingMediaSender` trong storagetypes (nhận `sendCtx *ext.Context` — RT-2).
2. `buildInputMediaFromLocation` + `isResendFallbackErr` + unit test (location doc/photo/unsupported/noforwards; phân loại lỗi).
3. `SendExistingMedia` trên Telegram storage (pin `t.config.ChatID` — RT-14a).
4. Hook vào `processElement` + `tfile` với fallback chọn lọc (RT-7); RecordDedup(fid, "") + log ERROR khi record lỗi (RT-14c).
5. Album (`downloadElement`/`processBatch`): **v1 không resend** — giữ upload thường (ghi rõ trong code comment).

## Todo List

- [ ] Interface + buildInputMediaFromLocation + isResendFallbackErr + test
- [ ] SendExistingMedia (pin ChatID) trên telegram storage
- [ ] Hook 2 task + fallback chọn lọc + RecordDedup có check lỗi
- [ ] Log "resend ok" / "resend failed (fallback)" / "resend failed (ambiguous)"

## Success Criteria

- Save 1 file video TG→TG mới: log `resend ok`, KHÔNG có "Starting file download", không có file cache, message xuất hiện trong chat storage trong ~1-2s.
- **File nguồn fetch qua userbot (kênh bot không tham gia) vẫn resend qua userbot được (RT-2) — verify bằng kênh thực tế.**
- File nguồn noforwards: skip resend → tải+upload bình thường, task thành công.
- Lỗi ambiguous (giả lập bằng cách cắt mạng giữa send): element FAIL (không upload trùng), retry machinery xử lý.

## Risk Assessment

- **Risk**: FileReference hết hạn giữa refresh và send (cửa sổ mili-giây — không thực tế; nếu lỗi thì fallback bắt).
- **Risk**: caption khác đường upload (filename vs caption nguồn) — dùng đúng logic `mediaCaption`/SourceCaption override hiện có; sai lệch nhỏ chấp nhận được, ghi trong log.
- **Risk**: MEDIA_EMPTY / CHAT_FORWARDS_RESTRICTED từ server với media lạ — catch-all fallback xử lý tất cả.
- **Risk**: người dùng muốn NHẤT ĐỊNH có bản upload mới (thumbnail riêng...) — không có tín hiệu yêu cầu; nếu cần sau này thêm config per-storage `disable_resend`.

## Security Considerations

- Resend gửi media đến chat storage bằng peer đã có — không mở quyền mới; dùng đúng userbot ctx của storage hiện tại.

## Next Steps

- Phase 4 gắn hash layer vào đường fallback (sau khi đã tải).
