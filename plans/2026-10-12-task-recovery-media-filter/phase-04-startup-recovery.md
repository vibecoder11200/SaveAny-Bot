# Phase 4 — Phục hồi task lúc khởi động

> **Red-team 2026-10-12 (findings #2, #4, #5, #6, #7) — 5 thay đổi thiết kế**: (1) Re-fetch bằng helper mới `GetMessagesByIDs` theo danh sách ID thật — `GetMessagesRange` nổ đầy range (2 message rải rác = 299 RPC). (2) Fetch bằng đúng client đã tạo file (`ViaUserbot` trong payload) — forward task dùng bot client, "ưu tiên userbot" sẽ fail ở PM chat user khác owner. (3) Recovery **reuse row.ID** làm task ID mới, KHÔNG xóa row lúc enqueue — idempotent qua vô hạn lần crash. (4) Phân biệt message-deleted với lỗi hạ tầng; drop theo **tuổi row (48h)** thay vì đếm attempts (đếm boots = restart loop trong flood penalty làm mất sạch task). (5) Exists-skip dồn vào trong Execute **bất kể conflict strategy** (default là rename → nếu không 200 file cũ sẽ tải lại thành `file(1).ext`) — khỏi HTTP storm lúc boot.

Sở hữu file: `client/bot/recovery.go` *(mới)*, `common/utils/tgutil/message.go` *(helper GetMessagesByIDs)*, `cmd/run.go` *(gọi recovery.Run sau bot init)*, `core/tasks/tfile/execute.go` *(prelude SkipIfExists — chung file với phase 6 nhưng khác vùng, phase 6 land trước)*, `common/i18n/locale/*.yaml` + `i18nk` *(summary keys)*. Không đụng file của phase 2.

## Mục tiêu
Sau khi bot + userbot sẵn sàng, quét PendingTask → tái tạo task (idempotent) → mỗi user nhận 1 tin tổng kết. Restart liên tiếp nhiều lần không duplicate, không mất.

## Bước thực hiện

1. **`tgutil.GetMessagesByIDs(ctx, chatID int64, ids []int) (map[int]tg.MessageClass, error)`** (finding #6): dedup + sort ids → chunk 100 → `ctx.GetMessages(chatID, InputMessageClassSliceFromInt(chunk))` (helper có sẵn `message.go:105`) → gom map. KHÔNG dùng GetMessagesRange (nó expand mọi số nguyên trong `[min,max]` — `message.go:139-159`).
2. **recovery.Run(ctx, botCtx *ext.Context)** — goroutine riêng (không block startup):
   - `config.C().Task.Recovery == false` hoặc `ListPending` rỗng → return.
   - Drop theo tuổi trước (finding #4): row `CreatedAt` > 48h → xóa + đếm bucket `Dropped` (không đếm boots; restart loop vô hại).
   - Group theo (UserID × client cần thiết): fetch refs theo `ViaUserbot` — nhóm userbot bằng userbot ctx (nếu enable), nhóm bot bằng botCtx (finding #7). Client cần thiết mà chưa enable → bucket `ClientUnavailable`, xóa row, báo trong summary (điều kiện vĩnh viễn theo config, retry vô ích).
   - Fetch qua `GetMessagesByIDs` theo chat. Phân biệt 2 loại lỗi (finding #4):
     - **Message vắng mặt trong kết quả fetch** (fetch thành công, msgID không trả về / MSG_ID_INVALID) = nguồn đã xóa → bỏ file đó, đếm `Deleted`, task tiếp tục với phần còn lại (mọi file đều vắng → task hoàn tất rỗng).
     - **Lỗi RPC của chính fetch** (network/flood) = hạ tầng → `BumpAttempts` các row của user đó, GIỮ nguyên, dừng recovery user này ở boot này (boot sau sau khi penalty hết tự lành — không đốt task).
   - Storage: `storage.GetStorageByUserIDAndName` lỗi/thiếu → bucket `StorageFailed`, xóa row, báo summary (như cũ).
   - Enqueue: task đơn → `tftask.NewTGFileTask(row.ID, ...)`; batch → `batchtfile.NewBatchTGFileTask(row.ID, ...)` — **dùng lại row.ID** (finding #2): choke point phase 3 Record-upsert đè chính row đó (không sinh row mới), row chỉ bị xóa khi task hoàn tất thật (worker Complete). Crash giữa recovery → boot sau thấy đúng row cũ, chạy lại từ đầu — idempotent vô hạn.
   - Progress: mỗi task phục hồi 1 message mới per user (botCtx direct send — không có update để Reply), KISS như cũ; summary là message cuối.
3. **SkipIfExists trong Execute (finding #5)**: task mang flag `SkipIfExists bool` (recovery set true). Trong `tfile/execute.go` prelude (trước download): `stor.Exists(ref.Path)` → tồn tại = bỏ qua file, đếm skipped. Với task recovery, flag này **ghi đè conflict strategy của user** (kể cả rename-default và ask — boot không có prompt); existence lúc recovery = tiến độ cũ của chính task này = hợp đồng at-least-once. Task thường (flag false) giữ behavior hiện tại y nguyên. Không còn vòng Exists nào trong recovery.Run (mỗi file 1 HTTP call lên alist lúc boot = tự DoS).
4. **cmd/run.go**: sau `bot.Init` + có `bot.ExtContext()` → `go recovery.Run(ctx, ectx)`. Log INFO bắt đầu/kết thúc + số liệu từng bucket để quan sát trên prod.
5. **i18n**: key `info_recovery_summary` với `{TasksRecovered, FilesRecovered, Deleted, SkippedExists, StorageFailed, ClientUnavailable, Dropped}` — ẩn phần số 0. 2 locale + `go generate`.

## Edge đã xử lý (từ red-team)
- Album: message objects mang grouped_id nguyên vẹn → sourceGroupKey tự sinh lại → telegram storage vẫn gom album.
- File reference: luôn tươi vì re-fetch (lý do payload chỉ lưu msgID).
- Restart liên tiếp: row-ID reuse + upsert → không duplicate, không mất (không còn cửa sổ "enqueue xong nhưng chưa kịp xóa row cũ").
- User đã bị bỏ whitelist: task vẫn chạy như task thường; summary gửi qua bot chat cũ.
- Flood penalty đang chạy khi restart: fetch lỗi hạ tầng → giữ row, boot sau thử lại; attempts chỉ để log.

## Verify

```bash
go build ./... && go vet ./... && go test ./client/bot/... ./pkg/journal/ ./common/utils/tgutil/
# manual local: tạo task → kill -9 giữa download → restart → log recovery + task chạy tiếp + summary.
# restart 2 lần liên tiếp ngay trong lúc recovery → không duplicate task, không mất row.
# /cancel task queued → restart → KHÔNG hồi sinh. Ctrl+C giữa download → restart → task phục hồi (row chưa bị xóa).
```

Chấp nhận: đúng kịch bản acceptance trong plan.md; default user (conflict=rename) crash giữa batch 500 file tại file 200 → restart → 200 file cũ KHÔNG tải lại, KHÔNG sinh `file(1).ext`, chạy nốt 300 file còn lại.
