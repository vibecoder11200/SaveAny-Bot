# Phase 1 — Nền tảng: FileUniqueID + bảng dedup + hàm tra/ghi

## Context Links

- [Research report](../../reports/research-261023-0834-dedup-file-id-content-hash.md) — semantics file-id, benchmark hash
- `pkg/tfile/tgfile.go` — FromMedia đang nắm `document`/`photo` trong tay lúc build file
- `database/telegram_saved.go` + `database/telegram_saved_test.go` — pattern ledger + test đã được chứng minh

## Overview

- **Priority**: P1 — nền cho mọi phase sau
- **Status**: Pending
- Thêm method `FileUniqueID() string` cho `TGFile`; tạo bảng `saved_file_dedup` với hàm tra/ghi fail-open.

## Key Insights

- `tg.Document.ID` và `tg.Photo.ID` là định danh ổn định (tương đương Bot API `file_unique_id`), có sẵn trong message — 0 request.
- Prefix loại (`doc:`/`photo:`) vì document và photo là 2 không gian ID riêng.
- DB pool đang `MaxOpenConns(1)` — check-then-insert tuần tự, không cần unique constraint cứng; index thường là đủ, ghi trùng vô hại.

## Requirements

- FileUniqueID trả về `""` với file không có media nguồn (vd file build từ reader).
- **[RT-4] `FromMediaMessage` PHẢI truyền field `fileUniqueID`** — hàm này (tgfile.go:175-187) build struct thủ công, bỏ field sẽ chết im lặng lớp 1 ở mọi call site thật (save.go, message.go, media_group.go, watch.go, api, recovery). Refactor cả 2 constructor funnel qua 1 internal constructor để không thể lệch; unit test assert `FileUniqueID() != ""` qua `FromMediaMessage`.
- Dedup API fail-open: lỗi DB → trả `false` + WARN, không chặn lưu.
- **[RT-5] Rule `UserID <= 0`** (task API không set UserID; task non-TG không có field): bỏ BOTH check lẫn record, log INFO một lần — không cho phép namespace `user_id=0` chung ở shared API token.
- Ghi idempotent (ghi 2 lần không tạo 2 dòng logic trùng — cho phép dòng thừa vật lý, query vẫn đúng).

## Architecture

```go
// pkg/tfile: interface TGFile thêm
FileUniqueID() string // "doc:123" | "photo:456" | ""

// database/saved_file_dedup.go
type SavedFileDedup struct {
    ID           uint      `gorm:"primaryKey"`
    UserID       int64     `gorm:"index:idx_dedup_scope"`
    StorageName  string    `gorm:"index:idx_dedup_scope;index:idx_dedup_fileid;index:idx_dedup_hash"`
    FileUniqueID string    `gorm:"index:idx_dedup_fileid"` // "" với nguồn non-TG / file resend (v1 không hash)
    ContentHash  string    `gorm:"index:idx_dedup_hash"`   // "" với file resend
    Size         int64
    StoragePath  string
    CreatedAt    time.Time
}
func DedupHasFileID(ctx, userID int64, storageName, fileUniqueID string) bool
func DedupHasHash(ctx, userID int64, storageName, hash string) bool
func RecordDedup(ctx, userID int64, storageName, fileUniqueID, contentHash string, size int64, storagePath string) error
```

Query where: `user_id = ? AND storage_name = ? AND file_unique_id = ? AND file_unique_id != ''` (hash tương tự).

## Related Code Files

- Sửa: `pkg/tfile/tgfile.go` (interface + struct + FromMedia set field), `database/db.go` (AutoMigrate)
- Tạo: `database/saved_file_dedup.go`, `database/saved_file_dedup_test.go`

## Implementation Steps

1. Refactor `tgFile` construction: 1 internal constructor duy nhất dùng chung cho `FromMedia` + `FromMediaMessage`; cả 2 set `fileUniqueID` (`"doc:"+ID` / `"photo:"+ID`). Export method trên interface `TGFile`.
2. Kiểm tra các impl khác của TGFile (nếu có) compile.
3. Viết model + 3 hàm theo pattern `telegram_saved.go` (logger từ ctx, fail-open; `UserID<=0` → no-op theo RT-5).
4. AutoMigrate += `&SavedFileDedup{}`.
5. Test theo pattern `telegram_saved_test.go`: record → Has true; scope sai (khác storage/user) → false; hash rỗng không match; **FileUniqueID qua FromMediaMessage khác rỗng (RT-4)**; user_id=0 no-op.

## Todo List

- [ ] FileUniqueID trên TGFile + set trong FromMedia
- [ ] Model + AutoMigrate
- [ ] DedupHasFileID / DedupHasHash / RecordDedup
- [ ] Test pass (kể cả -race ở phase 6)

## Success Criteria

- `go build ./...` + `go test ./pkg/tfile/... ./database/...` xanh.
- Test chứng minh: cùng (user, storage, fid) → true; khác storage → false.

## Risk Assessment

- Thêm method vào interface TGFile làm vỡ impl bên ngoài (nếu plugin tự impl TGFile) — grep xác nhận chỉ có impl nội bộ; nếu có impl ngoài thì dùng interface mở rộng type-assert thay vì thêm method.

## Security Considerations

- Không có gì nhạy cảm — bảng chỉ chứa hash/id định danh, local SQLite.

## Next Steps

- Phase 2 dùng `FileUniqueID()` + `DedupHasFileID`.
