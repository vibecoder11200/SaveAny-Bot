# Phase 2 — Lớp 1: file-id check trước tải

## Context Links

- Phase 1 (`FileUniqueID`, `DedupHasFileID`, `RecordDedup`)
- `client/bot/handlers/utils/shortcut/tftask.go` — vòng lặp tạo element lúc /save (đã có Exists check theo tên ở :183-193 và album :226-246)
- `core/tasks/batchtfile/execute.go` — `processElement` / `downloadElement` sau `refreshElemFile`
- `core/tasks/tfile/execute.go` — task file đơn

## Overview

- **Priority**: P1
- **Status**: Pending
- Chặn download+upload khi `(user_id, storage_name)` đã từng lưu file Telegram có cùng file-id. Chạy ở 2 tầng: lúc tạo task (skip khỏi element list — không tạo task item) và runtime (recovery/direct-add path).

## Key Insights

- **[RT-12 rejected — giữ cả 2 tầng, lý do]**: runtime check là tầng load-bearing (recovery không qua tftask.go), NHƯNG mỗi element runtime đi qua `refreshElemFile` = 1 RPC. Re-run 295 file chỉ qua runtime = 295 RPC thừa; creation-time check (DB-only, <0.5s tổng) chặn sạch re-run phổ biến TRƯỚC khi tốn RPC. Conflict theo tên phải chạy ở creation (strategy là quyết định per-command); file-id dedup chạy sau nó.
- `refreshElemFile` (FRE fix) refetch message trước khi tải — file-id lấy từ media **đã refresh**, đúng bản chất.
- Per-storage semantics: query luôn kèm `storage_name` của element (sau khi apply rule — `fileStor`).

## Requirements

- Lịch sử skip phải ghi rõ lý do trong log: `dedup skip (file-id doc:123) → storage X, path Y`.
- KHÔNG thay đổi behavior conflict theo tên hiện tại — file-id check chạy **sau** Exists-theo-tên (tên trùng vẫn ưu tiên skip như cũ, tránh đổi semantics người dùng đã quen).
- Element deduped ở runtime được đếm là deduped (tracker API làm ngay phase này — RT-13), KHÔNG phải failed.

## Architecture

Tầng tạo task (`tftask.go`), trong vòng lặp file đơn — ngay sau khối Exists theo tên:

```go
if fid := file.FileUniqueID(); fid != "" {
    if database.DedupHasFileID(ctx, user.ChatID, fileStor.Name(), fid) {
        deduped = append(deduped, file.Name())
        continue // không tạo element
    }
}
```

Album loop tương tự cho từng item. `deduped` list truyền vào tracker (constructor nhận thêm list — API thật làm ở phase này theo RT-13).

Tầng runtime — đầu `processElement`/`downloadElement`, sau `refreshElemFile`:

```go
if t.UserID > 0 { // RT-5: user_id=0 → no-op
    if fid := elem.File.FileUniqueID(); fid != "" && database.DedupHasFileID(ctx, t.UserID, elem.Storage.Name(), fid) {
        t.markItemDeduped(elem.ID)
        logger.Infof("dedup skip (file-id %s): already saved to storage %s", fid, elem.Storage.Name())
        return nil // processElement
    }
}
```

**[RT-3 — CRITICAL] Trong `downloadElement` (album path) PHẢI trả sentinel `errSkippedExists`** (execute.go:39) thay vì `nil`: `downloadElement` trả nil → elem vào `successElems` → `os.Open(localPath)` fail ENOENT (cache chưa tạo) → markItemFailed đè lên deduped + hủy cả nhóm đã tải. Sentinel được success-filter (:182-187) loại đúng + all-skipped accounting (:188-203) xử lý sẵn. Unit test: album 5 item, item giữa dedup → nhóm còn lại hoàn thành đủ số.

`tfile/execute.go` tương tự (return theo flow task đơn).

## Related Code Files

- Sửa: `client/bot/handlers/utils/shortcut/tftask.go`, `core/tasks/batchtfile/execute.go`, `core/tasks/tfile/execute.go`
- Dùng: `database/saved_file_dedup.go` (phase 1)

## Implementation Steps

1. **[RT-13] Tracker API thật ngay phase này**: `progress.go` thêm `deduped []string` + mutex sẵn có, `MarkDeduped(name)`/`MarkItemDeduped(id)`, `DedupedCount()` — KHÔNG wiring tạm.
2. Thêm check tạo-task ở 2 vòng lặp (file đơn + album) trong `tftask.go`; deduped list truyền vào tracker (constructor nhận thêm list — đổi mọi caller).
3. Runtime check ở `processElement` (return nil) + `downloadElement` (**return errSkippedExists** — RT-3) sau `refreshElemFile` thành công.
4. Runtime check ở `tfile/execute.go`.
5. Log chuẩn `dedup skip (file-id ...)` — grep được.

## Todo List

- [ ] Tracker: MarkDeduped/DedupedCount (API thật, không tạm)
- [ ] Check tạo-task tftask.go (đơn + album)
- [ ] Runtime check batchtfile — processElement nil / downloadElement sentinel (RT-3)
- [ ] Runtime check tfile
- [ ] Test album có item giữa bị dedup
- [ ] Log lý do từng lần chặn

## Success Criteria

- Unit test: task tạo từ file đã có fid trong ledger → không có element đó; runtime recovery gặp fid đã lưu → item hoàn thành không tải; **album 5 item với item #3 dedup → 4 item kia completed, #3 deduped, nhóm không fail (RT-3)**.
- Re-run `/save` trên range đã lưu: log hiện `dedup skip (file-id` cho từng file NGAY từ lúc tạo task (không element, không tải, không RPC refresh).

## Risk Assessment

- **Risk**: file-id false-positive làm mất file thật — chỉ xảy ra nếu Telegram trả cùng ID cho nội dung khác (không có bằng chứng; file_unique_id được docs đảm bảo stable-per-file). Mức chấp nhận được; hash layer phase 4 là lớp độc lập để đối chiếu.
- **Risk**: tftask.go check thêm DB call mỗi file (~300 files = 300 query SQLite đơn-conn) — mỗi query < 1ms, tổng < 0.5s: chấp nhận.

## Security Considerations

- Không có — query nội bộ theo user_id của chính người gọi lệnh.

## Next Steps

- Phase 3 (resend) tái dụng cùng điểm cắm sau `refreshElemFile` — chạy resend cho file KHÔNG bị dedup chặn.
