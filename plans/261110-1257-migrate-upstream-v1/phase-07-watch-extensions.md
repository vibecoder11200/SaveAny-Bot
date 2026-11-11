---
title: "Phase 7: Watch Extensions"
status: todo
phase: 7
priority: P1
effort: "24h"
dependencies: [phase-02-storage-task-foundations (M2), phase-03-dedup-history-purge, phase-06-media-filter-deltas]
---

# Phase 7: Watch Extensions

## Overview

Port the fork's entire watch extension set onto v1's rewritten watch flow: the colon-safe args parser (`storage:/dir:/msgre:/mediatype:`), per-watch storage/dir resolution precedence, the restart catch-up cursor with its exact durability semantics, shared live intake, always-group albums as ONE batch task with a pinned conflict strategy, batch album notifications, and command/menu/i18n integration.

## Key Insights

- v1 watch flow (verified by reading upstream `client/bot/handlers/watch.go:321-459`): `ListenUserbotMediaEvents` loop → instance media gate at :333 → `GetWatchChatsByChatID` → per-row `MessageFilter` `msgre:` switch (:350-366) → user default storage (:371-375) → default dir (:377-384) → filename strategy → NEW-FOR-ALBUM buffering (:417-426) → rule storage-by-name override (:428-442) → `coretfile.NewTGFileTask` + `core.AddTask` (:444-455). Fork extensions slot into exactly these stations — including ALL watch-loop filter wiring moved here from Phase 6 (per-user default + per-watch mediatype through `AllowedFor`, fork ordering mediatype BEFORE the msgre switch).
- v1 `WatchChat` is minimal (UserID/ChatID/MessageFilter + composite unique `idx_watch_user_chat`, `database/model.go:30-35`); Phase 2's migration 2 added `MediaType/StorageID/DirPath/LastMessageID`.
- Upstream's unique index uses `Unscoped().Delete` on unwatch (`database/chat.go:7-42`), so no soft-deleted rows linger — the fork's partial unique index + `DedupWatchChats` sweep machinery is NOT needed on a v1-built DB; collision handling belongs to the Phase 8 importer only.
- Catch-up semantics (researcher-02 §1e, commits 0532a97/0e5c0d1/b5be423): cursor = "newest message ID observed (live) or durably enqueued (backfill)"; first boot fetches the single newest message (`OffsetID: 0`, no shift) and backfills NOTHING; existing watches walk history ASCENDING using real message IDs as anchors only (never ID arithmetic), `AddOffset: -100` pages, stop on short batch / 500-msg cap / stalled anchor / 10-min budget; FLOOD_WAIT left to middleware; overlap guard skips anything `<= CurrentWatchCursor`; post-flush cursor advance only when every album file actually enqueued; backfill pins Rename conflict strategy.
- Live intake (fork `watch.go:354-412`): cursor advances BEFORE any filter (a busy channel must progress even when nothing matches); mediatype filter first (falling back to per-user default); albums (any `GroupedID != 0`) buffer; singles run msgre per-file. Cursor durability boundary on live singles: touch only after `core.AddTask` succeeds — v1 `AddTask` persists the `TaskRecord` BEFORE enqueue (`core/core.go:227-294`), the same guarantee the fork got from its journal. Live×catch-up overlap is NOT fully closed by the fork machinery for singles: the live path never checks the cursor and the album buffer's message-ID dedupe covers grouped files only — a single arriving inside the 5s debounce window during boot can be enqueued by both catch-up and live. The port adds a live-side guard (Requirements) and keeps the dedup ledger as the second line of defense, so closure must never be presented as absolute.
- Albums (fork `watch.go:532-606`): ANY watched media group becomes ONE batch task at flush; group reduced to one member saves as single; msgre evaluated once per group against the caption-carrying member; conflict strategy pinned (pin-wins; user ASK degrades to Rename); buffer dedupes by message ID — this closed live×catch-up double-intake for ALBUMS only; singles additionally need the live-side cursor guard (Requirements).
- v1 batch plumbing supports grouping: `BatchItem` carries `SourceGroupKey`/`MetadataGroupKey` (`core/tasks/batchtfile/execute.go:218-233`, verified) and telegram `SaveBatch` plans albums from those keys (`storage/telegram/telegram.go:549-660`) — so grouped-ID files flow as one album without a fork-style `ForceGrouping` flag IF the batch is created as one task; verify the batch task constructor preserves group keys end-to-end.
- Notifications: in v1, `openWatchNotification` is a swappable var, but `watchNotifyProgress` is a STRUCT TYPE with a constructor (`client/bot/handlers/watch_notify.go:49,62`) — the batch-tracker port targets the constructor, not a var swap. Fork's per-task bot-context copy (gotgproto `ext.NewContext` trick) has no direct mygotg equivalent — verify mygotg context concurrency rules before porting.

