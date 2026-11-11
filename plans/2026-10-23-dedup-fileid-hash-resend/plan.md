---
title: "Dedup 3 lớp cho SaveAny-Bot: resend TG→TG + file-id + content hash"
description: "Chặn lưu trùng nội dung trên mọi tính năng lưu: lớp 0 resend-by-id, lớp 1 file-id trước tải, lớp 2 sha256 trước upload; counter Deduped riêng"
status: pending
priority: P1
effort: 14h
tags: [dedup, telegram, storage, database]
blockedBy: []
blocks: []
created: 2026-10-23
---

# Dedup 3 lớp cho SaveAny-Bot

## Overview

Hiện mọi skip đều theo **đường dẫn file** (`telegram_saved_files.storage_path`), không có hash và không có định danh nội dung. Hệ quả đã chứng minh trên prod: skip-nhầm khi channel có tên file trùng (`1.mp4`, `22.mp4` — vụ 67 skipped @phimsexvietnama), lưu trùng khi cùng nội dung khác tên, và tốn gấp đôi bandwidth (tải về rồi upload lại) cho lưu TG→TG.

Plan này thêm **3 lớp dedup theo nội dung**, áp cho mọi task lưu (batchtfile, tfile, parsed, directlinks, ytdlp, aria2dl, transfer, telegraph):

- **Lớp 0 — resend-by-id (TG→TG)**: thay tải+upload bằng `messages.sendMedia` + `inputMediaDocument`/`inputMediaPhoto` lấy từ message nguồn vừa refresh. Bandwidth gần bằng 0. Fallback tải+upload khi noforwards/lỗi.
- **Lớp 1 — file-id trước tải**: `tg.Document.ID` / `tg.Photo.ID` (stable, forward giữ nguyên — tương đương Bot API `file_unique_id`) tra bảng dedup theo `(user_id, storage_name)` trước khi tạo cache file → chặn cả download lẫn upload.
- **Lớp 2 — sha256 trước upload**: hash file cache (stdlib, ~1-2s/GB) → tra bảng dedup → trùng thì bỏ upload.

Quyết định đã chốt với user (brainstorm 2026-10-23): dedup **per-storage** (Hehe ≠ Zyro, lưu sang storage khác là chủ đích sao chép), **có làm lớp 0**, **counter Deduped tách riêng** trong message hoàn thành, **không backfill** file cũ.

Nghiên cứu nền: [research report](../reports/research-261023-0834-dedup-file-id-content-hash.md).

## Phases

| Phase | Name | Status |
|-------|------|--------|
| 1 | [Nền tảng: FileUniqueID + bảng dedup + hàm tra/ghi](./phase-01-foundation.md) | Pending |
| 2 | [Lớp 1: file-id check trước tải](./phase-02-fileid-layer.md) | Pending |
| 3 | [Lớp 0: resend-by-id TG→TG](./phase-03-resend.md) | Pending |
| 4 | [Lớp 2: sha256 check trước upload](./phase-04-hash-layer.md) | Pending |
| 5 | [UX: counter Deduped/Failed + i18n + log](./phase-05-ux.md) | Pending |
| 6 | [Test tổng hợp, race, deploy, verify trên prod](./phase-06-ship.md) | Pending |

## Dependencies

- gotd v0.149.0 đã có sẵn mọi type cần (`tg.InputMediaDocument`, `tg.InputMediaPhoto`, `MessagesSendMediaRequest`)
- Bảng `telegram_saved_files` (d9a304d) giữ nguyên — conflict theo tên vẫn hoạt động độc lập
- `sha256` stdlib — không thêm dependency
- Không đụng: FRE refresh, stall watchdog, recovery, edit-flood cooldown

## Cross-Plan Dependencies

| Relationship | Plan | Status |
|-------------|------|--------|
| Dựa trên | telegram_saved_files ledger (đã ship) | completed |

## Red Team Review

