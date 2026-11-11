---
title: "Phase 5: Ops Commands (/status /id /task queue positions)"
status: todo
phase: 5
priority: P2
effort: "4h"
dependencies: [phase-01-start]
---

# Phase 5: Ops Commands (/status /id /task queue positions)

## Overview

Port the fork's operator commands onto v1: `/status` (owner-gated instance health), `/id` (userbot-preferring entity resolve), and queue positions in the task list rendering — mostly assembling v1 accessors that already exist.

## Key Insights

- v1 already exposes queue snapshots: `core.GetRunningTasks(ctx)` / `core.GetQueuedTasks(ctx)` return `[]queue.TaskInfo` (`core/core.go:386-390`, verified) — no new queue API needed for counts; positions are slice index+1 at render time (fork pattern, `client/bot/handlers/tasks.go:94-104` in v0).
- v1 has NO `core.StartTime()` (grep verified: only dlutil timing hits) — uptime needs a new package-level start timestamp in `core` (set in `core.Run` or package init).
- Command registration is data-driven: append to `CommandHandlers` (`client/bot/handlers/register.go:17-36`); the list auto-becomes the BotFather command set (`client/bot/bot.go:178-190`). Commands run behind the whitelist gate `checkPermission` (register.go:60-89).
- `/status` must gate to the FIRST whitelisted user only (fork `status.go:28-31`): instance-wide stats (disk, queue, cache size) must not leak to other whitelisted accounts. v1 whitelist source: `config.C().GetUsersID()`. The gate must NOT be derived per-boot (a "lowest chat id that is whitelisted" fallback silently reassigns the owner vs fork semantics): persist the owner ONCE at first boot as `meta.owner_chat_id` — captured from the TOML import's first whitelist entry (imported users first), falling back to the first entry of `cfg.Users` on fresh installs — and gate BOTH `/status` and `/purge` (Phase 3) on it; it must be captured before any second whitelisted user exists.
- `/id` prefers the **userbot** client for `@username` resolution so public channels the bot is not in still resolve (fork `id.go:24-27`); v1 userbot context accessor to be located in `client/user/`.
- Sizes/paths: DB path comes from boot flags (`boot/flags.go:44-56` `--db-path`, default `<data-dir>/saveany.db`); cache dir from `config.C().Temp.BasePath` (cache lives under temp per `cmd/run.go` cache-clean) — expose accessors rather than re-deriving.

## Requirements

**Functional**
- `/status` (persisted owner only — `meta.owner_chat_id`, see Key Insights; never a lowest-chat-id fallback): uptime; queued + running counts; cache dir size; resume-state size measured as the total `.part`/`.bitmap` sidecar size under the cache dir (v1's native resume artifacts; the fork's `.resume` layout is not ported — locked decision, Phase 2 step 7(e)); DB file size; disk free for cache base + every local storage root, deduped per filesystem identity; every field degrades to "unknown" on error.
- `/id @username`: title/type/numeric ID; userbot-first resolution with bot fallback; typed classification (channel/megagroup→group/user/chat).
- Task list rendering (`/tasks` in v1): queued tasks show their position (index+1), max 10 rows + truncation note.
- i18n keys for all three (12 fork keys: 8 status, 4 id) in both YAMLs; register all commands.

**Non-functional**
- `/status` must never block on a hung filesystem call beyond a short timeout where fork used one; disk-free calls are per-identity (deduped), not per-storage.

## Architecture/Approach

- Thin handlers over tested core code (fork had no dedicated tests for these for the same reason); port `fsutil.DirSize` (single-flight, TTL cache), `fsutil.DiskFree`/`DiskID` (+ `_unix`/`_windows` variants) as the only new infrastructure. Rejected: a periodic metrics goroutine — fork computes on demand; KISS.
- Accessors: add `database.DBPath()` (or export from the database package where `Init` received it, `database/db.go:17-44`) instead of threading the path through settings again.

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Create | `client/bot/handlers/status.go` | `/status` (port; v1 accessors) |
| Create | `client/bot/handlers/id.go` | `/id` (port; mygotg resolve) |
| Create | `settings/owner.go` | Capture-once `OwnerChatID()` backed by `meta.owner_chat_id` (first whitelist entry; `cfg.Users[0]` on fresh installs); shared gate for `/status` here and `/purge` in Phase 3 |
| Create | `common/utils/fsutil/dirsize.go` + test | `DirSize` single-flight TTL cache (port) |
| Create | `common/utils/fsutil/disk_free_unix.go`, `disk_free_windows.go` | `DiskFree`/`DiskID` (port) |
| Modify | `core/core.go` | `StartTime()` package timestamp |
| Modify | `database/db.go` | `DBPath()` accessor |
| Modify | `client/bot/handlers/` task list rendering (v1 `tasks.go` / task menu builder) | Queue positions |
| Modify | `client/bot/handlers/register.go` | Append `status`, `id` commands |
| Modify | `common/i18n/locale/{en,zh-Hans}.yaml` | 12 keys + `go generate .` |

