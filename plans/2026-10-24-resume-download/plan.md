# Resume download byte-level (TG sources) — range-map đầy đủ

## Quyết định user (2026-10-24)
- Chỉ nguồn Telegram (tfile + batchtfile); stream-mode/photo (size 0) không resume.
- Range-map đầy đủ theo block partSize (bitmap), port tinh thần PR #237 upstream.
- Resend at-least-once: chấp nhận trùng (document). Purge: cả 2 ledger (đã ship /purge).

## Kiến trúc

### Ràng buộc phát hiện khi research
1. Boot clean `RemoveAllInDir(Temp.BasePath)` xóa SẠCH subdir → resume state phải nằm NGOÀI cache dir.
2. Task queue đa luồng (semaphore=Workers) → 2 task cùng (file, storage, path) có thể đồng thời ghi 1 `.part` → cần per-key mutex trong process.
3. Cache path hiện `<xid>_<name>` không stable qua restart → resume state key bằng identity riêng, KHÔNG đổi localPath (tránh đụng cleanup/ext-detect/hash-dedup).

### Thiết kế
- **Resume dir**: `<Temp.BasePath>-resume/` (sibling, không bị boot clean). Chứa `<stem>.part` + `<stem>.bitmap`.
- **Stem** = hex(sha256(fid|chat:msg fallback + "|" + storageName + "|" + destPath))[:20] — stable qua recovery rebuild (journal có đủ 3 thành phần), unique theo đích.
- **DownloadToCache(ctx, logger, file, localPath, idKey, wrap)**: complete-reuse localPath → plain path (size<=0) → resumable: lock stem → DownloadResumable vào partFile qua wrap(WriterAt) → stat check → rename part→localPath → remove bitmap.
- **DownloadResumable**: bitmap JSON atomic save (tmp+rename) mỗi block xong; errgroup SetLimit(threads); fetchChunk = UploadGetFile offset/limit + FloodWait sleep + timeout retry + **FRE refresh (thêm so với PR #237, tái dùng RefreshFileLocation, tối đa 2/download)** + meter observe qua eofAwareClient; CDN redirect → fallback DownloadParallelWithRefresh toàn file (reset bitmap).
- Bitmap incompatible (size/partSize đổi) → truncate part + full. Part missing/rỗng → reset bitmap.
- **Call sites**: downloadElement, processElement non-stream (batchtfile), tfile Execute non-stream thay CreateFile+DownloadParallelWithRefresh bằng DownloadToCache; giữ wdog-kick + progress qua wrap; giữ phân loại lỗi stall/crawl. defer cleanup localPath giữ nguyên; .part/.bitmap cố ý KHÔNG xóa khi lỗi.
- Progress hiển thị: resumed run chỉ đếm byte thiếu (chấp nhận, note trong journal).

### Không đụng
- Stream branches (batchtfile processElement stream, tfile stream.go): không cache → không resume, comment ghi rõ.
- FRE refresh/watchdog/meter machinery hiện có của DownloadParallelWithRefresh.
- 6 task non-TG. aria2 tự resume sẵn.

## Test
- Port serverLikeClient fake + TestDownloadResumableFull/FailAfter/Resume từ PR #237; thêm: FRE refresh path, stem stable, DownloadToCache complete-reuse + rename finalize, boot-clean survivor (resume dir ngoài cache dir — assert bằng path layout).
- Gate: build/vet/test -race; deploy 0.66.0-fork.

## Verify prod (user)
1. /save file lớn (1-2GB) → cancel giữa chừng (hoặc kill container) → chạy lại → log hiện resume (số block skip) → file hoàn tất, tổng thời gian giảm tương ứng phần đã tải.
2. File mới lạ: tải full bình thường (không regression).
3. Stream mode vẫn chạy.
