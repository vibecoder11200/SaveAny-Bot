# Phase 1 — Phân loại media + parser MediaFilter

Sở hữu file (chỉ phase này được đụng): `client/bot/handlers/utils/mediautil/media_type.go` *(mới)*, `client/bot/handlers/utils/mediautil/media_type_test.go` *(mới)*.

## Mục tiêu
Hàm phân loại media thuần (không RPC, không I/O) + parser bộ lọc, dùng chung cho mọi luồng ở phase 2. Table-driven tests theo convention repo.

## Bước thực hiện

1. **`media_type.go`** — `MediaType(media tg.MessageMediaClass) string`:
   - `*tg.MessageMediaPhoto` → `photo`
   - `*tg.MessageMediaDocument` → doc.AsNotEmpty() rồi scan `Attributes` theo thứ tự ưu tiên: `*tg.DocumentAttributeSticker` → `sticker`; `*tg.DocumentAttributeAnimated` → `animation`; `*tg.DocumentAttributeVoice` → `voice`; `*tg.DocumentAttributeAudio` → `audio`; `*tg.DocumentAttributeVideo` → `video` (gồm round video); rơi xuống cuối → `document`.
   - Media không hỗ trợ/nil → `""` (người gọi đã chặn bằng `IsSupported` từ trước).
   - Hằng số token: `const MediaTypePhoto = "photo" / video / animation / audio / voice / sticker / document` (export để phase 2 dùng + map alias).
2. **`MediaFilter`**:
   ```go
   type MediaFilter map[string]bool          // token -> cho phép
   func ParseMediaFilter(spec string) (MediaFilter, error)
   // "video,photo" → {video:true, photo:true}; "" → nil (nil = cho tất cả)
   // alias: gif→animation, music→audio, vid→video, img→photo, doc→document
   // token lạ → error (để /save báo ngay thay vì lọc sai — fail-closed, red-team #10)
   func (f MediaFilter) Allows(mediaType string) bool  // nil → true; "" → false
   func IsMediaFilterSpec(arg string) bool  // red-team #14: chỉ kiểm HasPrefix(arg, "t:")
   ```
   Quy tắc phát hiện (phase 2 áp dụng): arg bắt đầu bằng `t:` → **luôn** là media filter; parse lỗi → báo lỗi ngay cho user kèm token sai + danh sách token hợp lệ (fail-closed #10 — không âm thầm rơi về regex). Regex legacy bắt đầu `^t:` hay `[^t]` vẫn là regex vì không bắt đầu bằng `t:` literal. Error của ParseMediaFilter chứa token lỗi + danh sách token hợp lệ (user-facing — phase 2 đưa thẳng vào reply).
3. **Tests** (`media_type_test.go`, dựng `tg.MessageMediaDocument` với attributes thật — không mock interface):
   - 7 loại + video round + ảnh nén + document mime image/jpeg vẫn là `document` (photo token chỉ dành cho MessageMediaPhoto — ghi rõ trong doc comment).
   - Parse: chuỗi thường, có alias, có space, chuỗi rỗng → nil, token lạ → error (kèm token lỗi trong message).
   - Allows: nil filter cho tất cả; filter có token thì loại không nằm trong set bị chặn.
   - IsMediaFilterSpec: `t:video,photo` / `t:vid` / `t:bogus` / `t:` → true (bất cứ cái gì có prefix); `^t:foo$` / `video,photo` → false. Phần phân định lỗi/sạch do ParseMediaFilter đảm nhiệm.

## Verify

```bash
gofmt -w client/bot/handlers/utils/mediautil/
go build ./... && go vet ./client/bot/handlers/utils/mediautil/
go test ./client/bot/handlers/utils/mediautil/
```

Chấp nhận: test xanh, không đổi behavior hiện có (chỉ thêm hàm mới — `IsSupported` giữ nguyên).
