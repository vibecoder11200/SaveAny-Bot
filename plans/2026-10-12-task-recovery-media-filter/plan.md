# Plan: Task Recovery (#205) + Media Filter (#217) — SaveAny-Bot fork

Ngày: 2026-10-12 · Base: `main @ 2ee5b39` (fork v0.63.0 + 3 fix riêng) · Chế độ: `:hard`
Red-team chính thức 4 lens: 2026-10-12 — 23 findings thô → 14 duy nhất, đã áp dụng toàn bộ (xem `## Red Team Review`).

## Hợp đồng (từ brainstorm, đã chốt với user)

**Outcome**
1. Task TG-file (đơn + batch, từ forward//save/watch/link) sống sót qua restart: tự phục hồi khi khởi động + 1 tin tổng kết mỗi user.
2. Lọc media theo loại cho watch + /save + link-batch (không lọc forward tay), default per-user lưu DB, đặt qua /config.
3. Download "bò" do flood-wait bị cắt trong 15 phút với lỗi phân biệt được (watchdog v2).

**Constraints**: journal mỏng SQLite — giữ nguyên pkg/queue in-memory làm executor; recovery re-fetch theo (chatID,msgID) bằng **đúng client đã tạo task** để lấy file_reference tươi, không bao giờ persist location; thiết kế sạch để PR upstream (i18n zh+en, docs song ngữ, conventional commits, **2 PR nhỏ** thay vì 1 PR lớn); single-process; **idempotency của recovery = SkipIfExists flag ghi đè conflict strategy** (default rename KHÔNG phải idempotency — red-team #5).

**Non-goals**: checkpoint part-download; journal ytdlp/aria2/transfer/telegraph; lọc forward thủ công; web UI; thay thế pkg/queue bằng DB-backed queue. Task tạo qua HTTP API: **tự động được phủ** bởi choke point `core.AddTask` (red-team #2) — không cần loại trừ.

**Acceptance**
- `docker kill` **hoặc Ctrl+C** giữa batch đang tải → restart → task tự re-enqueue (idempotent qua nhiều lần restart liên tiếp), tin tổng kết "N task / M file (K file nguồn đã xóa)", file đã lưu xong không tải lại (**kể cả user default conflict=rename — không sinh `file(1).ext`**), album giữ nguyên nhóm.
- `/cancel` task đang queued/running → restart → task **không** hồi sinh.
- `/save -100x 1-500 t:video`, `/watch chat mediatype:photo,audio`, link-batch áp user default; filter rỗng = nhận tất cả (backward-compat 100%); `t:` sai cú pháp → báo lỗi, không chạy (fail-closed); forward tay không bị lọc.
- Download bị flood-wait "bò" → bị cắt trong window 15 phút với lỗi "download crawling: flood-wait absorbed ..."; chết im tuyệt đối → lỗi stall v1 như cũ; download khỏe không bao giờ bị cắt oan.
- `go build ./...` + `go vet ./...` + `go test ./...` xanh (trừ 4 test fail sẵn đã biết trên Windows).

## Kiến trúc

```
┌─ Executor (giữ nguyên) ─────────────────────────────┐
│ core.AddTask(task)  ← CHOKE POINT journal.Record    │
│  ├ nếu task là TG-file: Record(upsert) TRƯỚC,       │
│  │  enqueue lỗi → Complete bù                        │
│  └ core.worker → journal.Complete (trừ nhánh        │
│     shutdown: root ctx chết → GIỮ row)              │
│ core.CancelTask → Complete (task queued bị discard  │
│  ngoài worker — không để row "hồi sinh")            │
└─────────────────────────────────────────────────────┘
               ▼
┌─ SQLite data/saveany.db ──┐   ┌─ Startup recovery ──────────────┐
│ PendingTask(id,user,      │   │ cmd/run.go → recovery.Run()      │
│   payload,attempts,ts)    │──▶│  drop row >48h → group theo      │
│ User.media_filter (cột)   │   │  (user × client ViaUserbot) →   │
│ WatchChat.media_type(cột) │   │  GetMessagesByIDs (chunk 100) → │
└───────────────────────────┘   │  NewTask(**row.ID reuse**) →    │
                                │  SkipIfExists flag → summary    │
                                └──────────────────────────────────┘
Download paths: StallWatchdog v1 (giữ nguyên) + FloodWaitMeter v2
(eofAwareClient đo thời gian RPC; ≥7.5ph chờ/15ph → cancel)
```

Payload JSON (tính tại lúc tạo task — đích đã resolve + tên đã sanitize, không re-eval rules):
```json
{"files":[{"chat_id":-100...,"msg_id":521,"name":"","storage":"TG Zyro","path":"a.mp4","via_userbot":false}],
 "user_id":636...,"as_batch":true,"source":"save"}
```

Media classification (thuần, không tốn RPC): `photo` = MessageMediaPhoto; document Attributes: Sticker→`sticker`, Animated→`animation`, Voice→`voice`, Audio→`audio`, Video→`video` (kể cả round); còn lại `document`. Alias: `gif`→animation, `music`→audio.

## Phases

| # | Phase | Nội dung | Phụ thuộc |
|---|---|---|---|
| 1 | [Phân loại media](phase-01-media-classification.md) | MediaType + MediaFilter parse/Allows + IsMediaFilterSpec + tests — **DONE 2026-10-12 (62d3049)** | — |
| 2 | [Tích hợp filter](phase-02-filter-integration.md) | t: trong /save (grammar 2-arg), cột WatchChat.MediaType, link-batch default, User.media_filter + /config flip-on-tap, i18n — **DONE (a1662eb + 535aaec)** | 1 |
| 3 | [Journal task](phase-03-task-journal.md) | Model PendingTask + pkg/journal + **choke point core.AddTask** + ma trận cancel + SQLite DSN pragma + sanitize tên file + config flag — **DONE (6518565)** | — |
| 4 | [Phục hồi lúc khởi động](phase-04-startup-recovery.md) | recovery.Run() + GetMessagesByIDs + client routing ViaUserbot + row-ID reuse + SkipIfExists + drop theo tuổi — **DONE (28b2512)** | 3 |
| 5 | [Docs + PR + Deploy](phase-05-docs-pr-deploy.md) | Docs zh+en, example config, fork commits + **2 PR upstream** (PR upstream CHỜ user duyệt), ship prod — **DONE phần fork+deploy 2026-10-12 (d03c668, image 0.63.0-fork chạy prod, WAL + pending_tasks xác nhận)** | 1–4 (+6 nếu xong sớm) |
| 6 | [Watchdog v2 — flood-wait meter](phase-06-watchdog-v2.md) | FloodWaitMeter đo thời gian RPC chậm (≥2ph/RPC, ≥50% window 15ph) + giữ v1 — **DONE (1b902ad)** | — (độc lập) |

Commit thứ tự thực tế (mỗi commit build được độc lập): 62d3049 → a1662eb → 535aaec → 1b902ad (meter trước journal, theo ghi chú ownership tfile/execute.go) → 6518565 → 28b2512 → d03c668.
Code-review 2026-10-13 (code-reviewer agent, verdict safe-deployed, không critical/high): sửa M1 (Ctrl+C giữa recovery mất row — queue.ErrQueueClosed skip compensation), M2 (`t:`/`mediatype:` rỗng giờ fail-closed), L1/L2/L3/L5 → 66ca50a + a87bcd6 + b7c7c4a, đã push fork. Defer: L4 (Windows reserved device names), L6 (boot message volume — theo design). Báo cáo: `plans/reports/code-reviewer-261013-0149-task-recovery-media-filter.md`.
Lệch thiết kế đã ghi nhận: helper per-user filter đặt ở `mediautil.UserMediaFilter` (không phải method `User.MediaFilterSet`) vì mediautil đã import database — method trong database sẽ tạo import cycle.

Ghi chú phạm vi (2026-10-12): phase 6 thêm sau khi prod ghi nhận file 90MB "bò" 66 phút do chuỗi FLOOD_WAIT per-chunk. Thiết kế byte-floor đầu tiên bị red-team bác bỏ (sai toán 4-5 lần vì đếm thread song song) — thay bằng flood-wait meter. Phase 1–2 và 3–4 là 2 luồng độc lập, phase 6 luồng thứ ba — triển khai song song được.

## Red Team Review (2026-10-12 — 4 lens, workflow ak-plan)

Reviewers: Security Adversary, Scope & Complexity Critic, Failure Mode Analyst, Assumption Destroyer (code-reviewer agents, song song). 23 findings thô → khử trùng lặp → 14; tất cả qua evidence filter (file:line); **chấp nhận 14/14** (3 fix được chỉnh, 1 hạ severity). User không duyệt từng finding (autonomous) — mọi disposition ghi ở đây để có thể phủ quyết lại được.

| # | Mức | Finding (lens) | Disposition → áp vào |
|---|---|---|---|
| 1 | CRITICAL | Byte-floor 4MiB/15ph sai toán 4-5 lần: 4 thread × 1MiB/part → crawl ≥ floor; sự cố thật 20MiB/15ph = 5× floor → không bao giờ bắn; link chậm thật không phân biệt được bằng byte (Failure+Assumptions độc lập) | ✅ Accept — bỏ byte floor; flood-wait meter đo thời gian RPC; giữ v1 → phase-06 |
| 2 | CRITICAL | "3 creation sites" thực là 5 (thiếu watch.go:425 album + api/factory.go:75); recovery bypass hooks → crash lần 2 mất task (Scope+Assumptions+Security+Failure) | ✅ Accept — choke point core.AddTask + recovery reuse row.ID → phase-03/04 |
| 3 | CRITICAL | Ma trận cancel sai 2 chiều: Ctrl+C → row bị xóa (mất dữ liệu recovery); user cancel-queued → row sống (task hồi sinh) (Failure+Assumptions) | ✅ Accept — worker skip Complete khi root ctx chết; hook core.CancelTask → phase-03 |
| 4 | HIGH | Recovery trong flood penalty đốt task (fail-fast + IgnoreErrors + row bị xóa); attempts đếm boots → restart loop 3 lần là mất vĩnh viễn; fetch-error lẫn message-deleted (Failure+Security) | ✅ Accept (modified) — phân biệt deleted/infra; drop theo tuổi 48h, attempts chỉ để log → phase-04 |
| 5 | HIGH | Conflict default = RENAME → recovery tải lại 200 file thành file(1).ext; Exists-skip mỗi file lúc boot = HTTP storm alist (Failure+Scope) | ✅ Accept — SkipIfExists flag trong Execute ghi đè strategy, bỏ boot-time Exists → phase-04 |
| 6 | HIGH | GetMessagesRange nổ đầy range (2 msg rải rác = 299 RPC) — recovery tự thành nguồn flood (Assumptions) | ✅ Accept — helper GetMessagesByIDs → phase-04 |
| 7 | HIGH | Payload không ghi client tạo file; "ưu tiên userbot" fail với PM chat user ≠ owner (Assumptions) | ✅ Accept — TGFileRef.ViaUserbot + fetch đúng client + bucket ClientUnavailable → phase-03/04 |
| 8 | HIGH→MED | Path traversal qua file.Name() vào cache path; journal persist + replay (Security) | ✅ Accept (hạ severity theo threat model đa-user/API) — SanitizeFileName choke point → phase-03 |
| 9 | HIGH | 1 PR upstream gộp 2 tính năng + schema column + schema table + core change = quá intrusive (Scope) | ✅ Accept — fork làm đủ; upstream: PR A filter inline (zero schema) + PR B recovery; DB/UI phần để sau → phase-05 |
| 10 | MEDIUM | Filter fail-open (parse lỗi = cho qua tất cả); callback không validate token (Security) | ✅ Accept — fail-closed ở mọi điểm nhập (t: lệnh / watch tạo / callback); giá trị lưu đã validate → WARN nil chỉ cho dữ liệu hỏng → phase-01/02 |
| 11 | MEDIUM | SQLite không WAL/busy_timeout; ncruces chỉ nhận pragma qua DSN; Record-sau-AddTask → orphan row immortal (Failure+Assumptions) | ✅ Accept — DSN pragma + SetMaxOpenConns(1); Record-trước-enqueue trong choke point → phase-03 |
| 12 | MEDIUM | UI 9 nút toggle + nút Save phát minh vocabulary mới (Scope) | ✅ Accept — flip-on-tap stateless đúng pattern fnamest/conflictst → phase-02 |
| 13 | LOW | WatchChat.Filter 1 slot `type:expr` chứa không nổi 2 filter (Security+Scope) | ✅ Accept (modified) — cột WatchChat.MediaType riêng thay vì overload chuỗi → phase-02 |
| 14 | LOW | Grammar t: phá workflow regex cũ (`t:` literal); args[3] không khai báo (Failure) | ✅ Accept — prefix t: = luôn filter (fail-closed); `^t:` vẫn regex; grammar 2-arg ghi rõ help+docs → phase-01/02/05 |

Câu hỏi mở của reviewer đã chốt: ngưỡng "link chậm hợp lệ" — theo kỳ vọng user đã chốt (23KB/s là kẹt; "bò dưới ~4,5KB/s cũng là kẹt"), meter ngưỡng 2 phút/RPC là biên lành minh (link 4-thread cần >~35KB/s tổng).

### Whole-Plan Consistency Sweep (sau khi áp findings)

- Kiến trúc + payload JSON ở plan.md này: đã cập nhật theo #2/#7 (choke point, row-ID reuse, via_userbot, meter).
- Constraint "conflict strategy là cơ chế idempotency" (cũ, sai theo #5) → thay bằng SkipIfExists flag.
- Phase table: hàng 3 "hook 3 creation sites" → "choke point core.AddTask"; hàng 6 "sàn throughput ≥4MB/15 phút" → "flood-wait meter".
- File ownership validation gate: cập nhật theo ownership mới (phase 3 thêm core/tasks struct files + database/driver.go + pkg/tfile sanitize; phase 4 thêm tgutil/message.go + prelude execute.go; phase 6 thêm common/tdler/dler.go + dlutil/floodwait_meter.go). Điểm chung duy nhất: `tfile/execute.go` (phase 6 meter + phase 4 prelude SkipIfExists — khác vùng code, phase 6 land trước) và `database/model.go` (P2 cột User + cột WatchChat; P3 bảng PendingTask — khác struct).
- Mục "Red-team gate (self-review)" cũ: các claim bị findings bác ("3 creation sites phủ mọi task ✓", "conflict strategy = idempotency", "quá 3 lần drop", byte-floor) đã bị thay thế ở mọi chỗ xuất hiện — không còn stale.
- Phase-05 commit list: cập nhật thành 7 commit fork + chiến lược 2 PR upstream (PR A = commit 1+2).

## Validation gate

- Mỗi phase: `gofmt -w` các file chạm + `go build ./...` + `go vet ./...` + `go test ./<các pkg chạm>/...` phải xanh trước khi sang phase sau.
- File ownership: phase 1–2 không đụng `database/model.go PendingTask|pkg/journal|core/`; phase 3–4 không đụng `mediautil filter|watch filter|save.go filter|config.go`; phase 6 chỉ đụng `dlutil/stall*+floodwait_meter*` + `tdler/dler.go` + 3 file download paths (không đổi writer callback của v1). Giao: `tfile/execute.go` (P6 meter trước, P4 prelude sau), `database/model.go` (P2/P3 khác struct).
- End-to-end (phase 5): runbook verify trên prod như acceptance ở trên, gồm cả kịch bản Ctrl+C và `/cancel` không hồi sinh.

## Next

`/ak:cook` theo phase; phase 5 kết thúc bằng build image + ship prod (lặp runbook 2026-10-12 đã chạy thành công hôm nay).
