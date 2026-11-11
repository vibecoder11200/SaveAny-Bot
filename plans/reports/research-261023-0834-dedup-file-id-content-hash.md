# Research: Content dedup cho SaveAny-Bot (file_unique_id + content hash)

- **Ngày**: 2026-10-23 08:34 (+7)
- **Phạm vi**: semantics định danh file Telegram, thuật toán hash, điểm cắm trong pipeline SaveAny-Bot, schema ledger, tham chiếu tool tương tự
- **Nguồn**: core.telegram.org (Bot API types + MTProto files), gotd v0.149.0 source, benchmark xxhash/blake3 của cespare & zeebo, codebase local
- **Ghi chú công cụ**: WebSearch built-in hết hạn mức (reset 29/10) — dùng Exa web search thay thế.

## 1. Kết luận chính (TL;DR)

1. **Telegram có sẵn định danh ổn định để dedup**: `file_unique_id` (Bot API) = "cùng một file thì cùng ID qua thời gian và giữa các bot". Tương đương MTProto: `tg.Document.ID` (int64) cho document, `tg.Photo.ID` cho photo — **đọc trực tiếp từ message, biết trước khi tải, không tốn request nào**. Forward/copy nguyên bản giữ nguyên ID.
2. **Hiện không có hash nào trong SaveAny-Bot** — mọi skip đều theo đường dẫn file (`storage_path`), gây skip-nhầm khi trùng tên và bỏ sót khi trùng nội dung khác tên (đã xác minh bằng grep toàn bộ `storage/ database/ core/tasks/ pkg/tfile/`).
3. **Thuật toán hash không phải nút thắt**: xxhash 15-17 GB/s, blake3 (pure-Go AVX2) ~4 GB/s, sha256 stdlib (SHA-NI) ~1-2 GB/s. Với file 2 GB: sha256 mất ~1-2s — không đáng kể so với 6 phút tải. → Chọn **sha256** vì chuẩn, 256-bit không lo collision, công cụ ngoài đọc được.
4. **Phát hiện bonus lớn**: MTProto cho phép **gửi lại media có sẵn bằng `messages.sendMedia` + `inputMediaDocument` mà không cần re-upload** (file không bị khóa noforwards). Lưu TG→TG hiện tại đang tốn bandwidth tải + upload; có thể nhảy xuống ~KB. Đáng cân nhắc làm "lớp 0" của dedup.

## 2. Semantics định danh file Telegram

| Định danh | Ổn định như nào | Lấy ở đâu | Bắt được gì |
|---|---|---|---|
| `file_unique_id` (Bot API) | "Supposed to be the same over time and for different bots" — официальный dedup ID | Server cấp kèm mọi file object | Cùng underlying file dù ở chat nào |
| `tg.Document.ID` (MTProto) | Tương đương file_unique_id của document; forward giữ nguyên | `document.ID` — có sẵn trong message | Như trên; đây là thứ gotd exposе |
| `tg.Photo.ID` | ID của photo gốc; mọi size của cùng photo chung ID | `photo.GetID()` | Photo trùng giữa các kênh |
| Content hash (sha256) | Tuyệt đối theo byte | Phải tải xong mới tính | Mọi bản trùng byte, kể cả re-encode-tên / upload-lại |

Giới hạn của lớp file-id: file **upload lại độc lập** (uploader gửi lại cùng nội dung) có thể nhận ID mới trên server — chỉ hash nội dung mới chắc chắn. Hai lớp bổ trợ: file-id chặn sớm miễn phí, hash chặn triệt để.

Kèm theo: media có cờ `noforwards` (protected) **không được resend** bằng inputMediaDocument — lớp resend-by-id phải fallback về tải+upload khi gặp.

## 3. Benchmark hash (Go)

| Thuật toán | Throughput (input lớn) | Ghi chú |
|---|---|---|
| cespare/xxhash (asm) | 15-17 GB/s | 64-bit, non-crypto — quá đủ nhanh nhưng collision space nhỏ |
| zeebo/blake3 (AVX2) | ~4 GB/s | Pure-Go + ASM, 256-bit |
| crypto/sha256 (SHA-NI) | ~1-2 GB/s | Stdlib, 0 dependency, chuẩn mực |
| crypto/md5 | ~0.8-1 GB/s | Cũ, 128-bit, không nhanh hơn sha256 ở CPU hiện đại |

**Khuyến nghị: sha256 stdlib.** Lý do: tốc độ đã dư (bottleneck là tải/upload), 256-bit không cần lo collision, không thêm dependency, hash dùng kiểm tra tay bên ngoài (rclone/btrfs/shasum) đọc được.

