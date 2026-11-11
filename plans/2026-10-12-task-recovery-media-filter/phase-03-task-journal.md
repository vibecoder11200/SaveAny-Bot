# Phase 3 — Journal task TG-file (ghi khi tạo, xóa khi xong)

> **Red-team 2026-10-12 (findings #2, #3, #8, #11) — 4 thay đổi thiết kế**: (1) Hook dời từ "3 creation sites" (đếm sai — thực tế 5 site gồm `watch.go:330`, `watch.go:425` album, `api/factory.go:75`) về **choke point `core.AddTask`** — mọi site hiện tại và site mới của upstream tự động được phủ. (2) Ma trận cancel: shutdown giữ row, user-cancel xóa row — 2 chiều cũ đều sai. (3) SQLite pragma phải qua DSN (ncruces), không qua `db.Exec`. (4) Thứ tự Record-trước-enqueue triệt tiêu race orphan row. (5) Payload thêm `ViaUserbot` + tên file đã sanitize.

Sở hữu file: `database/model.go` *(struct PendingTask)*, `database/driver.go` *(DSN pragma)*, `database/db.go` *(thêm model vào AutoMigrate + pool cap)*, `pkg/journal/journal.go` *(mới)*, `pkg/journal/journal_test.go` *(mới)*, `core/core.go` *(Record trong AddTask + Complete trong worker + hook CancelTask)*, `core/tasks/tfile/tfile.go` + `core/tasks/batchtfile/batchtfile.go` *(method JournalPayload — struct file, KHÔNG đụng execute/stream/writer của phase 6)*, `pkg/tfile` *(hàm sanitize tên file nếu chưa có)*, `config/task.go` *(mới: `[task] recovery = true`)* + `config/viper.go` *(default)*.

## Mục tiêu
Mọi task TG-file khi vào queue có dòng journal (từ **duy nhất 1 điểm** trong code); khi kết thúc thật sự dòng bị xóa; restart bất kỳ lúc nào (kể cả Ctrl+C) → những gì chưa xong còn nguyên trong DB; task bị user hủy không bao giờ "hồi sinh".

## Bước thực hiện

1. **Model + payload**:
   ```go
   type PendingTask struct {
       ID        string    `gorm:"primaryKey"` // = task xid
       UserID    int64
       Payload   string    // JSON, schema ở dưới
       Attempts  int       // số lần recovery gặp lỗi hạ tầng (chỉ để log/observe)
       CreatedAt time.Time // cơ sở drop theo tuổi (phase 4)
   }
   ```
   ```go
   type TGFileRef struct { ChatID int64; MsgID int; Name string; StorageName string; Path string; ViaUserbot bool }
   type TGTaskPayload struct { Files []TGFileRef; AsBatch bool; Source string }
   ```
   `ViaUserbot` = client nào đã build file lúc tạo (forward = bot; /save+link = userbot nếu enable; watch = userbot) — phase 4 fetch lại bằng đúng client đó. Name/Path là đích ĐÃ resolve (kể cả rules) và **đã sanitize** (finding #8: xem bước 2).
2. **Sanitize tên file (finding #8 — path traversal)**: thêm `pkg/tfile.SanitizeFileName(name string) string` (bóc `../`, `..\`, ký tự không hợp path, rút gọn về base name) áp tại điểm build TGFile duy nhất (`tfile.FromMediaMessage`/tương đương — mọi flow đều đi qua). Journal vì thế chỉ persist tên sạch; replay không thể thoát khỏi cache/storage dir.
3. **SQLite qua DSN (finding #11)**: ncruces gormlite chỉ nhận pragma per-connection qua DSN (`driver/driver.go:77-79`) — sửa `GetDialect` thành `file:<path>?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)`. Thêm `sqlDB.SetMaxOpenConns(1)` sau open (write volume thấp, loại hẳn SQLITE_BUSY giữa worker Complete × handler Record).
4. **pkg/journal** (import database, KHÔNG import core):
   - `Record(ctx, taskID string, userID int64, p TGTaskPayload) error` — upsert theo ID; check `config.C().Task.Recovery` tắt thì no-op; lỗi DB → log WARN + trả error (caller chỉ log, không chặn task).
   - `Complete(ctx, taskID string)` — DELETE WHERE id, idempotent; gọi đồng bộ (DELETE sqlite ~µs), lỗi chỉ log.
   - `ListPending(ctx) []PendingTask`, `BumpAttempts(ctx, id)`.
5. **Choke point `core.AddTask` (finding #2)** — KHÔNG hook rải rác theo site:
   - Hai loại task implement interface nhỏ (định nghĩa trong core): `type journalSource interface { JournalPayload() (userID int64, p journal.TGTaskPayload, ok bool) }` — trong `tfile.go`/`batchtfile.go` (struct file), đọc từ fields sẵn có của task + tracker.
   - `core.AddTask`: nếu task implement `journalSource` → **`journal.Record` TRƯỚC khi enqueue** (finding #11: task chưa vào queue thì chưa thể chạy → Complete không thể xảy ra trước Record); enqueue lỗi → `journal.Complete` bù trong cùng nhánh error.
   - Tự phủ cả 5 site thật (tftask.go ×2, watch.go ×2, api/factory.go) và mọi site upstream thêm sau này. Bước grep-verify giữ lại chỉ để đối chiếu danh sách loại trừ (ytdlp/aria2/transfer theo non-goals).
6. **Ma trận cancel/hoàn tất (finding #3)** — 3 đường, 3 hành vi:
   | Đường | Row | Cơ chế |
   |---|---|---|
   | Worker kết thúc bình thường (thành công/thất bại) | **xóa** | `journal.Complete` sau khối log kết quả trong worker (defer) |
   | User hủy task đang **chạy** | **xóa** | worker nhận ctx canceled từ `core.CancelTask` → nhánh canceled → Complete (như trên) |
   | User hủy task đang **queued** (bypass worker — `pkg/queue/queue.go:74` discard im lặng) | **xóa** | hook trong `core.CancelTask`: sau queue cancel thành công → Complete (DELETE idempotent nên 2 đường không giẫm chân) |
   | **Shutdown** (root ctx chết: Ctrl+C / bot client drop) | **GIỮ NGUYÊN** | worker check `errors.Is(rootCtx.Err(), context.Canceled)` trước khi Complete → bỏ qua Complete trên nhánh shutdown — đây là dữ liệu recovery phải cứu |
7. **Config**: `[task] recovery = true` (mặc định true). Viper defaults + config.example.toml (phase 5 docs chi tiết).

## Verify

```bash
go build ./... && go vet ./...
go test ./pkg/journal/ ./core/ ./database/... ./pkg/tfile/...
# test journal: sqlite temp file qua DSN mới; Record→ListPending thấy row; Complete→mất; Record duplicate ID → upsert không lỗi.
# test core: (a) task chạy xong → row mất; (b) CancelTask khi queued → row mất (không hồi sinh sau restart); (c) shutdown-cancel (root ctx cancel) → row CÒN; (d) AddTask lỗi enqueue → row bị xóa bù.
# test sanitize: "../../etc/passwd" → "passwd"; "a/b.mp4" → "b.mp4".
```

Chấp nhận: chạy bot local, tạo 1 forward task → row sinh ra; task xong → mất; kill -9 giữa task → còn; `/cancel` task queued → row mất ngay; Ctrl+C giữa task → row còn (chờ recovery phase 4).
