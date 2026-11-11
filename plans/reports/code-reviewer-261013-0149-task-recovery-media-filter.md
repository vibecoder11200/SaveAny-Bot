# Code Review — Task Recovery + Media Filter + Flood-Wait Meter

- Range reviewed: `2ee5b39..d03c668` (7 commits, 44 files, +2363/−70)
- Reviewer: code-reviewer agent (2026-10-13) · Verdict: **Safe to keep deployed on prod** — no critical/high findings
- Reviewer verification: `go build`, `go vet`, `gofmt -l` clean; `go test -race` on journal/dlutil/core/tfile/mediautil green; full suite only has the 3 pre-existing `storage/telegram` fixture failures.

## Fixed after review (66ca50a, a87bcd6, b7c7c4a)

| # | Mức | Finding | Fix |
|---|---|---|---|
| M1 | Medium | Ctrl+C giữa lúc startup recovery đang re-enqueue → AddTask compensation xóa row đang phục hồi (mất task đúng lúc shutdown phải giữ) | `queue.Add` trả `ErrQueueClosed`; choke point bỏ qua compensation cho sentinel này; recovery map sang keep-for-next-boot; test đóng queue assert row còn (`core/core_test.go`) |
| M2 | Medium | `/save chat 1-500 t:` (hoặc `t:,,,`) parse ra nil filter → âm thầm áp user default, trái hợp đồng fail-closed | Nil filter sau parse `t:` → báo lỗi, không chạy task. Tương tự `/watch mediatype:,,` → từ chối lúc tạo |
| L1 | Low | Batch skip+fail hỗn hợp báo sai "all elements failed" | Message đếm cả skipped |
| L2 | Low | Summary per-user thiếu deletedSources của row bị abort do infra | Cộng `rowDeleted` vào summary lúc abort |
| L3 | Low | Test margin window meter mỏng (~20ms) → flake trên CI tải cao | Sleep `2*testWindow` trước Observe thứ hai |
| L5 | Low | DSN có sẵn `?` bị bỏ qua pragma | Append bằng `&` (guard `_pragma=`) |

## Đã đánh giá là không cần sửa (deferred)

- L4: `SanitizeFileName` chưa xử lý Windows reserved device names (CON, NUL, COM1-9…) — prod chạy Linux/docker; chỉ ảnh hưởng local storage trên Windows host. Backlog nếu cần.
- L6: Boot recovery gửi 1 message mỗi task + 1 summary — theo design plan; SendMessage fail được xử lý degrade-to-nil.
- Upstream PR A (filter inline) / PR B (recovery): backlog chờ user duyệt, chưa tạo gì.

## Điểm reviewer xác nhận non-issue (trích)

- Record-before-enqueue không race với worker Complete/CancelTask (single sqlite conn + thứ tự ghi); double-delete idempotent theo PK.
- Meter: atomics `-race` sạch; vòng đời dlCtx con của watchdog ctx đúng ở cả 3 download path; `NewDownloader` nil-meter giữ behavior cũ byte-for-byte.
- Row-ID reuse + upsert-keep-attempts idempotent qua crash lặp; SkipIfExists chỉ recovery set (grep xác nhận).
- Phân loại deleted vs infra trong `GetMessagesByIDs` khớp semantics của `GetMessageByID` có sẵn.
- Sanitize chặn đủ 4 điểm đặt tên (kể cả replay từ journal payload).
- `^t:` regex legacy vẫn là regex; nil filter = allow-all backward-compat 100%.
