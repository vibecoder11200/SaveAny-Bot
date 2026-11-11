# MERGE PROPOSAL — v1 (upstream rebuild) → main (v0 line)

**Status: READY FOR USER VALIDATION — merge is USER-GATED.**
`main` stays the v0 production line until the user runs the production
migration, validates, and says merge.

## What this branch is

A rebuild of the fork on upstream SaveAny-Bot v1.0.0-beta.5 (`upstream/main`),
branch `v1`, 9 commits, **180 files changed, +13,393 / −454 lines**
(`git diff upstream/main..v1`). Upstream wins overlaps; all fork feature
groups were ported on top; 26 red-team plan findings applied during the
build.

## Feature inventory (all verified green)

| Group | What landed | Tests |
|-------|-------------|-------|
| Storage & task foundations | storage IDs replace names (DB row per storage, `MigrateMinioStorageType`), task records + native resume, resend-by-file-id, SkipLarge + forum-topic routing, `DedupUserIDForChat` | unit + e2e |
| 3-layer dedup | file-ID → content-hash → re-save ledger, fail-open, per (user, storage), API tasks dedup-off (userID=0); `/history` (list/search/stats), `/purge <storage>` | unit (dedup/history/purge) |
| Download guards | StallWatchdog (15 min), FloodWaitMeter (15-min window, cancels at half-window), DownloadFailure mapping, progress-edit flood backoff + final-edit survival | unit incl. cancel/resume race |
| Ops | `/status` (owner-gated, `.part`/`.bitmap` resume size, per-filesystem disk free), `/id` (userbot-preferring), queue positions in `/tasks` | build + manual |
| Media filter | per-user default (`/config`), `/save t:` override, per-watch `mediatype:`; precedence = instance gate → first non-nil scope (override semantics, matching the plan's matrix); fine tokens | unit matrix (10 cases) |
| Watch extensions | shared live/backfill intake, per-watch storage/dir/msgre/mediatype overrides (storage stored as ID, lost-storage fallback + owner notice), album = ONE batch task (native group keys on telegram storage), cursors (live advances pre-filter; backfill advances only on durable enqueue), boot catch-up (500-cap/100-page/10-min budget, first-boot init, FlushChat-before-summary, exactly-once), partial unique index replacing upstream's full index | unit (parse/resolve/catch-up/cursor) + e2e TestWatchCatchUp |
| v0→v1 importer | `--fork-db` (+env), boot refusal guard before `database.Init`, single-tx import, column probing for mid-fork schemas, hollow-import refusal, pending-task count warning, read-only source (byte-compare test) | 6 fixture tests |
| Docker | `Dockerfile.micro` (alpine+curl, non-root), `Dockerfile.pico` (scratch+glebarez), no-CI (locked), config-time ffmpeg/yt-dlp absence warnings | tag-combo builds |

## Known regressions vs the fork (locked decision: upstream native resume wins)

1. **Completed-download resume state is wiped at boot when no tasks are
   queued** (the fork spared it 7 days). v1's boot cache-clean decides on the
   empty queue.
2. **A failed task's partial download is not reusable across tasks** — v1
   keys resume state by taskID, a retry creates a new task and re-downloads
   from scratch (the fork reused `.part` state across tasks).

Both accepted; if they hurt in production, the fix is a follow-up on v1's
resume model, not a fork revert.

## Known gaps / notes

- Web API-created tasks: dedup off by design (userID=0).
- Stream mode: layer-1 file-id dedup check + record-after-upload only; the
  hash layer is N/A by design (bytes stream through).
- `AddTaskWithSource` keeps the task record on `ErrQueueClosed` (restart
  recovery) — a deliberate fork divergence from upstream; documented at the
  call site, re-check on upstream merges.
- CI Docker image builds: out of scope (no Docker on the dev box); possible
  post-validation follow-up.
- Windows e2e: process-signal restart tests skip on Windows (Go exposes no
  cross-process Ctrl+C API — same platform limit upstream's SIGTERM test
  already skipped); `sqlite_glebarez` tag required for ALL local Windows
  test runs (ncruces WASM driver cannot open absolute Windows paths —
  pre-existing upstream defect).
- gofmt flags `settings/legacy.go` (pre-existing upstream artifact, file
  untouched by the fork).

## Migration readiness

Production migration guide: `docs/fork/migration-v0-to-v1.md` — fresh data
dir + `--config` (mandatory) + `--fork-db <copy>`, queue-drain step, boot-log
expectations, verification checklist, rollback (v0 dir untouched by
construction; upstream-downgrade SQL snippet). The boot guard refuses any
attempt to point v1 at the v0 data dir.

## User validation checklist (before merging)

1. Dry-run the migration on a COPY of production data (guide §4); eyeball
   the import summary counts against `/history stats`.
2. Verify watches survived with cursors; restart once, confirm silence
   (fetched=0) and one live event still saves.
3. One test save + repeat (dedup skip), one album to telegram storage
   (real album send), one watch album to non-telegram storage (subfolder).
4. MinIO via s3 config (`virtual_host: false`) — test-connection, save,
   object visible in bucket.
5. `/status` renders; `/id @user` resolves; web console login + task
   lifecycle works.
6. Only after 1–5 pass: `git merge v1` into `main` (fast-forward or merge
   commit per user preference), tag a fork release, push binaries to the
   fork repo for selfupdate.

## Post-merge follow-ups (optional, none blocking)

- CI workflow for image builds.
- Revisit the two resume regressions on v1's resume model.
- Prune upstream's unused i18n keys upstream-side (33 orphans exist upstream
  too; left alone to keep merges clean).
