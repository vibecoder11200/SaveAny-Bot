---
title: "Phase 5: /status + /history + queue position (v0.68.0)"
status: done
---

# Phase 5: Ops commands — /status, /history, queue position

## Context Links

- Scout: `plans/reports/Explore-261024-1827-queue-ledger-help.md`; Research: `plans/reports/researcher-261024-1827-upstream-drift.md`
- Decisions: plan.md #3 (/history scope incl. source columns)

## Overview

Owner-visibility commands: `/status` (uptime, version, workers, queue depth, cache+resume sizes, disk free, watch count), `/history` (paginated ledger + search + stats + source chat/msg for new saves), and position numbers in `/task queued`.

## Key Insights

- Queue accessors exist: `core.GetLength/GetRunningTasks/GetQueuedTasks` (core.go:158-166); `QueuedTasks()` returns enqueue order → position = index+1. `/task` renders max 10 with truncated note (tasks.go:81-117).
- Ledger `SavedFileDedup` is fork-only → safe to extend. No chat/msg columns today; no list/search queries. RecordDedup sites: **10 total — 4 TG** (`tfile/execute.go:172,207`, `batchtfile/execute.go:408,452`) + **6 non-TG** (aria2dl:217, directlinks:199, telegraph:109, parsed:167, transfer:134, ytdlp:222) with no TG source. **/purge deletes these rows** (purge.go:32 → PurgeDedupByStorage) — /history is therefore "live ledger" history; purge resets it (must be documented in /history output + release notes).
- `tgutil.ChatIdFromPeer(msg.GetPeerID())` + `msg.ID` derive source at TG task sites (`batchtfile/task.go` already uses this pattern for recovery payloads).
- No dir-size/disk-free/uptime helpers exist. `golang.org/x/sys v0.48.0` already indirect.
- Upstream-compat: append new commands at END of `CommandHandlers`; build on core accessors, don't fork the /tasks concept.

## Requirements

- [ ] `/status`: version+commit+dockermode, uptime, workers N, queued/running counts, cache dir size + resume dir size, disk free for cache base + each LOCAL storage root, watch count, DB file size. One compact message. **Gated to the FIRST configured whitelisted user (admin)** — global stats must not leak to additional whitelisted IDs (S9); non-admin callers get a "not permitted" reply.
- [ ] `SavedFileDedup` += `ChatID int64` (index), `MessageID int` — 0 = unknown (legacy rows). Populated by tfile + batchtfile execute paths (TG sources only); other recorders unchanged.
- [ ] `/history [page]` — 20/page, newest first: name (path base), storage, dir, size, time, source chat (if known).
- [ ] `/history search <text> [page]` — LIKE on storage_path + storage_name (escaped %/_).
- [ ] `/history stats` — total count + size, by storage (count/size), by day (last 14 days). Footer: "history tracks successful saves; /purge resets it per storage".
- [ ] `/task queued` — each row prefixed `#N` (position), footer "x/y shown".
- [ ] All strings in BOTH locales; commands appended at end of CommandHandlers; `/help` updated.

## Architecture

1. **fsutil additions** (`common/utils/fsutil/`): `DirSize(path) (int64, error)` (WalkDir, skip errors) — **single-flight + 60s TTL cache per path** (S7: WalkDir is uncancellable; a naive timeout leaks the goroutine and repeated /status stack concurrent full-tree walks on 100k-file caches); `DiskFree(path) (uint64, error)` — `disk_free_unix.go` (`//go:build !windows`, `syscall.Statfs`), `disk_free_windows.go` (`golang.org/x/sys/windows.GetDiskFreeSpaceEx`; promote x/sys to direct dep). Unit test DirSize only (DiskFree = smoke assert ≥ 0 on temp dir).
2. **Uptime**: `var startTime = time.Now()` in `cmd/run.go` (export via small `config` or `core` getter — prefer `core.StartTime()` alongside accessors); set explicitly at boot for testability.
3. **Ledger queries** — new file `database/saved_file_history.go`: `ListSavedFiles(ctx, userID, q string, page, pageSize) ([]SavedFileDedup, int64 total, error)` (q empty = list), `SavedFileStats(ctx, userID) (Stats, error)` with per-storage + per-day aggregation (SQLite `date(created_at)`); LIKE escaping via `ESCAPE '\'`.
4. **Source columns**: extend `RecordDedupAfterUpload`/`RecordDedup` call sites in `core/tasks/tfile/execute.go:172,207` + `core/tasks/batchtfile/execute.go:408,452` — add new `...WithSource(ctx, logger, userID, stor, fileUniqueID, hash, size, path, chatID int64, msgID int)` variants in `database/saved_file_dedup.go`; old funcs delegate with 0,0. Derivation helper `tgutil.SourceFromTGFile(file tfile.TGFile) (chatID int64, msgID int)` (type-assert `tfile.TGFileMessage`, `Message()` non-nil).
5. **Handlers**: `client/bot/handlers/status.go`, `client/bot/handlers/history.go` (+ register at END, i18nk keys, msgelem renderers). `/status` walks storage roots from `storage.GetUserStorages(ctx, userID)` — disk-free only for local-storage types; Telegram/WebDAV rows show "—".
6. **Queue position**: tasks.go `showQueuedTasks` — enumerate `QueuedTasks()`, prefix `#i+1`, keep maxShown truncation.

