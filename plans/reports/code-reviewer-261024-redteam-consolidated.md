# Red-team consolidated report — plan 261024-1145-watch-ops-hardening

Session 2026-10-24. 3 hostile reviewers (code-reviewer agents) with verification roles: Security Adversary (Fact Checker), Failure Mode Analyst (Flow Tracer), Assumption Destroyer (Scope Auditor). 29 raw findings → 15 adjudicated (all Accept, 1 scoped) → applied to phase files. Adjudication table lives in the plan's `## Red Team Review`.

## Key verified facts that changed the plan

1. `pkg/tfile/sanitize.go:15-17` is a filename sanitizer (keeps last path component) — no rel-path validator exists; WebDAV (`storage/webdav/webdav.go:91`) and Alist (`storage/alist/alist.go:204`) join user paths unguarded; only local guards (`storage/local/local.go:55-62`).
2. Userbot already auto-retries FLOOD_WAIT: `client/user/userclient.go:68` `middleware.NewDefaultMiddlewares(ctx, 5*time.Minute)` → `floodwait.NewSimpleWaiter().WithMaxRetries(3).WithMaxWait(5m)` (`client/middleware/default.go:16-33`).
3. `CreateAndAddBatchTGFileTaskWithEdit` (`shortcut/tftask.go:126-317`): rule-gated album subfolders (:185); applyRule clobbers caller target (:170-186); 7 unconditional EditMessage sites; ASK-conflict prompt returns WITHOUT enqueueing (:279-281); ≤1-file album groups dropped (:236-239); returns dispatcher.EndGroups.
4. Album buffer (`watch.go:199-244`) is in-memory only; durability boundary = `core.AddTask` → `journal.Record` (`core/core.go:119-145`).
5. Watch msgre filter runs per-member BEFORE the album gate (`watch.go:277-296` → `:350-363`); albums carry caption on one member only.
6. `event.File.Name()` ≠ `tgutil.GetMediaFileName` for docs without filename attribute (tfile sanitizes + falls back to GenFileNameFromMessage; `pkg/tfile/tgfile.go:200-206,107` vs `common/utils/tgutil/media.go:18-36`).
7. gotgproto PeerStorage keys are TDLib-marked (-100…); `@username` watches store PLAIN channel ids → `GetInputPeerById` misses → InputPeerEmpty. Correct variant-retry exists unexported at `storage/telegram/util.go:179-202` (tryGetInputPeer).
8. `semver.Parse` (update.go:22) rejects leading `v`; `ghselfupdate.DetectLatest` requires a release asset with OS_ARCH suffix and skips prereleases.
9. Dedup check at download-start, record post-upload (`batchtfile/execute.go:543/571` vs `:307-311`) — concurrent double-enqueue can duplicate; overwrite strategy disables dedup entirely (`tftask.go:104-106`).
10. `botCtx.Reply` needs an update — boot-time messages must use `botCtx.SendMessage` (pattern: `client/bot/recovery.go:352-354`).
11. RecordDedup sites: 10 (4 TG + 6 non-TG). `/purge` deletes ledger rows (`purge.go:32`).
12. Multi-user hole: userbot is a single global account; any whitelisted user can watch any chat it can see (`middleware.go:35-50` whitelist) — documented as single-user assumption; `/status` additionally admin-gated.
13. `tftask.ProgressTracker` vs `batchtfile.ProgressTracker` are incompatible interfaces — adapter required for watch album notify.
14. AutoMigrate failure is `logger.Fatal` (`database/db.go:47`) → per-release DB backup mandated.

## Full findings + verification tables

Kept verbatim in the conversation transcript of 2026-10-24 (session 3b9030172307bd37b1a00d1b0616931c); disposition rationale in plan.md `## Red Team Review`.
