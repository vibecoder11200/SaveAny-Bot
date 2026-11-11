---
title: "Phase 4: Download Guards (Stall Watchdog + Floodwait Meter)"
status: todo
phase: 4
priority: P2
effort: "6h"
dependencies: [phase-02-storage-task-foundations (M1)]
---

# Phase 4: Download Guards (Stall Watchdog + Floodwait Meter)

## Overview

Port the framework-free download guards — stall watchdog, floodwait (crawling) meter, and progress-edit flood backoff — and wire them into v1's download and progress-edit paths, extending v1's existing middleware stack instead of stacking a second waiter.

## Key Insights

- All three fork components are context/timer based with no gotgproto imports (researcher-02 §7): `stall.go` (15-min watchdog with `Kick`, non-blocking, `DownloadFailure` picking stall → flood-crawl → raw cause), `floodwait_meter.go` (15-min window, 2-min slow-RPC threshold, cancel when accumulated successful-slow time ≥ half the window), `progress_edit.go` (global 1 rps limiter, process-wide "flooded-until" clamp [0, 1h], `MESSAGE_NOT_MODIFIED` as idempotent success, final edit waits out the pause).
- v1 already has a floodwait middleware chain: recovery → retry → `floodwait.NewSimpleWaiter` (`client/middleware/default.go:14-20`). The fork's catch-up code deliberately did NOT stack another wrapper (fork `watch_catchup.go:21-25`); fork's own middleware added `WithMaxRetries` + `ratelimit.New(100ms, 5)` on top of the simple waiter — v1's defaults must be compared before changing anything.
- v1 download entry: `tdler.DownloadToCache` (`common/tdler/download_cache.go:23-88`) with resumable `.part` + bitmap sidecar (`common/tdler/resume.go:51-104`). Fork wired per-RPC `Observe` inside its downloader (`common/tdler/dler.go:51` in v0); the v1 equivalent RPC observation point must be located by reading `common/tdler/` first.
- v1 has ~80 bot-side `ctx.EditMessage` sites across ~25 handler files, but the fork routed ONLY its two central progress trackers through the limiter (`core/tasks/tfile/progress.go`, `core/tasks/batchtfile/progress.go` — verified fork consumers); handler/menu one-shot edits were never rate-limited in the fork and stay direct here. Note: the ported global limiter waits on `context.Background()` (fork behavior) — keep it and flag it as known-acceptable.

## Requirements

**Functional**
- `common/utils/dlutil/stall.go`: watchdog with resettable timer, `Kick` on progress, `Stalled` flag, cause-chain `DownloadFailure`; `DownloadStallTimeout = 15min`.
- `common/utils/dlutil/floodwait_meter.go`: `Observe(dur, err)` counting only successful slow RPCs; window cancel semantics as fork.
- `common/utils/tgutil/progress_edit.go`: global 1 rps + flooded-until clamp [0, 1h]; drop non-final edits during pause; final edit waits bounded by cap+ctx; idempotent MESSAGE_NOT_MODIFIED.
- Watchdog + meter wired around v1's tfile download (plain + stream) and batchtfile element downloads (plain + stream) — all four paths, matching fork coverage (fork initially missed the per-element guard; do not repeat that).
- Progress-edit backoff wired INSIDE the two central progress trackers (`core/tasks/tfile/progress.go`, `core/tasks/batchtfile/progress.go`) exactly as the fork did; handler/menu one-shot edits stay direct (they were never limited in the fork).
- Middleware: compare v1's `client/middleware/default.go` stack with fork's (max-retries + 100ms/5 ratelimit); add only what v1 lacks.

**Non-functional**
- Guards must not add per-download goroutine leaks: watchdog goroutine exits on ctx done (fork `stall.go` shape).
- No second floodwait waiter anywhere; catch-up (Phase 7) relies on the same middleware stack.

## Architecture/Approach