## Requirements

**Functional**
- `/watch <chat> [storage:<name>] [dir:<path>] [msgre:<regex>] [mediatype:<types>]`: flags any order; msgre value = rest of token + following non-flag tokens joined by spaces (colons safe inside); regex capped 512 chars, compiled once per watch via process-lifetime cache; unknown `<x>:` token and malformed grammar → distinct localized errors; `mediatype:` fail-closed; `dir:` passes `SanitizeRelDirPath`; `storage:` resolves name→StorageID at add time (unknown name → error).
- Resolution precedence at flush: explicit `storage:`/`dir:` on the watch REPLACES the rule layer entirely; partial override keeps the user default on the other axis; no overrides → rules → user defaults. A second `SanitizeRelDirPath` pass at flush (defense against out-of-band DB edits).
- Catch-up on userbot start, semantics EXACTLY as Key Insights bullet 4, on mygotg `messages.getHistory`; summary message to the owner (fetched/enqueued/albums/capped). Catch-up runs behind a BOOT-ONLY once-latch (`sync.Once` or an explicit flag) started once from `app.Start` (after `resume.Run` is launched) — NEVER inside `ApplyTelegram`'s call graph, because `ApplyTelegram` (`app/app.go:106`) re-runs on every telegram-settings save (`api/settings.go:31`) and would re-trigger the full sweep plus owner-summary spam on each save.
- Shared intake: live events advance cursor pre-filter; singles: msgre per-file, cursor after successful `AddTask`; albums: buffer → ONE batch task → cursor to max member ID only after durable enqueue. Live-side overlap guard for singles: skip a live event when `event.MessageID <= CurrentWatchCursor(watchID)` (safe: per-chat message IDs are monotonic) — with a regression test for the debounce race (a single inside the 5s window during boot enqueued exactly once); the dedup ledger remains the second line of defense.
- `core.AddTaskWithSource` divergence (deliberate, must be preserved on future upstream merges): upstream deletes the TaskRecord unconditionally when `queue.Add` fails (`core/core.go:284-291`) — including `queue.ErrQueueClosed` during shutdown — while the watch intake advances the cursor pre-filter, so shutdown-corner live events would be permanently lost (no record, cursor past them). Fork-side patch: on `errors.Is(err, queue.ErrQueueClosed)` KEEP the TaskRecord (mirroring fork journal semantics, fork `core/core.go:149-158`) so `resume.Run` recovers it next boot; record this as a deliberate divergence from upstream in the commit message.
- Flush-time storage fallback: if `watch.StorageID` fails to resolve (a storage deleted + recreated under the same name yields a NEW ID, and `StorageReferences` — extended in Phase 2 — plus console deletes can orphan watches), fall back to the user's default storage AND notify the owner once; do NOT advance the cursor while the watch is unresolved (a pre-filter advance would silently eat every subsequent message).
- Album notifications: single-file tracker (v1's, kept) + batch album tracker (ported) with per-album message; `/watch notify on|off` unchanged (v1 has the toggle).
- Per-watch `mediatype:` filter layered with Phase 6's `AllowedFor` helper (ALL watch-loop filter wiring lives here per Phase 6's dependency note): watch filter > user default > instance gate, applied with fork ordering (mediatype BEFORE the msgre switch).
- Watch list rendering shows per-watch storage/dir/mediatype prefixes (3 fork i18n sub-keys) + catch-up summary key; both YAMLs, `go generate .`.