_Review 2026-10-23 — 4 reviewer hostile (Assumptions / Failure / Scope / Security), 15 findings sau khử trùng lặp, adjudicated. User không trả lời kịp vòng hỏi — áp phương án recommended (14 Accept, trong đó 2 modified; 1 Reject có lý do), ghi rõ để override sau._

| # | Sev | Finding | Disposition |
|---|-----|---------|-------------|
| 1 | Critical | Resend đọc FileReference thối từ msg.GetMedia() — refresh chỉ update location | **Accept**: build InputMedia từ `file.Location()` (ref tươi) |
| 2 | Critical | Resend lệch account (userbot fetch, bot gửi → bị từ chối) | **Accept**: gửi qua client đã fetch (`Dler()==uctx.Raw`), interface nhận sendCtx |
| 3 | Critical | Dedup `return nil` trong downloadElement giết cả album batch | **Accept**: trả sentinel `errSkippedExists` có sẵn + test |
| 4 | High | FromMediaMessage bỏ field fileUniqueID → lớp 1 chết im lặng | **Accept**: funnel 1 internal constructor + test qua cả 2 |
| 5 | High | user_id=0 (API/non-TG) chia namespace dedup chung | **Accept**: UserID<=0 → no-op check+record |
| 6 | High | Resend path thiếu ext-detect → lệch name-ledger | **Accept**: path informational, dedup dựa fid/hash |
| 7 | High | Fallback catch-all → lỗi mập mờ vẫn upload = trùng chắc chắn | **Accept**: chỉ fallback với tgerr codes biết; transport/lạ → fail element |
| 8 | High | SkipLarge trả nil → ghi hash cho file chưa lưu = mất dữ liệu | **Accept**: sentinel ErrSkippedLarge (sửa luôn bug name-ledger cũ) |
| 9 | High | Album filter items/successElems lệch index → progress sai item | **Accept**: lọc lockstep 1 pass + assert + test |
| 10 | High | 4/6 task non-TG stream không hash được | **Accept (modified)**: giữ đủ 8 task theo contract, hash chỉ khi có file local; stream branch skip + debug log |
| 11 | High | pkg/dedup + hashutil là package thừa | **Accept**: Sha256File → fsutil; check/record → saved_file_dedup.go |
| 12 | Medium | Bỏ creation-time check (runtime đủ) | **Reject**: runtime đi qua refreshElemFile = 1 RPC/element; re-run 295 file qua runtime tốn 295 RPC thỡ — creation-time (DB-only) chặn sạch trước |
| 13 | Medium | Wiring tạm phase 2 rồi làm lại phase 5 | **Accept**: tracker API thật ngay phase 2 |
| 14 | Medium | Resend pin ChatID (vá redirect filename); log RecordDedup lỗi to; plans/ vào .gitignore trước khi push fork public; dedup không tự dọn khi user xóa message | **Accept**: cả 4 vào phase 3/6 + hạn chế ghi journal |
| 15 | Low | Tách Failed khỏi Skipped = ngoài contract | **Accept (opportunistic)**: gần miễn phí, sửa đúng gốc nhầm lẫn "67 skipped" |

### Whole-Plan Consistency Sweep

Đã rà sau khi áp: bỏ hết tham chiếu `pkg/dedup`/`hashutil` (phase 4/6), bỏ wiring tạm "phase 5" (phase 2/5), phase 5 thu hẹp còn i18n+render, phase 3 architecture thay bằng buildInputMediaFromLocation + sendCtx + isResendFallbackErr, phase 6 test list + .gitignore + at-least-once + hạn chế eviction. Không còn mâu thuẫn chưa giải.

### Verified-clean (reviewer xác nhận, không cần đổi)

Document/photo ID ổn định để dedup; storage names global-unique (không cross-user leak); `MaxOpenConns(1)` serialize check-then-insert; fail-open DB; ExistingMediaSender đúng pattern capability-interface có sẵn; không log AccessHash (giữ nguyên — chỉ log scalar).