- **Port components verbatim, rewire integration only**: the three files are framework-free; the only new engineering is finding v1's per-RPC hook and edit sites. Rejected: re-implementing guards on v1 taskevent stream (`pkg/taskevent/taskevent.go:24-45`) — events are too coarse (task-level, not RPC-level) for the crawl detector.
- **One meter per download, shared edit limiter process-wide**: matches fork semantics (meter detects per-download crawl; edit flood is a bot-account-wide condition).

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Create | `common/utils/dlutil/stall.go` + test | Port verbatim |
| Create | `common/utils/dlutil/floodwait_meter.go` + test | Port verbatim |
| Create | `common/utils/tgutil/progress_edit.go` + test | Port verbatim |
| Modify | `common/tdler/` (download path) | Wire meter `Observe` at the per-RPC point found in step 1 |
| Modify | `core/tasks/tfile/execute.go`, `core/tasks/tfile/stream.go` | Watchdog ctx + meter |
| Modify | `core/tasks/batchtfile/execute.go` | Per-element plain + stream guards |
| Modify | `client/middleware/default.go` | Only the delta v1 lacks vs fork stack |
| Modify | `core/tasks/tfile/progress.go`, `core/tasks/batchtfile/progress.go` | Wire the limiter inside the two central progress trackers (the fork's only limited consumers); handler/menu edits stay direct |

## Implementation Steps

1. Read `common/tdler/` in v1 to find the per-RPC completion point (fork wired `dler.go:51`); document it with file:line in the commit message. If v1's downloader exposes no per-RPC callback, add a minimal optional callback parameter defaulting to nil (no behavior change when unset).
2. Port the three component files + their tests (fork test files: `dlutil/stall_test.go` 4 tests, `floodwait_meter_test.go` 6, plus progress-edit tests).
3. Wire tfile: derive `dlCtx` from watchdog ctx so in-flight RPCs observe cancellation; pass meter into the download call (fork pattern: `tdler` args).
4. Wire batchtfile element downloads — both plain and stream paths.
5. Apply progress-edit backoff exactly where the fork did: wire the limiter INSIDE the two central progress trackers (`core/tasks/tfile/progress.go`, `core/tasks/batchtfile/progress.go`). Do NOT sweep the ~80 bot-side `ctx.EditMessage` sites across ~25 files — handler/menu one-shot edits stay direct (fork behavior; `client/bot/handlers/watch_notify.go` has no direct progress edits and is untouched by this step). Keep the global limiter's `context.Background()` wait (fork behavior) and flag it as known-acceptable in the commit message.
6. Diff v1 `client/middleware/default.go` against fork's waiter config; apply only missing knobs (max retries, ratelimit 100ms burst 5).
7. Run verification; manually reason through one slow-download scenario in the commit message (what fires, in what order).

## Todo

- [ ] Per-RPC observation point located/documented (or minimal callback added)
- [ ] stall.go + floodwait_meter.go + progress_edit.go ported with tests green
- [ ] All four download paths guarded (tfile plain/stream, batchtfile element plain/stream)
- [ ] Limiter wired inside the two central progress trackers (tfile + batchtfile progress.go); handler/menu edits stay direct; context.Background() wait kept as known-acceptable
- [ ] Middleware delta applied (no duplicate waiter)
- [ ] Cancel-then-resume leaves a consistent `.part` + bitmap sidecar (test or e2e evidence)

## Verification

```bash
go build ./... && go vet ./...
go test ./common/utils/dlutil/...
go test ./common/utils/tgutil/... -run ProgressEdit
go test ./core/tasks/...
```

## Risk Assessment

1. **No clean per-RPC hook in v1's downloader** (medium likelihood, medium impact): meter needs RPC-granular durations. Mitigation: step 1 adds an optional callback with nil default; if even that is invasive, degrade to watchdog-only and file the crawl-detector as a documented gap for Phase 11 review — never block downloads on meter plumbing.
2. **Watchdog false positives on legitimately slow sources** (low, medium): 15-min no-progress threshold is fork-proven in production; keep the value identical, make it a constant (not config) as fork did.
3. **Edit backoff starving final edits** (low, high): final progress message must never be dropped — port the fork invariant (final edit waits, bounded by clamp+ctx) and its tests exactly.
4. **Guard-cancelled download vs resume state** (low, high): a stall/crawl cancellation aborts a `.part` download mid-flight; v1's resume bitmap sidecar is atomically rewritten (`common/tdler/resume.go:51-104`), so cancellation leaves a consistent resumable state — assert this with a cancelled-download-then-resume test if the e2e resume tests don't already cover cancellation.

## Security Considerations

- No new inputs; guards are internal. The edit-flood clamp is itself a Telegram-side abuse guard (prevents the bot from hammering editMessage and getting the account flood-banned) — keep clamp ≤ 1h so a stuck clamp cannot silence notifications indefinitely.

## Next Steps

Phase 7's catch-up walker must use the SAME middleware for FLOOD_WAIT (no manual wrapper) — note this constraint carries forward.
