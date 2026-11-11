# Phase 5 — Docs song ngữ + chuẩn bị PR upstream + deploy prod

Sở hữu file: `docs/content/{en,zh}/usage/*.md` *(sửa save.md, watch.md; thêm task-recovery.md nếu cần trang riêng — ưu tiên gom vào trang hiện có)*, `config.example.toml`, commit history.

## Bước thực hiện

1. **Docs**:
   - `save.md`: mục "Filters" — thêm cú pháp `t:video,photo` + bảng 7 token + alias, ví dụ kết hợp regex caption.
   - `watch.md`: filter types thêm `mediatype:` với ví dụ.
   - Task recovery: 1 đoạn ngắn trong trang config/docs deployment — "restart an toàn: task tự phục hồi, tắt bằng `[task] recovery = false`".
   - Cả en lẫn zh đồng bộ nội dung.
2. **config.example.toml**: thêm section `[task]` với `recovery = true` + comment song ngữ theo style file.
3. **Tách commit (fork) + chiến lược PR upstream (red-team #9 — maintainer thích PR nhỏ, 1 PR gộp 2 tính năng + schema column + schema table + core change = quá intrusive)**:
   - Commit trong fork (mỗi commit chạy được độc lập, conventional style):
     1. `feat(media): add media type classification and filter parser` (phase 1)
     2. `feat(bot): filter media by type in /save via t: syntax` (phase 2 — phần t:)
     3. `feat(bot): per-user media filter default, watch mediatype and /config ui` (phase 2 — phần DB/UI)
     4. `feat(core): persist pending TG-file tasks in sqlite journal` (phase 3)
     5. `feat(core): recover pending tasks on startup` (phase 4)
     6. `feat(core): cancel crawling downloads stuck on flood waits` (phase 6)
     7. `docs: media filter and task recovery` (phase 5)
   - **Upstream: 2 PR riêng**, đọc từ các commit trên:
     - **PR A — media filter inline** (commit 1+2+docs phần filter): chỉ `t:` trong /save — zero schema change, dễ duyệt nhất. Giữ lại (chưa gửi): per-user default, /config UI, watch `mediatype:` (cần cột DB), link-batch default — đợi maintainer phản hồi PR A rồi gửi tiếp.
     - **PR B — task recovery** (commit 4+5+docs phần recovery): journal + recovery, maps #205.
     - Watchdog v2 (commit 6) gửi PR riêng thứ 3 hoặc để sau — phụ thuộc maintainer (flood-wait handling là vùng nhạy cảm đã từng đổi gần đây).
   - Lưu ý: các commit fix tuần trước (floodwait/progress/stall) PR riêng — không trộn.
4. **Deploy prod** (runbook đã validate hôm 2026-10-12):
   ```bash
   # local (chú ý entrypoint.sh phải LF trước khi build!)
   sed -i 's/\r$//' entrypoint.sh
   docker build -t saveany-bot:<ver>-fork --build-arg VERSION=<ver>-fork \
     --build-arg GitCommit=$(git rev-parse --short HEAD) \
     --build-arg BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -f Dockerfile .
   docker save saveany-bot:<ver>-fork | gzip | ssh root@160.30.113.48 "gunzip | docker load"
   # server
   cd /opt/saveany-bot && docker compose up -d --force-recreate
   docker exec saveany-bot /app/saveany-bot version   # verify commit
   ```
5. **Verify trên prod** (checklist acceptance):
   - `/save` 1 range nhỏ → `docker compose kill` giữa lúc tải → `docker compose up -d` → log recovery + tin tổng kết + task chạy nốt; **restart liên tiếp 2 lần trong lúc recovery → không duplicate, không mất**.
   - Ctrl+C/dừng compose giữa download → start lại → task phục hồi (row không bị xóa lúc shutdown).
   - `/cancel` task queued → restart → task không hồi sinh.
   - `/save ... t:video` chỉ video; `/watch ... mediatype:photo` chỉ ảnh; `/save ... t:bogus` báo lỗi token.
   - Watchdog: quan sát log 24h — không còn download kéo dài hàng giờ không lỗi; nếu tái diễn crawl → log "download crawling: flood-wait absorbed ...".
   - Quan sát 24h: không log lỗi mới, journal table không phình (row xóa sau task).

## Hoàn tất
- Cập nhật `plans/.../plan.md` đánh dấu phase done.
- Đăng issue/PR upstream khi user duyệt (PR #217 mention; recovery maps #205).
