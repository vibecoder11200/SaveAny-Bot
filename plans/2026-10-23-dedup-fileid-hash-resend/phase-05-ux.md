# Phase 5 — UX: counter Deduped/Failed tách riêng + i18n + log chuẩn

## Context Links

- Phase 2/3/4 — các điểm đã gọi `markItemDeduped` (tạm dùng markItemCompleted + list)
- `core/tasks/batchtfile/progress.go` — tracker hiện tại; done message lumps failed vào Skipped (:~195)
- `common/i18n/locale/en.yaml:397-399` — `batch_done`, `batch_done_with_skipped`, `batch_canceled`
- i18n key ban đầu phải tránh tên `id` (go-i18n reserved) — bài học eb383c5

## Overview

- **Priority**: P2
- **Status**: Pending
- **[RT-13] Tracker API (MarkDeduped/DedupedCount) đã làm Ở PHASE 2** — phase này chỉ còn i18n + render + tách Failed. **[RT-15 — dán nhãn opportunistic]** Tách Failed khỏi Skipped ngoài contract gốc nhưng sửa đúng nguồn nhầm lẫn "67 skipped" và gần miễn phí (buildBatchDoneMarkup phải mở lại anyway).

## Key Insights

- `buildBatchDoneMarkup` (progress.go:196) compute `totalSkipped := len(skipped) + failed` — phải mở lại để thêm Deduped; itemCounts đã trả `failed` sẵn — tách chỉ là 1 biến template + 2 dòng i18n.
- i18n: thêm key `deduped` vào section msg của batch; đổi template `batch_done_with_skipped` thêm 2 dòng (Deduped/Failed). Đổi cả 2 locale.
- Đặt key dạng `batch.deduped_*` — KHÔNG dùng key tên `id` (go-i18n reserved, bài học eb383c5).

## Requirements

- Progress message: `Status: ✅ N | ⏭️ Deduped: M | 📥 K | ⏳ W` (chỉ thêm khi M > 0 để không nhiễu task thường).
- Done message: `Succeeded / Deduped / Skipped / Failed / Total size` — mỗi dòng 1 con số, Failed tách khỏi Skipped.
- Log 3 dạng chuẩn grep được: `dedup skip (file-id`, `dedup skip (hash`, `resend ok`.

## Architecture

- Data đã có sẵn từ tracker (phase 2): `DedupedCount()`, failed từ `itemCounts()`. Phase này chỉ đổi render + template.
- Item deduped hiển thị nhóm riêng (không đếm vào ✅).
- Done message: `Succeeded / Deduped / Skipped / Failed / Total size`.

## Related Code Files

- Sửa: `core/tasks/batchtfile/progress.go`, `core/tasks/batchtfile/task.go` (tracker constructor), `common/i18n/locale/en.yaml`, `common/i18n/locale/zh-Hans.yaml`, regen `common/i18n/i18nk/keys.go`
- Đụng nhẹ: các call site markItemDeduped (phase 2/3/4 đặt tạm) — đổi sang API thật

## Implementation Steps

1. Render progress + done message với Deduped/Failed (data đã có từ tracker phase 2); ẩn Deduped khi = 0.
2. i18n en + zh-Hans + `go run ./cmd/geni18n`.
3. Log 3 dạng chuẩn rà lại nhất quán (`dedup skip (file-id`, `dedup skip (hash`, `resend ok`).

## Todo List

- [ ] Render 2 message type (Deduped/Failed)
- [ ] i18n 2 locale + regen
- [ ] Log chuẩn nhất quán

## Success Criteria

- Task có 3 loại kết thúc (thành công/dedup/skip-tên) + fail → message hoàn thành hiện đúng 4 con số, tổng khớp số element.
- Không task nào dedup → message y như cũ (không thêm dòng Deduped).

## Risk Assessment

- **Risk**: đổi state render làm lệch progress các task cũ đang chạy khi deploy — task đang chạy dùng binary cũ, task mới dùng mới; không migration gì. An toàn.
- **Risk**: i18n key trùng reserved word go-i18n — đã áp dụng bài học (không dùng `id`).

## Security Considerations

- Không có.

## Next Steps

- Phase 6 ship + verify end-to-end trên prod.
