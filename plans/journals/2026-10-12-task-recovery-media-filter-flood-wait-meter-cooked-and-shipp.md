---
title: Task recovery + media filter + flood-wait meter cooked and shipped
date: 2026-10-12
summary: "Implemented plans/2026-10-12-task-recovery-media-filter end to end: 7 fork commits, deployed to prod, upstream PRs pending user approval"
---

# Task recovery + media filter + flood-wait meter cooked and shipped

## What happened
Cooked plans/2026-10-12-task-recovery-media-filter (6 phases, --auto):
- feat(media) 62d3049: MediaType classification + fail-closed MediaFilter parser (mediautil), User.MediaFilter + WatchChat.MediaType + PendingTask models.
- feat(bot) a1662eb: /save batch t:<types> syntax (2-arg grammar, regex moves to 4th arg).
- feat(bot) 535aaec: watch mediatype: filter (own column, checked before msgre), /config flip-on-tap media filter screen, link-batch default filter.
- feat(core) 1b902ad: FloodWaitMeter (>=2min/RPC successful calls, 7.5min per 15min window -> cancel with distinct crawl error) beside the stall watchdog; wired into all 3 download paths via NewDownloaderWithMeter.
- feat(core) 6518565: PendingTask sqlite journal; single choke point core.AddTask (Record before enqueue, compensate delete on enqueue error); cancel matrix (user cancel deletes row, shutdown keeps it); SanitizeFileName at every TG-file name assignment; sqlite WAL+busy_timeout via DSN + SetMaxOpenConns(1).
- feat(core) 28b2512: startup recovery (48h age drop, GetMessagesByIDs chunked fetch, ViaUserbot client routing, row-ID reuse => idempotent, SkipIfExists overrides conflict strategy, per-user summary).
- docs d03c668: en+zh usage/config docs + [task] in example config.

Deployed per runbook: image saveany-bot:0.63.0-fork (commit d03c668) built, loaded on 160.30.113.48, compose recreated. Verified: version stamp, WAL=wal, pending_tasks table exists (0 rows), bot+userbot up.

## Decision
- Per-user filter helper is mediautil.UserMediaFilter (not a User method): database importing mediautil would be an import cycle.
- gotd has no DocumentAttributeVoice type: voice = DocumentAttributeAudio with Voice flag.
- Commit order deviates from plan (meter before journal) so every commit builds standalone; watch.go UserID lines and SkipIfExists hunks were strip/restore staged at commit boundaries.
- Upstream PRs (PR A filter-inline, PR B recovery) NOT opened: plan gates them on user approval.

## Next steps
- User: open PR A (filter inline) / PR B (recovery) upstream when ready; 24h prod watch for crawl error "download crawling: flood-wait absorbed" and journal table growth.
- Runbook acceptance scenarios (kill mid-batch, /cancel no-resurrect, t: bogus token) still to be exercised from Telegram.

> Historical work record — not durable authority. Prefer docs/specs/ADRs for current decisions.

## 2026-10-13 — Hotfix: mid-task file_reference refresh (gap đã khép)

Điều tra prod 2 lần chạy `/save -1002688384341` (563-1000 và 573-1000) cho thấy ref Telegram sống **~2h sau khi fetch** (đo 2 lần độc lập: fetch 09:04 → chết 11:03; fetch 19:24 → chết 21:21-21:24). Batch 405 file × ~5 phút/file ÷ 2 slot ≈ 17h >> 2h → mỗi lần chạy chỉ ~10-25 file đầu qua cửa, phần còn lại fail chùm `FILE_REFERENCE_EXPIRED` trong ~30-50 giây cuối (hiện trong UI là "Skipped").

**Fix (commit `0078e81`, deployed prod 2026-10-13T05:41Z):**
- `tdler.RefreshFileLocation`: re-fetch message gốc qua đúng client đã build file (userbot file → userbot) + swap location tươi in-place (`tfile.RefreshLocation`, giữ name/path/message/dler). Sentinel `ErrSourceGone` cho nguồn đã xóa.
- Batch element: refresh ref **ngay trước khi tải** (prelude — element chờ hàng giờ); source gone → fail element với lý do thật; lỗi hạ tầng khác → fallback location cũ.
- `tdler.DownloadParallelWithRefresh`: gặp FRE **giữa chừng** → refresh + retry từ đầu 1 lần (che file khổng lồ tải >2h).
- Single non-stream: FRE retry; stream (batch + single): prelude (pipe không retry được giữa dòng).
- Kèm: `MESSAGE_NOT_MODIFIED` progress edit → idempotent success (không còn ERRO nhiễu).

**Chưa làm (chấp nhận):** stream mode không retry giữa dòng (pipe đã ăn byte); retry tải lại từ offset 0 (double-count progress bytes — cosmetic); prelude là 1 RPC/element (~50ms, không đáng kể).

**Verify prod:** chờ user re-run `/save -1002688384341 573-1000 (?s)^.{0,200}$` — kỳ vọng: 0 (hoặc gần 0) FILE_REFERENCE_EXPIRED, task chạy qua mốc 2h không chết chùm; log sẽ hiện "File reference expired mid-download, refreshing and retrying once" nếu có retry.

## 2026-10-14 (tối) — Telegram storage saved-files index (commit `d9a304d`, deployed 15:25 UTC)

