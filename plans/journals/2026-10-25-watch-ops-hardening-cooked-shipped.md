---
title: "Cook: watch hardening + ops commands shipped (v0.66.6→v0.68.0)"
date: 2026-10-25
summary: "ak:cook --auto complete: 3 releases shipped + deployed to prod, catch-up verified live (10 missed files recovered, 0 duplicates), one patch (v0.67.1 cursor-init fix)"
---

# Cook: watch hardening + ops commands shipped (v0.66.6→v0.68.0)

## What happened

Executed `plans/261024-1145-watch-ops-hardening/plan.md` in auto mode. All 5 phases implemented, gated, shipped as 4 releases (3 planned + 1 fix-forward patch), deployed and verified on prod (root@160.30.113.48, /opt/saveany-bot).

### v0.66.6-fork (8d317da) — bugfix batch
- `watch_args.go` flag parser: msgre regex survives `:` and multi-word tails; compiled-regex cache + 512-char cap (S6).
- Help lists exactly the 25 registered commands (ghost /import dropped, missing commands added), both locales.
- GitRepo → `vibecoder11200/SaveAny-Bot`.

### v0.67.0-fork (0532a97) + v0.67.1-fork (0e5c0d1) — watch hardening
- `SanitizeRelDirPath` closes the dir: traversal hole for all storage backends (creation-time + join-time).
- `/watch storage:/dir:` per-watch override; override watches skip the rule layer (`resolveWatchTarget`).
- msgre parity via shared `mediautil.MatchStringForFilter` (caption + " " + raw media filename — byte-identical to /save; /save now uses the helper too).
- Batch core extracted with `BatchOptions` (UISink nil-safe, Override, ForceGrouping, pinned non-interactive strategy, progress override); watched albums ALWAYS one batch task; first-file album folder with -HHMMSS collision suffix; ≤1-member album groups kept (both /save deltas documented in release notes).
- Catch-up: per-watch cursor (`watch_chats.last_message_id`, guarded TouchWatchCursor never lowers); first-boot init to newest (NO backfill, owner decision 10); forward paging on real IDs only, cap 500/boot, 10-min budget, 1 req/s pacing, no manual FLOOD_WAIT wrapper; backfill pins rename strategy; one "enqueued" summary per watch after album force-flush, silent when nothing new.
- **v0.67.1 fix**: cursor init used the paged fetch form (OffsetID 0 + AddOffset -100) which returned no usable messages — init never recorded a cursor. Canonical newest call (Limit 1, no shift) fixes it; found via prod log (silent init) + DB (NULL cursor).

### v0.68.0-fork (ac552ed) — ops
- `/status` gated to first whitelisted user (S9): version/commit/docker, uptime (`core.StartTime`), workers, queued/running, watch count, cache+resume sizes (`fsutil.DirSize` single-flight + 60s TTL), disk free for cache + local roots (`DiskFree`, statfs/GetDiskFreeSpaceEx), DB size.
- `SavedFileDedup.ChatID/MessageID` (ADD COLUMN, legacy rows NULL→unknown); 4 TG record sites pass source via `WithSource` recorders + `tgutil.SourceFromTGFile`.
- `/history [page] | search <text> [page] | stats` — 20/page, escaped LIKE, per-storage + 14-day aggregations, /purge footer documented (F8).
- `/task queued` positions; commands appended at END of CommandHandlers.

## Prod verification (evidence)
- All 3 versions print parseable semver (`0.66.6-fork`/`0.67.x`/`0.68.0-fork` — no leading v, F5/A6), clean boots, AutoMigrate OK with pre-deploy DB backups each release.
- Catch-up live test (cursor lowered 10000→9985, restart): recovered 10 media files missed during earlier restarts, dedup ledger +10 rows all first-time file-ids (0 previously-seen → no duplicates), cursor advanced to last enqueued (9998; 9999/10000 non-media), summary sent. Second restart: silent, no fetch loop.
- `ghselfupdate.DetectLatest` on linux/amd64 resolves v0.67.0-fork + binary asset → /update works against the fork; Docker builds report-only.
- Schema on prod: watch_chats has storage_name/dir_path/last_message_id; saved_file_dedups has chat_id/message_id; 236 legacy rows intact.