## Implementation Steps

1. Add `core.StartTime()` (set once when `core.Run` initializes) and `database.DBPath()`.
2. Port `fsutil` size/disk helpers + tests (Windows variant compiles on this machine; unix variant compiles under `GOOS=linux go build`).
3. Port `/status`: gate = persisted `meta.owner_chat_id` via `settings/owner.go` — on first read, if absent, capture it once from the first whitelist entry (TOML-imported users first, else first entry of `cfg.Users`) and store it; never derive it dynamically (a lowest-chat-id fallback silently reassigns the owner vs fork semantics). `/purge` (Phase 3) reuses the same accessor. Assemble fields with per-field error degradation; resume-state size = total `.part`/`.bitmap` sidecar size under the cache dir.
4. Port `/id`: locate v1 userbot client/context accessor (`client/user/`); resolve via userbot, fall back to bot ctx; classify entity types.
5. Add queue positions to v1's task-list rendering (find the queued-tasks render path used by `/tasks` and the task callback menu).
6. Register commands, add i18n keys, `go generate .`.

## Todo

- [ ] `StartTime()` + `DBPath()` accessors
- [ ] fsutil DirSize/DiskFree/DiskID ported + tests (both GOOS compile)
- [ ] `/status` with persisted owner gate (`meta.owner_chat_id` capture-once) and degraded fields; resume-state size = `.part`/`.bitmap` total under cache
- [ ] `/id` userbot-preferring resolve
- [ ] Queue positions in task list
- [ ] i18n + registration + `go generate .`
- [ ] Owner persisted once at first boot (`meta.owner_chat_id` from the first whitelist entry / `cfg.Users[0]`); captured before any second whitelisted user exists; `/status` AND `/purge` (Phase 3) gate on it

## Verification

```bash
go build ./... && go vet ./...
go test ./common/utils/fsutil/...
go test ./client/bot/handlers/...
GOOS=linux go build ./common/utils/fsutil/   # unix disk-free compiles
go generate . && git diff --stat -- common/i18n/i18nk/keys.go
```

Live spot-check (needs running bot): `/id @durov` from the whitelisted owner; `/status` shows non-zero uptime; a second whitelisted account (if any) gets "not permitted".

## Risk Assessment

1. **Owner identity drift** (medium likelihood, medium impact): deriving the owner per-boot from the live whitelist (or worse, lowest chat id) silently reassigns the owner whenever the whitelist changes — diverging from fork semantics (first whitelist entry). Mitigation: persist `meta.owner_chat_id` once at first boot (first whitelist entry; fresh installs take `cfg.Users[0]`), captured before any second whitelisted user exists; gate `/status` AND `/purge` (Phase 3) on the persisted value.
2. **Userbot absent** (high likelihood in fresh setups, low impact): `/id` must fall back to the bot client and still work for entities the bot can see; degrade with the resolve-failed key otherwise.
3. **Containerized /status reads** (medium, low): disk-free of overlay filesystems and bind mounts differs from host view (Phase 9 images); every field already degrades to "unknown" — keep that path tested rather than special-casing Docker.

## Security Considerations

- `/status` gate is the whole point: disk paths, sizes, and queue contents are instance metadata. Keep the gate check FIRST in the handler, before any DB/disk work, so even error paths don't leak.
- `/id` resolves arbitrary usernames — no privilege implied; output is public entity info.

## Next Steps

Independent of phases 3/4/6 file-wise (touches `handlers/status.go`, `id.go`, task rendering) — safe to run in parallel; coordinate `register.go` merges.