**Triệu chứng gốc**: user chạy lại /save đến storage telegram ("TG Hehe") không bao giờ thấy conflict prompt, mỗi lần chạy là upload trùng toàn bộ. Nguyên nhân: `Telegram.Exists()` luôn trả false cố ý (telegram không có API hỏi file tồn tại trong chat) → conflict-check, skip khi chạy lại, và SkipIfExists của recovery đều mù với backend này. (Phần lưu alist hoạt động đúng — recovery 14:44 skip tới lin_siyee là bằng chứng.)

**Fix**: bảng `telegram_saved_files` (storage_name + storage_path, unique index) ghi sau MỌI đường upload thành công (media thường, split-zip, lossless parts, album) — Exists tra ledger này. Record best-effort (lỗi index không fail upload thành công); lookup fail-open (false → cùng lắm re-upload, không bao giờ chặn save). KHÔNG record file bị SkipLarge bỏ qua.

**Giới hạn đã ghi cho user**: file lưu TRƯỚC build này không có trong ledger (user chọn không scan chat cũ) → một lần chạy cuối có thể trùng phần cũ, sau đó index là chuẩn. File do người tự bỏ vào chat TG cũng không được phát hiện.

**Verify**: test `TestTelegramSavedIndex` xanh (record/exists/storage-khác/path-khác/idempotent/batch); 2 fail `storage/telegram` là fixture `tests/testvideo` thiếu sẵn có trên Windows (pre-existing). Prod: bảng tạo xong lúc boot, bot + userbot khởi động sạch.

## 2026-10-15 — Stall watchdog bị bỏ sót ở processElement (8eaa47c)

**Triệu chứng prod:** batch 687-1000 treo chết — 2 worker đứng 0 byte >1h (từ 01:39/01:47Z), không một dòng log, 2 TCP conn TG idle, CPU 0.3%. Trước đó 22:12:50Z đã có 1 element fail `engine was closed` (gotd engine chết giữa chừng, 1 element nhận lỗi, phần còn lại đen thui treo vĩnh viễn).

**Nguyên nhân:** commit 2ee5b39 nối StallWatchdog vào downloadElement (album), stream branch và tfile — nhưng bỏ sót **processElement nhánh parallel thường** (đường mọi file đơn lẻ đi qua). Không watchdog = treo vô hạn. Gần như chắc chắn cũng là thủ humane vụ run2 "10 tiếng không tiến độ" trước đây.

**Fix (8eaa47c):** copy nguyên wiring (NewStallWatchdog + Kick trong ProgressWriterAt + Stalled() check) vào nhánh thiếu.

**Bằng chứng sau deploy (8eaa47c, 02:57Z):**
- Recovery dựng lại task 295 file, ledger skip đúng 28+3 file đã lưu (31 rows khớp ✅31)
- Download chảy lại 5.4 MB/s trên connection mới
- File 04.29_59.35.44 stall 0 byte lần nữa → watchdog cắt đúng 15m0s (03:12:51), task sang file kế tiếp sau 3s
- Hai file từng treo (04.29_59.35.44, 04.04_43.31.15) stall 0 byte cả trên connection mới — nghi route DC riêng bị hỏng; để re-run sau picking up. Có thể liên quan run /save đầu tiên treo cùng 2 file này.

**Học được:** khi thêm defensive wiring cho mọi path, grep call-site của từng nhánh thực tế (log line-number so với source) thay vì tin vào cấu trúc đối xứng.

## 2026-10-23 — Plan dedup 3 lớp (research → brainstorm → plan :hard)

**Bối cảnh:** vụ 67 skipped @phimsexvietnama phơi ra cơ chế skip mù theo tên (không hash, không file-id). User yêu cầu dedup nội dung cho TẤT CẢ tính năng lưu, quy trình ak:research → ak:brainstorm → ak:plan :hard.

**Nghiên cứu chính:** `file_unique_id` Bot API ≡ `tg.Document.ID`/`tg.Photo.ID` MTProto (stable, forward giữ nguyên, biết trước khi tải); sha256 stdlib đủ nhanh (~1-2GB/s, bottleneck là tải); MTProto cho resend không re-upload (`messages.sendMedia` + inputMediaDocument) → lớp 0 tiết kiệm ~100% bandwidth TG→TG.

**Contract chốt với user:** per-storage (Hehe ≠ Zyro), làm cả lớp 0 resend, counter Deduped riêng, không backfill.

**Plan:** `plans/2026-10-23-dedup-fileid-hash-resend/` — 6 phases (foundation fileID+table → layer1 pre-download → layer0 resend → layer2 hash → UX → ship). 

**Red-team 4 reviewer, 15 findings:** 3 Critical đều ở phase resend — (1) FileReference thối: refresh chỉ update `f.location`, KHÔNG phải msg.GetMedia() — phải build InputMedia từ location; (2) account mismatch: access-hash gắn theo account fetch — gửi qua `Dler()==uctx.Raw` rule; (3) dedup return nil trong downloadElement giết album batch — phải trả errSkippedExists. Cùng SkipLarge-nil bug có sẵn, FromMediaMessage field-drop trap, user_id=0 namespace, fallback chọn lọc (lỗi mập mờ → fail element, không upload trùng). 14 apply + 1 reject (giữ creation-time check — tránh 295 RPC refresh thừa cho re-run).

**Học được:** (a) "đối xứng" giữa các constructor/function là giả định — grep call site thật; (b) fallback catch-all với hệ phân tán = nhân bản dữ liệu khi lỗi mập mờ; (c) refresh location ≠ refresh media — 2 nguồn sự thật phải tách rạch.