## Related Code Files

- Create: `common/utils/fsutil/dirsize.go`, `disk_free_unix.go`, `disk_free_windows.go`, `dirsize_test.go`
- Create: `database/saved_file_history.go` + `saved_file_history_test.go`
- Modify: `database/saved_file_dedup.go` (WithSource variants), `database/model.go` (SavedFileDedup cols)
- Modify: `core/tasks/tfile/execute.go`, `core/tasks/batchtfile/execute.go` (source pass-through)
- Create: `common/utils/tgutil/source.go` (`SourceFromTGFile`)
- Modify: `core/core.go` or `cmd/run.go` (startTime), `client/bot/handlers/status.go` (new), `history.go` (new), `tasks.go` (positions), `register.go` (2 commands at END), `help.go` locales + regen
- Modify: `go.mod` (x/sys direct)

## Implementation Steps

1. fsutil DirSize + DiskFree (build tags) + tests; `GOOS=linux go build ./...` to prove unix file compiles.
2. Model columns + WithSource recorders + `SourceFromTGFile` + wire 4 TG call sites.
3. `saved_file_history.go` queries + tests against temp SQLite (list/search/stats; LIKE escaping; legacy rows show unknown source).
4. startTime getter; `status.go` handler + i18n.
5. `history.go` handler + i18n (arg grammar: `[page]` int → page; `search <text> [page]`; `stats`).
6. tasks.go positions; help text + locales + regen i18nk.
7. Full gate incl. `GOOS=linux go build`.

## Todo

- [ ] fsutil DirSize/DiskFree + build-tag tests
- [ ] Ledger source columns + WithSource + 4 call sites
- [ ] History queries + tests
- [ ] /status handler + uptime
- [ ] /history handler (list/search/stats)
- [ ] Queue positions + help/locales + regen
- [ ] Full gate green

## Success Criteria

- Unit tests green: dirsize, history queries (pagination, search escape, stats, legacy 0-source), positions rendering.
- Dev run: `/status` shows real numbers (compare `du -sh` on cache); `/history` lists recent saves incl. chat source for new TG saves; `/task queued` numbered.
- AutoMigrate on prod DB copy adds columns, existing rows keep ChatID=0.

## Risk Assessment

- DirSize on huge cache walks every file (~seconds for 100k files) — run in goroutine with 5s timeout; /status renders "…" on timeout. Bounded, one-off per command.
- SQLite LIKE on unindexed storage_path = full scan of user's rows — ledger is per-user (small); stats query bounded by GROUP BY. Acceptable; note: if ledger grows >100k rows, add later index (out of scope).
- Disk-free for non-local storages undefined → show "—" (no fake numbers).
- Upstream /tasks divergence: positions only touch fork's tasks.go rendering — merge-safe (upstream churns this file; keep the diff small).

## Security Considerations

- `/status` gated to first whitelisted user (S9); `/history` strictly scoped `WHERE user_id = caller`; `/history` footer documents that `/purge` resets history (F8). LIKE input escaped. No secrets printed (paths only).

## Next Steps

- Phase 6 ships v0.68.0 with prod verification.
