# Phase 2 — Tích hợp filter vào /save, watch, link-batch + /config

> **Red-team 2026-10-12 (findings #9, #10, #12, #13, #14)**: (1) WatchChat.Filter là 1 chuỗi `type:expr` — không nhồi thêm mediatype vào đó; thêm **cột `MediaType` riêng** (AutoMigrate). (2) UI /config dùng **flip-on-tap stateless** đúng vocabulary hiện có (`fnamest`/`conflictst` áp dụng tức thì) — bỏ màn hình 9 nút + nút Save. (3) Fail-closed: `t:` lỗi cú pháp báo lỗi ngay, không âm thầm cho qua tất cả; callback validate token. (4) Grammar 2-arg khai báo rõ. (5) Phần per-user default + /config UI + link-batch default: giữ trong **fork**, để PR upstream sau (phase 5).

Sở hữu file: `client/bot/handlers/save.go`, `client/bot/handlers/watch.go`, `client/bot/handlers/utils/shortcut/message.go` *(chỉ hàm GetFilesFromUpdateLinkMessageWithReplyEdit)*, `client/bot/handlers/config.go`, `client/bot/handlers/utils/msgelem/` *(nếu cần builder keyboard)*, `database/model.go` *(cột User.MediaFilter + cột WatchChat.MediaType)*, `database/user.go` / `database/watch.go` *(nếu có hàm update)*, `common/i18n/locale/{en,zh-Hans}.yaml`, `common/i18n/i18nk/keys.go` *(generate)*, docs để phase 5.

## Mục tiêu
Filter áp cho 3 luồng tự động (watch, /save, link-batch), default per-user, forward tay KHÔNG lọc.

## Bước thực hiện

1. **DB**: `User.MediaFilter string` (rỗng = tất cả) + `WatchChat.MediaType string` (rỗng = không lọc — **cột riêng**, không đụng chuỗi `Filter` của msgre; red-team #13). AutoMigrate có sẵn. `func (u *User) MediaFilterSet() mediautil.MediaFilter` — parse 1 lần; giá trị lưu chỉ được ghi qua đường đã validate (bước 5) nên lỗi parse = dữ liệu hỏng hiếm → log WARN + nil (cho tất cả) + không crash.
2. **/save** (`save.go handleBatchSave`) — grammar mới (red-team #14), khai báo rõ trong help + docs:
   ```
   /save <chat> <range> [t:types] [regex]
   ```
   - `IsMediaFilterSpec(args[2])` (chỉ check prefix `t:`) → parse `args[2]`; **parse lỗi → reply lỗi ngay kèm token sai + bảng token hợp lệ, không chạy task** (fail-closed #10). Regex (nếu user gõ thêm) nằm ở `args[3]` — handler mở rộng đọc thêm 1 arg; không có `t:` thì `args[2]` là regex như cũ (100% backward-compat).
   - Thứ tự ưu tiên: `t:` của lệnh > `user.MediaFilterSet()` > nil. Áp `Allows(MediaType(media))` ngay trong vòng lọc media (trước `FromMediaMessage` — khỏi build file).
3. **/watch** (`watch.go`): cú pháp user-facing thêm `mediatype:photo,audio` trong arg filter (cùng chỗ `msgre:` như cũ) nhưng **lưu vào cột `WatchChat.MediaType` riêng**. Parse filter lúc tạo watch: token lạ → báo lỗi ngay (fail-closed, không lưu watch hỏng). `listenMediaMessageEvent`: parse `WatchChat.MediaType` 1 lần (lỗi parse ở listen-time = dữ liệu hỏng → log ERROR + coi như không match gì để an toàn, không âm thầm cho qua — #10). Check mediatype trước cả msgre. Watch không có mediatype riêng → áp user default.
4. **Link-batch** (`shortcut/message.go` — hàm addFile loop): áp `user.MediaFilterSet()`.
5. **/config** (`config.go`) — **flip-on-tap stateless** (red-team #12, đúng pattern `fnamest`/`conflictst`):
   - Menu cha thêm nút "Media filter". Màn hình con: 7 dòng toggle (✅/⬜️ mỗi loại) + 1 dòng "All (clear)".
   - Callback `config mediafilter <token>`: **validate token ∈ 7 enum trước** (token lạ → log + ignore, #10) → toggle token trong `User.MediaFilter` → `UpdateUser` → rerender màn hình ngay (không nút Save, không state trung gian — callback data vẫn vô hại, mất state không sao).
   - Callback `config mediafilter all` → xóa trắng chuỗi (rỗng = nhận tất cả).
   - Chuỗi lưu canonical comma-separated; mọi giá trị ghi đi qua validate ở đây.
6. **i18n** (~12 key × 2 locale): menu label, màn hình toggle, đã lưu, watch help bổ sung `mediatype:`, save help bổ sung `t:` + grammar 2-arg. `go generate ./...`.

## Verify

```bash
go generate ./... && gofmt -w <files>
go build ./... && go vet ./...
go test ./client/bot/handlers/... ./database/...
```

Chấp nhận:
- `/save chat 1-100 t:video` chỉ giữ video; `/save chat 1-100 t:video,photo ^re$` lọc cả hai (regex ở arg thứ 4); `/save chat 1-100 ^t:old$` vẫn là regex thuần (không bị bắt làm filter); `/save chat 1-100 t:viseo` → báo lỗi token, không chạy.
- Watch có `mediatype:photo` chỉ lưu ảnh; watch không filter + user default `video` → chỉ video; watch có cả `msgre:` + `mediatype:` → cả hai cùng áp (2 cột riêng).
- /config: tap `video` → ✅ + lưu tức thì; tap lại → ⬜️; "All" → xóa trắng. F5/rerender không lệch state.
- Forward tay (media.go) không đổi — KHÔNG chạm `media.go`/`media_group.go`.
- User cũ (cột rỗng) + WatchChat cũ (MediaType rỗng) = behavior hiện tại 100%.
