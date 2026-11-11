# Phase 6 — Test tổng hợp, race, deploy, verify trên prod

## Context Links

- Phases 1-5
- Runbook deploy đã chuẩn hóa: `sed -i 's/\r$//' entrypoint.sh` → `docker build -t saveany-bot:0.63.0-fork .` → `docker save -o tar` → scp → `docker load -i` → `cd /opt/saveany-bot && docker compose up -d --force-recreate` → verify log
- Server: root@160.30.113.48, key `C:\Users\ankha\.ssh\anvps-megahost`, DB `/opt/saveany-bot/data/saveany.db`
- 2 test storage/telegram pre-existing fail vì fixture Windows — đã biết, không phải do dedup

## Overview

- **Priority**: P1
- **Status**: Pending
- Gate chất lượng + ship + kịch bản verify thực tế 3 lớp trên prod.

## Key Insights

- Deploy lần này restart container — recovery + startup cache-clean đã được chứng minh ổn (3 lần trước).
- Bảng `saved_file_dedup` tạo lúc boot bằng AutoMigrate — verify bằng sqlite_master sau deploy.
- Kịch bản verify dùng đúng channel @phimsexvietnama (đã có 167 file trong ledger theo tên) + range 687-1000 (17 failed cũ chờ re-run).

## Requirements

- `go build ./...` xanh; `go vet` các package đụng; `go test -race` xanh cho: `database`, `pkg/tfile`, `common/utils/fsutil`, `core/tasks/batchtfile`, `storage/telegram` (trừ 2 test fixture fail đã biết).
- **[RT-14d] Thêm `plans/` vào `.gitignore` TRƯỚC khi commit** — plan chứa IP server + đường dẫn SSH key, fork là public repo.
- Commit tách theo phase theo conventional commits; push fork (gh credential helper vibecoder11200).
- KHÔNG `git add -A` (CRLF phantom) — add từng file.

## Kịch bản verify prod (lần lượt)

1. **Migration**: sau restart, `sqlite_master` có `saved_file_dedup` + 3 index; bot log sạch, không task pending.
2. **Lớp 0 resend**: user chạy `/save` 1 tin video TG mới vào TG storage → log `resend ok`, không "Starting file download", không cache file, message vào chat trong ~giây. **Kiểm tra riêng: tin từ kênh chỉ userbot xem được (RT-2) cũng resend được — nếu không, kiểm tra userbot có quyền gửi vào chat storage (add admin nếu thiếu).**
3. **Lớp 1 file-id**: chạy lại đúng lệnh vừa chạy → toàn bộ file vừa lưu log `dedup skip (file-id` NGAY từ lúc tạo task (không element, không tải, không RPC refresh), done message `Deduped: N` đúng số.
4. **Lớp 2 hash**: lưu 2 tin khác nhau cùng nội dung khác tên (TG) → tin 2 log `dedup skip (hash` sau khi tải, không upload.
5. **Không regression**: 1 file hoàn toàn mới → tải + upload bình thường qua đường cũ (resend chỉ áp TG→TG); local storage vẫn nhận file TG mới (fid chỉ chặn khi cùng storage đã có).
6. **Re-run 687-1000**: ~17 failed cũ được thử lại; các file đã lưu bị chặn bởi file-id/hash — **đếm message mới thực tế trong TG Hehe** (không chỉ đọc log — at-least-once semantics, RT-14c).

## Hạn chế phải ghi trong journal + nhắc user (RT-14e)

- File lưu TRƯỚC dedup không có hash/fid → 1 lần gặp lại để học.
- Resend không có hash; stream-mode bỏ hash; UserID=0 (API) không dedup.
- **Dedup ledger không tự dọn khi user tự xóa message khỏi chat storage** — re-run sẽ không lưu lại (dedup chặn). Cần purge command (follow-up); trước mắt user xóa message thì phải chủ động báo tôi purge rows.

## Implementation Steps

1. Chạy gate local (build/vet/test -race); sửa tới xanh.
2. Commit từng phase (nếu chưa commit theo phase khi cook) + push fork.
3. Build image + deploy theo runbook.
4. Verify 1 → 6; theo dõi log live trong ~15 phút đầu.
5. Ghi journal (ak:journal): kết quả, hạn chế (file cũ không hash; resend không hash; stream-mode bỏ hash), số liệu.

## Todo List

- [ ] Gate local xanh
- [ ] Commit + push
- [ ] Deploy + verify migration
- [ ] Verify 3 lớp theo kịch bản
- [ ] Re-run 687-1000 + đếm kết quả
- [ ] Journal

## Success Criteria

- 6 mục kịch bản pass, đủ bằng chứng log cho từng lớp.
- Không task nào fail vì dedup (fail-open xác nhận qua log WARN nếu DB lỗi).

## Risk Assessment

- **Risk**: resend bị TG rate-hạnh vì gửi nhanh (sendMedia nhanh hơn upload nhiều) — storage limiter (2s/request mặc định) vẫn áp; theo dõi FLOOD_WAIT ở verify; nếu dính, tăng RateLimit config của storage, không cần code.
- **Risk**: kịch bản 4 khó tìm đúng cặp tin — dùng cách tự tạo: forward 1 tin từ kênh khác vào chat test 2 lần với tên file khác qua Telegram client rồi /save cả 2.

## Security Considerations

- Không expose gì mới; verify không dán token/app_hash vào log.

## Next Steps

- Sau verify: nhắc user về hạn chế đã biết (file trước-dedup không hash; cần 1 lần gặp lại để học hash).
