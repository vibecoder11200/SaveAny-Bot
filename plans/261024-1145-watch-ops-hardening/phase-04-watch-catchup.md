---
title: "Phase 4: watch restart catch-up (v0.67.0 part 2)"
status: done
---

# Phase 4: Watch restart catch-up

> Updated: Red Team Session 1 (2026-10-24) — findings S2/S3/S4/F1/F6/F7/F10/A7/A8 applied: no manual flood wrapper, cursor advances only on durable enqueue (albums at flush), live cursor advances on every observed event, guarded first-boot init, SendMessage summary after force-flush, non-overwrite strategy for backfill, tryGetInputPeer peer resolution.

## Context Links

- Research: `plans/reports/researcher-261024-1827-gotd-catchup.md` (paging algorithm)
- Decisions: plan.md #1 (cap 500), #9 (summary notify), #10 (first-boot cursor only)
- Depends: Phase 3 (`LastMessageID`, guarded `TouchWatchCursor`, shared intake with per-group filtering)

## Overview

Persist a last-seen cursor per watch; on boot backfill messages missed while offline (forward paging, cap 500/watch), reconstructing albums, feeding them through the SAME intake as live events; one summary per watch after its buffer force-flush.

## Key Insights (research + red-team verified)

- Paging: raw `tg.MessagesGetHistoryRequest{Peer, OffsetID: cursor, AddOffset: -100, Limit: 100, MinID: cursor}`; results DESCENDING → reverse; `len(batch) < limit` = done; never ID-arithmetic the anchor; `Limit` ≤ 100; gotd query iterator unusable (drops MinID/MaxID).
- **FLOOD_WAIT: the userbot ALREADY auto-retries** via `middleware.NewDefaultMiddlewares(ctx, 5*time.Minute)` = `floodwait.NewSimpleWaiter().WithMaxRetries(3).WithMaxWait(5*time.Minute)` (client/user/userclient.go:68, client/middleware/default.go:16-33). Do NOT add a manual wrapper (would stack ≈12 waits ≈1h stall per request). Only add a total wall-clock budget per watch.
- **Peer resolution**: `watch_chats.ChatID` from `@username` watches is the PLAIN channel id; gotgproto PeerStorage keys are TDLib-marked (-100…) → `GetInputPeerById(plainID)` returns `InputPeerEmpty` (miss). The correct 4-variant retry exists but is unexported: `storage/telegram/util.go:179-202 tryGetInputPeer` (retries channel/chat/user marked variants). Export it (or move to tgutil) and treat `InputPeerEmpty`/zero as miss → skip watch with warn.
- Albums: GroupedID identical in history; consecutive IDs; pages split albums; album can straddle cursor. Catch-up feeds the Phase-3 intake (buffer + flush), so straddled partials flush as smaller groups — fine.
- Journal: `journal.Record` happens inside `core.AddTask` (core.go:119-145) — durability boundary = AddTask success, NOT "message emitted".

## Requirements

- [ ] **Live cursor: advance on EVERY media event observed for a watched chat** (per watch row), BEFORE filters — non-matching media must also advance the cursor or busy channels re-walk the cap forever (A8). Guarded UPDATE.
- [ ] **Cursor advance on durable enqueue only** (F1): singles — after `core.AddTask` returns nil; albums — via a cursor-advance callback fired INSIDE the flush callback after its AddTask succeeds (advance per-group max ID). Never advance at emit/buffer time.
- [ ] Boot backfill per watch: `LastMessageID == 0` → guarded init to current newest (`Limit:1` call; same `last_message_id < ?` guard — no unconditional write, no lowering race F10), NO backfill. Else backfill.
- [ ] Backfill: newest guard first; forward pages ascending; cap 500 msgs/watch; stop on short batch or reaching guard; cap-hit → summary notes older skipped. Total wall-clock budget per watch (default 10 min) — abort cleanly, cursor stays at last durable point.
- [ ] Backfilled messages go through the SAME intake (per-file mediatype gate, per-group msgre, album buffer, per-watch resolution) as live.
- [ ] Backfill tasks pin a NON-OVERWRITE strategy (S4: overwrite disables dedup → live/backfill overlap would duplicate; pass explicit `rename`-family internal strategy for backfilled tasks).
- [ ] Summary per watch via `botCtx.SendMessage(ownerChatID, ...)` (Reply needs an update — none exists at boot, recovery.go:352-354 is the pattern), sent AFTER that watch's album buffer force-flush; wording distinguishes "enqueued" (not "saved"); silent when 0 new messages; skipped-older note when cap-hit.
- [ ] Peer resolution failure (InputPeerEmpty after variant retry) → warn log, skip that watch (live unaffected).
- [ ] Pacing ≥1 req/s between getHistory calls across the sequential watch loop.

## Architecture

New file `client/user/watch_catchup.go` + thin wiring:

```go
type CatchUpResult struct { Fetched, Enqueued, Albums, SkippedByCap int; Capped bool; Err error }
// RunWatchCatchUp(raw *tg.Client, watches []database.WatchChat, intake func(watch, ev), now time.Time) []CatchUpResult
```