## Not verified (needs owner in Telegram)
- `/help` rendering, `/watch storage:X dir:Y` end-to-end save, album folder naming/collision on a real watched album, `/status`/`/history`/`/task queued` replies. All covered by unit tests except the live UI rendering; owner should spot-check per phase-06 checklists.

## Deviations from plan
- Intake lives in `client/bot/handlers/watch.go` (not client/user) — the catch-up engine feeds handler-side album buffers; avoids an import cycle.
- Cursor-init bug found and patched as v0.67.1 (plan's fix-forward v<X>.1 path).
- One `git checkout -- .` mid-session briefly reverted unstaged Phase-4 wiring; re-applied and amended before any release — final commits verified complete.

## Full gate (each release)
`go generate`(geni18n) clean · `gofmt` clean (touched dirs) · `go vet ./...` clean · `go test -race ./...` green except the 3 documented fixture skips (TestCreateSplitZip/TestExtractThumbFrame/TestGetVideoMetadata — missing tests/ assets) · `GOOS=linux go build ./...` OK.

## Post-ship review cycle (2026-10-25, same day)
- **code-reviewer** on 45375da..ac552ed: 0 Critical / 1 High / 4 Medium / 6 Low. Verdict FIX-BEFORE-RELEASE on H1+M1 → fixed, gated, shipped as **v0.68.1-fork** (b5be423), deployed + verified on prod.
  - **H1 live×catch-up double intake** (release blocker): backfill pinned MinID to a boot-time cursor snapshot, so messages arriving live during the backfill were processed twice; with prod's `workers=2` + `media_group_timeout=10s > 5s debounce`, both copies could land in the SAME album buffer cycle and flush as one batch task containing the file twice — un-dedupable (ledger row exists only after first upload). Fixes: album buffer rejects duplicate message IDs per (chat,user); backfill re-reads the durable cursor per message and skips ≤ it (live advance is pre-filter → skip is lossless). Residual: F6's documented in-flight check-then-record window for separate tasks remains accepted.
  - **M1** summary wording was inverted (cap defers NEWEST, auto-refetched next boot — text told the owner to /save the already-saved range) — both locales corrected.
  - **M2** summary sent via shared bot context → per-call copy (ext.Context concurrency invariant).
  - **M3 (scoped)** backfill advances cursor over evaluated-no-task messages (filtered types/regex/text) only when every fed album file durably enqueued — fixes the ≥500-non-matching livelock while keeping failed album flushes re-fetchable.
  - **L3** batch core downgrades ASK→rename for unattended callers (TrackMsgID 0, no UISink).
  - Deferred (reviewer fast-follow/backlog): M4 album notify dropped skipped/deduped lists, L1 search grammar page-suffix heuristic, L2 sameDiskFree heuristic (Linux cosmetic), L5 unique index on (user_id, chat_id), L6 test gaps (FlushChat interleave, batchWatchNotifyProgress, intake cursor semantics, /history grammar).
- New tests: TestWatchMediaGroupBufferDedupsMessageIDs, TestCurrentWatchCursor. Prod now on 0.68.1-fork, clean boot.
- **code-simplifier** pass on the session files: net −19 lines across 6 files, all behavior-preserving (verified by re-reading every hunk + full gate from the main session): shared `filterAndAppend` for the three batch filter chains (flat/album semantics can no longer drift), dead override branch + dead nil-guard + unused `maxWatchDirLen` removed, mediatype branches collapsed, shared `historyPageArg`/`addRoot` helpers. Committed as 29f7fa1 and pushed — no deploy needed (behavior-identical; rides the next release).

> Historical work record — not durable authority. Prefer docs/specs/ADRs for current decisions.

## Review-backlog patch v0.68.2-fork (2026-10-25, owner-approved Option A)
Owner decision: fold M4 + L1 + L2 into one small patch, include L6 tests, leave L5 (unique index user_id,chat_id) deferred. Shipped as **v0.68.2-fork** (3861d08).

- **M4 album notify parity**: `BatchOptions.Progress` → `ProgressFactory func(skipped, deduped []string) ProgressTracker`, invoked AFTER the batch partition — the watch album caller now passes the real creation-time skipped/deduped lists into `newBatchWatchNotifyProgress`, so the single notification message reports them like the interactive `/save` batch reply. (The old static `Progress` field couldn't: the lists only exist post-partition.)
- **L1 /history search grammar**: explicit `p<page>` marker (`/history search report p2`); a trailing plain number ("report 2024") stays query text. Page past the end → dedicated `page_out_of_range` reply (en+zh) instead of generic no-matches. Simplifier added shared `historyPageCount` so footer and out-of-range check can't drift.
- **L2 /status disk identity**: `fsutil.DiskID` (unix: stat st_dev; Windows: GetVolumePathName volume root); `addRoot` dedups rows by that identity, degrading to path-as-id on error. Replaces the sameDiskFree free-byte-equality heuristic (on Linux all paths share VolumeName "", so two distinct filesystems with equal free bytes merged rows).
- **L6 tests**: TestWatchMediaGroupBufferInterleavedWriters (2 goroutines × 50 IDs, -race, flush = exactly 50), TestNewBatchWatchNotifyProgressDisabled (nil when WatchNotify off / no botCtx), TestFirstBatchFileName (TaskInfo stub), TestWatchIntakeLiveCursorAdvancesPreFilter (live advances before filters with DefaultStorage=""; backfill does not move it), TestHistorySearchArgs/TestHistoryPageMarker grammar tables.
- Gate green (baseline unchanged: 3 fixture skips only); code-simplifier pass → 2 micro-refinements in history.go, rest confirmed minimal; commit 3861d08 pushed; GH release with binary+checksums (gh resolved `krau/SaveAny-Bot` by default — must pass `-R vibecoder11200/SaveAny-Bot` explicitly).
- Deployed: image saveany-bot:0.68.2-fork (VERSION=0.68.2-fork), DB backup `saveany.db.pre-0.68.2.20261025-120933` taken first, compose tag switched, force-recreated. Boot clean: 6 storages, user client login, catch-up ran and finished silently (cursor current). No schema change this release.
- Remaining deferred: **L5** unique index (user_id, chat_id) — needs a pre-migration dedup sweep; F6 in-flight dedup race stays accepted as documented.

## L5 shipped as v0.68.3-fork (2026-10-25, owner request "IMPLEMENT THIS")
The last deferred review item: one watch row per (user, chat) is now a database invariant, not just an application count-check.

- `DedupWatchChats` (boot): sweeps live duplicate rows — highest cursor survives, ties keep the newest id (the guarded cursor never lowers, so the survivor keeps every duplicate's backfill progress); losers hard-deleted (no query in the codebase reads soft-deleted watches; UnwatchChat/DeleteUser already hard-delete).
- `EnsureWatchChatUniqueIndex`: `CREATE UNIQUE INDEX IF NOT EXISTS idx_watch_chats_user_chat_active ON watch_chats(user_id, chat_id) WHERE deleted_at IS NULL` — **partial** so GORM soft-deleted rows can't block watch→unwatch→watch-again; raw SQL because no GORM tag expresses the WHERE. Wired into `Init` after AutoMigrate, Fatal on failure; both steps idempotent (IF NOT EXISTS / sweep removes nothing on a clean DB).
- Test TestEnsureWatchChatUniqueIndex: drops the index to recreate legacy dirty state, seeds NULL/300/500 + equal-cursor ties + a soft-deleted row; asserts survivors, removed=3, duplicate live insert rejected, re-watch after soft-delete succeeds, second pass idempotent.
- code-simplifier: null result (all candidates rejected on clarity/behavior bar — order-independence of the tie-break kept explicit).
- Gate green (baseline 3 fixture skips). Commit 6c87dc5, tag v0.68.3-fork, GH release with binary+checksums (used `-R vibecoder11200/SaveAny-Bot`).
- Pre-deploy prod check: 1 live watch row, 0 duplicate groups (sweep no-op as expected). DB backup `saveany.db.pre-0.68.3.20261025-164431` taken first. Deployed, boot clean (DB initialized ⇒ index ensure OK; catch-up silent). Verified on prod via `PRAGMA index_list(watch_chats)`: `idx_watch_chats_user_chat_active` unique=1 partial=1.

Review backlog now fully closed: H1+M1-M3+L3 (v0.68.1), M4+L1+L2+L6 (v0.68.2), L5 (v0.68.3). F6 in-flight dedup race remains accepted-by-design as documented.
