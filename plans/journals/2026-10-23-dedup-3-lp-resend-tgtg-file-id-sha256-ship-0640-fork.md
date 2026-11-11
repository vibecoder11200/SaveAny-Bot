---
title: "Dedup 3 lớp: resend TG→TG + file-id + sha256 ship 0.64.0-fork"
date: 2026-10-23
summary: "Implement 3-layer content dedup per plan 2026-10-23, deployed to prod, migration verified; prod verify scenarios 2-6 pending user /save"
---

# Dedup 3 lớp: resend TG→TG + file-id + sha256 ship 0.64.0-fork

## What happened

Executed plans/2026-10-23-dedup-fileid-hash-resend end to end (cook --auto).

- Phase 1: `TGFile.FileUniqueID()` ("doc:<id>"/"photo:<id>") qua 1 internal constructor (RT-4);
  bảng `saved_file_dedups` (GORM pluralize — plan viết `saved_file_dedup`, khác tên nhưng
  mọi access qua GORM nên cosmetic) + 3 index scope/fileid/hash; fail-open; UserID<=0 no-op (RT-5);
  `fsutil.Sha256File` stream 1MB.
- Phase 2: file-id check lúc tạo task (tftask.go đơn+album, sau name-conflict) và runtime
  (batchtfile processElement return nil / downloadElement return errSkippedExists — RT-3;
  tfile runtime). Đặt check TRƯỚC refreshElemFile vì fid capture lúc build, refresh không
  đổi fid (fid là document ID ổn định) → tiết kiệm 1 RPC/element bị skip.
- Phase 3: `storagetypes.ExistingMediaSender` + `Telegram.SendExistingMedia` — build input
  media từ `file.Location()` (RT-1), send qua client đã fetch (RT-2, `tdler.SendCtxForFile`),
  đích PIN `config.ChatID` (RT-14a), fallback chỉ với nhóm lỗi biết-chắc-chưa-gửi (RT-7),
  lỗi ambiguous → fail element. Album KHÔNG resend (v1, giữ media group). Không log
  AccessHash/FileReference (RT-14b). RecordDedup lỗi → log ERROR (RT-14c).
- Phase 4: hash sau tải trước upload ở cả 8 task; album filter lockstep 1 pass trong
  `uploadBatchResults` (RT-9); `ErrSkippedLarge` sentinel (RT-8) + `SkipLargeLimit`
  capability để skip TRƯỚC tải (bonus: sửa bug name-ledger ghi path file chưa upload);
  4 task có stream branch (parsed/directlinks/transfer/telegraph) skip hash + debug log (RT-10).
  6 task non-TG thêm field UserID, wiring ở 6 handler; api factory giữ 0 (no-op).
- Phase 5: tracker ItemPhaseDeduped/ItemPhaseSkipped + DedupedCount; done message
  Succeeded/Deduped/Skipped/Failed tách dòng (RT-15), progress line Deduped chỉ hiện >0;
  i18n en+zh-Hans + geni18n.
- Phase 6: gate xanh (build/vet/test -race; 2 test storage/telegram fail là fixture
  Windows đã biết). RT-14d: plans/ + reports/ vào .gitignore TRƯỚC commit (fork public).
  5 commits push main. Deploy 0.64.0-fork theo runbook; migration verified:
  bảng `saved_file_dedups` + idx_dedup_scope/fileid/hash tồn tại, log sạch, userbot
  login OK, 6 storage load OK.

## Decision

- Runtime dedup check đặt trước refreshElemFile (khác pseudo-code plan đặt sau) — fid
  không đổi qua refresh, sớm hơn = tiết kiệm RPC, đúng tinh thần RT-12.
- Bảng tên `saved_file_dedups` (GORM plural) — chấp nhận, không rename.
- Commit gộp thành 4 commit (gitignore/foundation/resend+sentinel/tasks+i18n) thay vì
  6 theo phase vì nhiều file bị đụng bởi nhiều phase (tfile/execute.go, batchtfile).
- Đổi batch_done_with_skipped template in-place (thêm Deduped/Failed) — không giữ
  template cũ.

## Next steps (user phải tự chạy — cần tương tác Telegram)

1. Lớp 0: /save 1 tin video TG mới vào TG storage → log `resend ok`, không download,
   message vào chat ~1-2s. Riêng: tin từ kênh chỉ userbot xem được (RT-2) — nếu
   fallback liên tục, add userbot admin vào chat storage.
2. Lớp 1: chạy lại lệnh vừa chạy → log `dedup skip (file-id` ngay từ tạo task, done
   message Deduped: N.
3. Lớp 2: lưu 2 tin khác tên cùng nội dung → tin 2 log `dedup skip (hash`.
4. Không regression: file mới lạ + local storage vẫn lưu bình thường.
5. Re-run range 687-1000 @phimsexvietnama: ~17 failed cũ retry; đếm message mới thực
   tế trong TG Hehe (at-least-once semantics).

## Hạn chế đã biết (RT-14e)

- File lưu TRƯỚC dedup không có hash/fid trong ledger → 1 lần gặp lại để học.
- Resend không có hash; stream-mode bỏ hash; UserID=0 (API) không dedup.
- Ledger không tự dọn khi user xóa message khỏi chat storage — re-run sẽ bị chặn;
  cần purge command (follow-up). User xóa message thì phải báo tôi purge rows.

> Historical work record — not durable authority. Prefer docs/specs/ADRs for current decisions.

## 2026-10-23 bổ sung: review + simplify + redeploy 0.64.1-fork

code-reviewer (hostile, 1 High / 3 Medium / 3 Low) + code-simplifier chạy sau ship 0.64.0.

Findings đã sửa (commit 96cabb4):
- **High**: tfile SkipLarge fall-through ghi dedup ledger cho file chưa từng upload
  → sentinel convert trong retry closure + return trước record.
- Medium: tryResend ambiguous không mark terminal → element "bốc hơi" khỏi summary
  (IgnoreErrors nuốt error) → markItemFailed + notify.
- Medium: stream branch parsed/directlinks retry sentinel N lần → convert như sibling.
- Medium (design): Overwrite strategy bị dedup chặn → task.DisableDedup set khi
  overwrite (bypass check, vẫn record; hash vẫn capture để record).
- Low: SendExistingMedia từ chối path redirect chat khác (preflight fallback);
  double OnDone hash-skip; sentinel retried N lần ở 2 retry closure; markItemFailed
  guard Skipped; logger dùng request logger; all-deduped message hiện cả 2 list.
- Issue "resend trước SkipLarge check" = đúng quyết định có sẵn trong plan phase 3
  ("SkipLarge KHÔNG áp cho resend") — không đổi.

code-simplifier (commit 2b59007): DedupSkipByHash/RecordDedupAfterUpload gom 6 block
hash-gate/record; dlutil.DownloadFailure gom 4 block post-mortem; processStreamElem
tách khỏi processElement; saveBatchItems gom 2 đuôi success/failure; bỏ dead code.
Simplifier còn bắt được bug test của tôi: TestTryResendOutcomes fail thật (2 fake
storage trùng tên "FakeBatch" → assertion bleed) — lỗi commit trước do output tail
che mất; đã fix tên riêng cho từng sub-case.

Gate: build/vet/test -race toàn bộ xanh (trừ 3 fixture Windows đã biết). Deploy
0.64.1-fork: log sạch, 0 error/fatal/panic, userbot + 6 storage OK.

Kịch bản verify prod 2-6 (resend/file-id/hash/regression/re-run 687-1000) vẫn chờ
user chạy /save.
