# Phase 4 — Lớp 2: sha256 check trước upload

## Context Links

- Phase 1 — `DedupHasHash`, `RecordDedup`
- `core/tasks/batchtfile/execute.go` — điểm sau tải xong, trước upload (`processElement` nhánh thường; `processBatch` trước `SaveBatch`)
- 6 task non-TG có `.Save(`: `parsed`, `directlinks`, `ytdlp`, `aria2dl`, `transfer`, `telegraph` (cùng `tfile`)
- Research: sha256 stdlib ~1-2 GB/s (SHA-NI) — 2GB file ≈ 1-2s, không đáng kể so với tải

## Overview

- **Priority**: P1
- **Status**: Pending
- Hash nội dung file đã tải (cache) → nếu `(user_id, storage_name)` đã có hash này → bỏ upload, đánh dấu deduped. Sau upload thành công → ghi hash (+ file-id nếu nguồn TG) vào ledger dedup.

## Key Insights

- Hash tính bằng **đọc tuần tự file cache** sau khi tải xong — download song song ghi out-of-order qua `WriterAt` nên không bọc hash vào writer được (đã cân nhắn trong research).
- **[RT-10, modified] Giữ đủ 8 task theo contract "tất cả tính năng lưu", nhưng chỉ hash khi có file local** — 4 task (parsed/directlinks/transfer/telegraph) có nhánh stream (khi `config.C().Stream` bật) lưu trực tiếp reader: nhánh đó skip hash + log debug. Mặc định Stream=false → các task này có file cache → hash hoạt động. ytdlp/aria2dl luôn có file.
- **[RT-8] SkipLarge TG trả `nil` im lặng** (telegram.go:135-138) — ghi hash cho file chưa hề lưu → lần sau dedup chặn = mất dữ liệu. Fix: sentinel `storagetypes.ErrSkippedLarge` từ SkipLarge branch (sửa luôn bug name-ledger có sẵn: hôm nay path vẫn bị recordSaved dù không upload). Call site: nhận sentinel → coi như skipped (không fail, không record gì).
- **[RT-9] Album filter phải lockstep**: `items` và `successElems` lọc cùng 1 pass NGAY TRƯỚC vòng `os.Open` (execute.go:214), assert `len(items)==len(successElems)` — callback progress map index position giữa 2 slice.
- Resend (phase 3) không có hash — không ghi hash cho đường đó.
- **[RT-5] `UserID <= 0` → skip check + record** (task API/non-TG) — không tạo namespace 0 chung.

## Requirements

- Log lý do: `dedup skip (hash sha256:abcd…ef) → storage X, path Y`.
- Ghi ledger dedup sau upload THÀNH CÔNG thật — **trừ khi storage trả `ErrSkippedLarge`** (RT-8): không record gì, đếm như skipped.
- Hash rỗng/"" không bao giờ match (phase 1 đảm bảo bằng where clause).

## Architecture

**[RT-11] KHÔNG tạo package mới** — gom hết vào 2 chỗ có sẵn:

```go
// common/utils/fsutil — thêm
func Sha256File(path string) (string, error) // stream 1MB buffer, hex lowercase

// database/saved_file_dedup.go — thêm (cạnh query nó compose)
func DedupCheckHash(ctx, userID int64, storageName, localPath string) (skip bool, hash string, err error)
// = Sha256File + DedupHasHash; userID<=0 → (false, "", nil) ngay (RT-5)
func DedupRecordSaved(ctx, userID int64, storageName, fileUniqueID, contentHash string, size int64, storagePath string) error
```

Call sites:
1. `batchtfile.processElement`: sau download OK + ext detect, trước upload → DedupCheckHash → skip thì dọn cache + MarkDeduped + return nil; upload xong (err==nil && !errors.Is(err, ErrSkippedLarge)) → DedupRecordSaved.
2. `batchtfile.processBatch`: **một pass lọc lockstep** (items+successElems) trước os.Open (RT-9); item trùng → MarkDeduped + loại; group rỗng → return nil.
3. `tfile/execute.go`: như (1).
4. 6 task non-TG: chỉ khi có local file (RT-10); nhánh stream skip + log debug; các site nhận `ErrSkippedLarge` tương tự.

## Related Code Files

- Sửa: `common/utils/fsutil/` (Sha256File + test), `database/saved_file_dedup.go` (DedupCheckHash/DedupRecordSaved + test), `pkg/storagetypes/` (ErrSkippedLarge), `storage/telegram/telegram.go` (SkipLarge trả sentinel), `core/tasks/batchtfile/execute.go`, `core/tasks/tfile/execute.go`, `core/tasks/{parsed,directlinks,ytdlp,aria2dl,transfer,telegraph}/execute.go`

## Implementation Steps

1. `Sha256File` vào fsutil + test (file rỗng, known-vector).
2. `DedupCheckHash`/`DedupRecordSaved` vào saved_file_dedup.go + test (copy file → skip true; khác 1 byte → false; UserID=0 no-op).
3. `ErrSkippedLarge` sentinel + telegram SkipLarge branch trả nó + các caller hiện tại không vỡ (các retry/recordAfterSave path xử lý sentinel như "không record, không fail").
4. Cắm (1)(3) — task TG-source; ErrSkippedLarge gate record.
5. Cắm (2) — album lockstep filter + assert + test progress attribution.
6. Cắm 6 task non-TG (chỉ khi có file local; stream branch skip + debug log).
7. Log chuẩn cả nhánh skip + record.

## Todo List

- [ ] Sha256File (fsutil) + test
- [ ] DedupCheckHash/DedupRecordSaved + test
- [ ] ErrSkippedLarge sentinel + SkipLarge branch + caller handling
- [ ] batchtfile đơn (gate record theo sentinel)
- [ ] batchtfile album (lockstep filter + test)
- [ ] tfile
- [ ] 6 task non-TG (conditional)
- [ ] Log chuẩn

## Success Criteria

- Test: 2 file khác nhau trùng nội dung (copy) → lần 2 DedupCheckHash true; khác 1 byte → false.
- Test: **SkipLarge-sized file → KHÔNG ghi hash, không ghi name-ledger, item đếm skipped (RT-8)**.
- Test: **album 4 item, item 2-3 dedup sau tải → progress attribution đúng từng item, phase counts đúng (RT-9)**.
- Trên prod: save 2 tin khác nhau cùng file (khác tên) → tin 2 log `dedup skip (hash`, không có message mới trong chat storage.

## Risk Assessment

- **Risk**: đọc đĩa thêm 1 lần/file — đo đạc: file 2GB NVMe ~1s, HDD ~10-15s vẫn nhỏ so với tải; nếu thành nút thắt sau này, chuyển sang hash-băng-tải ở writer tuần tự (stream mode) — ghi follow-up, không làm v1.
- **Risk**: 6 task non-TG có flow riêng (upload trực tiếp từ reader không qua file cache?) — scouting từng site khi làm; nếu reader không phải file vật lý (vd streaming HTTP) → bỏ qua hash tại site đó, ghi chú (chỉ record khi có file). KHÔNG refactor flow các task đó.
- **Risk**: SQLite write thêm ~1 dòng/file — volume đã chứng minh ổn (ledger hiện tại 444 dòng).

## Security Considerations

- Hash chỉ dùng so khớp nội dung nội bộ — sha256 không reversible, không rò thông tin.

## Next Steps

- Phase 5 gắn counter Deduped (markItemDeduped đã dùng ở phase 2/3/4 — giờ có thật).