- Intake reuse: `processMediaEventForWatch(botCtx, user, watch, ev)` extracted in Phase 3; catch-up builds events via `tfile.FromMediaMessage(media, raw, msg, tfile.WithNameIfEmpty(tgutil.GenFileNameFromMessage(msg)))` (same as client/user/watch.go:86-88).
- Force-flush: after a watch's pages are exhausted, force-flush its `watchMediaGroupMgr` buffer (new method `FlushChat(chatID)`) → THEN send summary + final cursor already advanced by the flush callback.
- Ordering: catch-up goroutine starts AFTER listener (`register.go:76-78`); live events during backfill may enqueue the same files — dedup absorbs most; residual check-then-record race window documented (F6: dedup check at download-start, record post-upload — concurrent tasks can both pass; mitigation: non-overwrite strategy + SkipIfExists on recovery path; if duplicates observed on prod, add in-flight `(user,storage,file_unique_id)` set).
- Loss window (S3, documented): cursor advances at enqueue; a task that later fails terminally is NOT re-fetched (recovery only re-runs crash-interrupted tasks). Owner sees the failure via watch-notify and can /save manually. Summary says "enqueued".
- No manual FLOOD_WAIT wrapper (S2): rely on SimpleWaiter middleware; only a wall-clock budget (`context.WithTimeout` around the whole per-watch run).

## Related Code Files

- Create: `client/user/watch_catchup.go`, `watch_catchup_test.go`
- Modify: `client/bot/handlers/watch.go` (extract intake in P3; flush-callback cursor advance; `FlushChat`), `watchMediaGroupHandler` (+FlushChat), `register.go` (boot `go runWatchCatchUp(botCtx)` gated on userbot enable)
- Modify: `database/chat.go` (guarded TouchWatchCursor reused for init; `ListAllWatchChats`)
- Modify: `storage/telegram/util.go` (export tryGetInputPeer → `tgutil.ResolveStoredPeer` or export in place) + reuse
- Modify: locales (summary/skipped/enqueued wording, en + zh-Hans) + regen

## Implementation Steps

1. `ListAllWatchChats`; guarded TouchWatchCursor already from P3.
2. Live loop: TouchWatchCursor per watch row on every observed media event (pre-filter).
3. Flush callback: cursor-advance callback param (per-group max ID) after AddTask success.
4. `watch_catchup.go`: pure planner `planPages(fetch, cursor, cap) (pages [][]*tg.Message, capped bool, err error)` — unit tests: gaps, short batch, cap, album split across pages, straddled boundary, descending→ascending; wall-clock budget honored (fake clock).
5. `FlushChat` force-flush; summary notifier (SendMessage, post-flush, "enqueued" wording).
6. Peer: export variant-retry resolver + InputPeerEmpty=miss test.
7. Backfill strategy pin (non-overwrite) — extend Phase-3 BatchOptions/singles task creation with explicit strategy for backfilled tasks.
8. Boot wiring + 1 req/s pacing; locales; full gate.

## Todo

- [ ] ListAllWatchChats + pre-filter live cursor advance
- [ ] Flush-callback cursor advance (durable boundary)
- [ ] watch_catchup.go planner + tests (gaps/cap/split/budget)
- [ ] FlushChat + SendMessage summary (enqueued wording, post-flush)
- [ ] Peer variant-retry export + miss handling
- [ ] Backfill non-overwrite strategy pin
- [ ] Boot wiring + pacing + i18n + full gate

## Success Criteria

- Planner tests cover the matrix above; guarded-init test (never lowers cursor); no manual flood code (grep: no AsFloodWait in watch_catchup.go).
- Dev sim: post → stop bot → post 3 (incl. album) → start → summary AFTER flush, all 3 saved, storage listing shows no duplicates; second restart quiet-watching → no summary, no fetch loop; busy-watch cursor growth test (non-matching events advance cursor — verify via DB between events).

## Risk Assessment

- **[ASSUMPTION — research-verified]** Positional windows/GroupedID contiguity (core.telegram.org + gotd source). Prod verify is the real check; degradation = split albums, files still save.
- **[Documented loss window]** terminal task failure post-enqueue is not re-fetched (S3); watch-notify surfaces it; manual /save recovers. If unacceptable later: move cursor advance to task-completion callback (design hook left via flush-callback pattern).
- **[Dedup race]** check-then-record window under parallel workers (F6): mitigated by strategy pin + SkipIfExists recovery; residual risk accepted, observed-duplicate → add in-flight set (contingency documented).
- 500-file burst on long downtime: owner-accepted; workers bound concurrency; Sequential watch loop + 1 req/s caps API pressure; wall-clock budget bounds the whole run.

## Security Considerations

- Single-user assumption (plan.md constraints); catch-up reads only chats with existing watch rows. Log chat IDs + counts only (no InputPeer structs). Summary → watch owner only.

## Next Steps

- Phase 5 independent; Phase 6 ships v0.67.0 (Phases 3+4).