Điểm tính hash: **đọc tuần tự file cache sau khi tải xong, trước khi upload** (download song song ghi WriterAt out-of-order nên không bọc hash ngay vào writer được; stream-mode thì bọc TeeReader). Chi phí: 1 lần đọc đĩa thêm — NVMe ~2GB/s, file 2GB ≈ 1s.

## 4. Bản đồ tích hợp trong SaveAny-Bot

**Lấy file-id (lớp 1):**
- `pkg/tfile/tgfile.go` — `FromMedia` đã có `document` (→ `document.ID`) và `photo` (→ `photo.GetID()`) trong tay; thêm field + method `FileUniqueID()` trên TGFile. Recovery rebuild cũng qua FromMedia nên tự có.
- Check trước khi tải: trong `batchtfile.processElement/downloadElement` (sau `refreshElemFile`, trước khi tạo cache file) — query ledger đã thấy file-id này chưa → skip cả download lẫn upload (đánh dấu skipped, hiện理由 trong log/message).

**Tính + check hash (lớp 2):**
- Sau tải (đủ 8 task type có call `.Save(`: telegraph, ytdlp, transfer, aria2dl, directlinks, tfile, parsed, batchtfile) — điểm chung nhất là **ngay trước gọi `storage.Save`**: đọc file → hash → check ledger → trùng thì skip upload, khác thì upload xong ghi ledger.
- Stream-mode (io.Pipe): bọc `io.TeeReader` trước `pw` — hash tính miễn phí trên luồng tuần tự.
- Nguồn không phải TG (ytdlp, aria2, directlinks): chỉ có lớp hash — vẫn cover.

**Ledger hiện có**: `telegram_saved_files (storage_name, storage_path)` unique. Mở rộng bằng bảng mới `saved_files_index`: `file_unique_id TEXT, content_hash CHAR(64), size, user_id?, storage_name, storage_path, created_at` + index theo hash và theo file_unique_id. Bảng cũ giữ nguyên (conflict theo tên vẫn cần).

## 5. Câu hỏi thiết kế chưa đóng (đưa sang brainstorm)

1. **Phạm vi dedup theo user hay global?** Bot nhiều user — hash của user A có nên chặn user B không? (Fork này 1 user thực tế nhưng code upstream multi-user.)
2. **Skip theo storage hay theo nội dung?** "Đã lưu file X vào TG Hehe" — khi save lại kênh khác mà trỏ tới TG Zyro thì: skip (đã có nội dung đâu đó) hay vẫn lưu (mỗi storage một bản)?
3. **Lớp 0 (resend-by-id TG→TG) có làm không?** Tiết kiệm bandwidth cực lớn cho use case mirror kênh, nhưng đổi khác: caption/attributes mang nguyên bản, không có thumbnail tự tạo, không qua cache. Tách PR riêng?
4. **Skip do dedup hiện thị thế nào?** Đóng góp vào "Skipped" như hiện tại hay mục mới "Deduped: N" để phân biệt với skip-tên-trùng?
5. **Chính sách khi cùng 1 batch có 2 tin trùng nội dung** (khác msg-id): skip bản thứ 2 ngay từ đầu (không tải) hay tải rồi mới biết trùng? (Lớp file-id check được pre-download chỉ khi cùng underlying file; 2 upload độc lập phải tải mới biết.)
6. **Hash có ghi cho file đã lưu TRƯỚC khi có tính năng** (backfill từ file vật lý local; TG storage thì không đọc lại được — chấp nhận lịch sử mù)?

## Nguồn

- [Bot API — File object (file_unique_id)](https://core.telegram.org/bots/api/types/file.md), [Document object](https://core.telegram.org/bots/api/types/document.md), [Sending files / file_id semantics](https://core.telegram.org/bots/api/)
- [MTProto — Uploading and Downloading Files (resend by inputMediaDocument)](https://core.telegram.org/api/files)
- gotd v0.149.0 — `tg/tl_document_gen.go` (Document.ID int64), local source
- [cespare/xxhash benchmarks](https://github.com/cespare/xxhash), [zeebo/blake3 benchmarks](https://github.com/zeebo/blake3/blob/master/README.md), [kelindar/hashbench](https://github.com/kelindar/hashbench)
- Codebase: `pkg/tfile/tgfile.go`, `storage/storage.go:25` (Save interface), `database/telegram_saved.go`, các `core/tasks/*/execute.go`