**Non-functional**
- The catch-up walker never stacks a second floodwait wrapper (Phase 4 constraint).
- All new states persisted in DB columns — restart-safe; nothing watch-related lives only in memory.

## Architecture/Approach

- **StorageID at add-time**: the parser accepts a name (user-facing), resolves to ID immediately, stores the ID; renames never orphan watches. Rejected: storing names (v1's own rule layer shows the failure mode, researcher-01 §12).
- **No partial unique index port**: upstream's `idx_watch_user_chat` + `Unscoped().Delete` already enforce one row per (user, chat) on v1-built DBs. Rejected: porting `DedupWatchChats`/`EnsureWatchChatUniqueIndex` — dead machinery here; the importer (Phase 8) owns collision policy for legacy data.
- **Album grouping via v1's native group keys** instead of porting `ForceGrouping`: verify `SourceGroupKey` flows from watch-built batch tasks into telegram album planning; only if v1 drops the key mid-path, add the minimal fork flag. Prefer the native path.
- **Catch-up walker as a separate file** (`watch_catchup.go`), goroutine started ONCE per boot from `app.Start` (after `resume.Run` is launched and the userbot is connected) behind a boot-only once-latch (`sync.Once` or an explicit flag). It must NOT hook `ApplyTelegram` (`app/app.go:106`): that function re-runs on every telegram-settings save (`api/settings.go:31`), which would re-trigger the full sweep and owner-summary spam on each save. Rejected: running catch-up inside `ListenUserbotMediaEvents` — different lifecycle (needs connected client, runs once at boot, then idles).

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Create | `client/bot/handlers/watch_args.go` + test | Parser port (framework-free, ~verbatim) |
| Create | `client/bot/handlers/watch_catchup.go` + test | Catch-up walker ported to mygotg getHistory |
| Create | `client/bot/handlers/watch_notify_batch.go` + test | Batch album tracker on mygotg |
| Modify | `client/bot/handlers/watch.go` | Intake + resolution precedence + always-group + list rendering |
| Modify | `database/chat.go` | `TouchWatchCursor` (guarded, never lowers), `CurrentWatchCursor` |
| Modify | `core/core.go` | `AddTaskWithSource`: KEEP the TaskRecord on `queue.ErrQueueClosed` (deliberate divergence from upstream `core/core.go:284-291`; preserve on upstream merges) |
| Modify | `app/app.go` | Boot-only once-latch start of the catch-up goroutine from `app.Start` (after `resume.Run` is launched) — never inside `ApplyTelegram`'s call graph |
| Modify | `common/i18n/locale/{en,zh-Hans}.yaml` | 5 watch keys + `go generate .` |
| Create | `e2e/telegram/watch_test.go` | Watch e2e against the fake harness (the fake implements `MessagesGetHistoryRequest`, `e2e/telegram/fake.go:259`): live watched-chat media event → task created; catch-up sweep after simulated restart enqueues missed messages exactly once |
| Modify | `core/tasks/batchtfile/` (only if group-key verification fails) | Minimal ForceGrouping equivalent |

## Implementation Steps

1. Port `watch_args.go` + tests verbatim (grammar is framework-free). Extend add-time handling: resolve `storage:` via `storage.GetStorageByUserIDAndName` → `StorageID`; sanitize `dir:`; validate `mediatype:` fail-closed; store `MessageFilter` only when msgre present (v1 column semantics unchanged).
2. Extend the watch event loop (`watch.go:339-375` region) — this is where ALL watch-loop filter wiring lands (moved from Phase 6 to avoid double-editing this exact region): apply per-watch `MediaType` + user default through `AllowedFor` (helper from Phase 6) with fork ordering (mediatype BEFORE the row's msgre switch); then resolve target: `watch.StorageID != 0` → `GetStorageByUserIDAndID` and skip rule storage matching entirely (explicit override replaces rule layer) — if that resolution FAILS (storage deleted + recreated, new ID), fall back to the user's default storage, notify the owner once, and do NOT advance the cursor while unresolved; `watch.DirPath != ""` → dir override (sanitized again at flush); else v1 behavior (rules → user defaults).
3. Port shared intake ordering: cursor touch pre-filter (live), album buffering for `GroupedID != 0`, singles with post-`AddTask` cursor touch. Port `TouchWatchCursor`/`CurrentWatchCursor` into `database/chat.go` (guarded never-lower update). Add the live-side overlap guard the fork lacked for singles: skip a live event when `event.MessageID <= CurrentWatchCursor(watchID)` (per-chat message IDs are monotonic). Patch `core.AddTaskWithSource` (`core/core.go:284-291`): on `errors.Is(err, queue.ErrQueueClosed)` KEEP the already-persisted TaskRecord instead of deleting it, so `resume.Run` recovers shutdown-corner live events next boot (fork journal semantics, fork `core/core.go:149-158`) — record this deliberate divergence from upstream in the commit message and note it must be preserved on future upstream merges.
4. Album flush: port `processWatchMediaGroup` — one batch task for the group (single-member degenerates to single task), msgre once per group on the caption-carrying member, pinned conflict strategy (verify v1's `ConflictStrategy` enum + where a batch task's strategy is set; ASK→Rename degradation), cursor to max member ID after `AddTask` success. Verify group-key flow (batch constructor → `BatchItem.SourceGroupKey` → telegram album plan) before writing; add the minimal flag only if broken.
5. Port the buffer manager (`watchMediaGroupHandler`) with message-ID dedupe and flush timer (v1's `watchMediaGroupMgr` exists for NEW-FOR-ALBUM; fork's always-group semantics replace the conditional).
6. Port `watch_catchup.go` onto mygotg: newest-page init (`OffsetID: 0`, no `AddOffset` shift), ascending anchors, `AddOffset: -100` pages, stop conditions (short batch / 500 cap / stalled anchor / 10-min budget), overlap guard (`<= CurrentWatchCursor`), Rename-pinned backfill, post-flush guarded advance, owner summary via a per-goroutine bot context (verify mygotg context copy/concurrency first — if contexts are goroutine-safe, drop the copy trick and note it).
7. Start the catch-up goroutine from `app.Start` behind a boot-only once-latch (`sync.Once` or an explicit flag), after `resume.Run` is launched and the userbot is connected; NEVER inside `ApplyTelegram`'s call graph — it re-runs on every telegram-settings save (`api/settings.go:31`) and would re-trigger the sweep + owner-summary spam. Overlap with `MarkInterruptedTaskRecords`/`resume.Run` double-enqueue is covered by the cursor overlap guard.
8. Port batch album notifications; keep v1's single-file tracker; ensure `/watch notify` toggle governs both.
9. Watch list rendering prefixes + catch-up summary i18n keys; `go generate .`.
10. Port the fork watch tests that encode semantics (`watch_args_test.go` 7, `watch_catchup_test.go` 6 incl. cursor-guard and live-cursor-advances-pre-filter, notify tests) — these are the phase's contract. Add the debounce-race regression test (single inside the 5s window during boot is enqueued exactly once, via the live-side cursor guard). Write NEW e2e watch tests against the fake-RPC harness (`e2e/telegram/watch_test.go`; the fake implements `MessagesGetHistoryRequest`, `e2e/telegram/fake.go:259` — no watch e2e exists upstream, none was scheduled before this finding): (a) a watched-chat media event creates a task; (b) a catch-up sweep after a simulated restart enqueues missed messages exactly once. The manual restart check stays in Phase 10's verification checklist as well.

## Todo

- [ ] Parser + add-time resolution (name→ID, sanitize, fail-closed mediatype) + tests
- [ ] Intake: pre-filter cursor, per-watch mediatype + user default via AllowedFor (ALL watch-loop filter wiring, moved from Phase 6; fork ordering mediatype-before-msgre), resolution precedence + flush-time re-sanitize
- [ ] Flush fallback: unresolved watch.StorageID → user default storage + owner notified once + cursor NOT advanced while unresolved
- [ ] Live-side cursor guard (`event.MessageID <= CurrentWatchCursor` → skip) + debounce-race regression test
- [ ] `AddTaskWithSource` keeps the TaskRecord on `queue.ErrQueueClosed` (deliberate divergence, documented, preserved on upstream merges)
- [ ] Cursor helpers (guarded) in database
- [ ] Album always-group → ONE batch task, pinned strategy, post-enqueue cursor
- [ ] Group-key end-to-end verification (native album planning or minimal flag)
- [ ] Catch-up walker on mygotg with all six semantics + tests
- [ ] Catch-up lifecycle: boot-only once-latch started from `app.Start` (never inside `ApplyTelegram`'s call graph)
- [ ] e2e watch tests on the fake harness (live event → task; restart catch-up enqueues missed messages exactly once)
- [ ] Batch album notifications + toggle (batch tracker targets the `watchNotifyProgress` CONSTRUCTOR — it is a struct type, not a var; only `openWatchNotification` is a var)
- [ ] List rendering + i18n + `go generate .`

## Verification

```bash
go build ./... && go vet ./...
go test ./client/bot/handlers/... -run "Watch"
go test ./database/... -run "Watch"
go test -tags=e2e ./e2e/telegram    # includes this phase's NEW watch e2e (fake harness implements MessagesGetHistoryRequest, e2e/telegram/fake.go:259)
```

Semantics checklist to assert in tests (ported from fork tests): live cursor advances pre-filter; live events at/below the cursor are skipped (debounce-race guard); cursor never lowers; backfill skips `<=` cursor; album cursor advances only after full enqueue; first-boot init stores newest ID and enqueues nothing; parser: colon-containing regex across words; unknown flag token error; 513-char regex rejected.

## Risk Assessment

1. **mygotg getHistory/context semantics drift** (medium likelihood, high impact): the catch-up walker's correctness rests on offset/anchor behavior and per-goroutine context use. Mitigation: step 6 verifies primitives against mygotg source before porting; the NEW watch e2e tests added in step 10 (against the fake harness, which implements `MessagesGetHistoryRequest`) cover the pump; keep history walking in one file so fixes are localized.
2. **Double intake live×catch-up** (medium, high — the fork's own hardest bug, commit b5be423): the fork's guards (buffer message-ID dedupe + catch-up's cursor overlap check) did NOT close singles — a single in the 5s debounce window during boot could be enqueued by both catch-up and live. The port adds the live-side cursor guard (`event.MessageID <= CurrentWatchCursor` → skip) plus its regression test, with the dedup ledger as the second line of defense; never relax "cursor after durable enqueue" and never present closure as absolute.
3. **Group-key breakage forcing ForceGrouping** (medium, medium): if v1's batch path drops `SourceGroupKey` for programmatically-built batches, albums split into singles in telegram storage. Mitigation: step 4 verification first; fallback flag is small and isolated to the batch constructor.

## Security Considerations

- `dir:` values are user input becoming storage paths: double sanitization (add-time + flush-time) is mandatory; tests must include traversal payloads (`../`, absolute, UNC, drive letters).
- Regex from users runs against every watched message: the 512-char cap + compiled-once cache bounds CPU/memory; never use user regex with unbounded input elsewhere.
- Catch-up fetches history with the USERBOT account — it can read whatever the userbot can; filters never widen access, but the summary message goes only to the watch owner (per-user loop already scopes it).

## Next Steps

Phase 8 (importer) needs these final WatchChat shapes and cursor columns; run it after this phase.
