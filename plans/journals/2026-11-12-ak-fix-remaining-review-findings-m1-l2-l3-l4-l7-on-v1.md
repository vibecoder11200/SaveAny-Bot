---
title: "ak-fix: remaining review findings M1/L2/L3/L4/L7 on v1"
date: 2026-11-12
summary: "Fixed all remaining accepted review defects (live cursor resolution gating, floodwait classification cap, Win32 dir segments, catch-up boot ceilings, ext copy seed serialization) + gofmt/go-enum hygiene; matrix green"
---

# ak-fix: remaining review findings M1/L2/L3/L4/L7 on v1

## What happened

User invoked /ak-fix to fix ALL remaining issues from the Phase 11 review of the
SaveAny-Bot v0->v1 migration (branch v1). The four-lens review had fixed H1-H4 and
most Mediums/Lows in earlier commits (2bff225, ad2043c, 652718e), leaving five
ACCEPTED findings that were real defects. Each was re-diagnosed against the code
before editing:

- M1 (live watch cursor advanced before enqueue): the unconditional pre-filter
  advance jumped the cursor past failed enqueues (permanent loss) and past failed
  album flushes. Fixed with per-event RESOLUTION gating: filter outcomes count as
  resolved (busy-channel correctness preserved), enqueue/user-fetch failures leave
  the message below the cursor for next boot's catch-up, album members resolve at
  flush. Reuses the H1 counter semantics.
- L2 (FLOOD_WAIT > 15 min misclassified as stall): SimpleWaiter sleeps inside the
  RPC, invisible mid-flight; the watchdog killed 16-min waits at 15 min and
  DownloadFailure reported "stalled" (checked Stalled before Crawled). Fixed:
  meter-first classification + MaxAutoFloodWait cap (stall window - 1m) so
  oversized waits are refused up front with the true cause.
- L3 (Win32-hostile dir segments): trailing dots/spaces trimmed per segment,
  reserved device stems shifted via fsutil.ShiftReservedDeviceStem ("con" ->
  "con_"); ".." was already dropped.
- L4 (unbounded watch catch-up fan-out at boot): boot-run ceilings added
  (500 files total, 30 min wall clock), checked between watches; unvisited
  watches defer with cursors untouched. Caught and fixed my own stale-read bug
  in the wiring (deferred publish read the previous watch's counters on
  first-boot early returns).
- L7 (ext.Context same-tick seed collision): mygotg seeds per-copy rand from
  time.Now().UnixNano(); the stream feeds Telegram RandomID send nonces, so
  same-tick copies that both send collide and the second send is silently
  dropped. Cannot reseed (unexported field, external module) - newExtContext
  helper serializes copies across clock ticks; both creation sites routed
  through it.

Hygiene: settings/legacy.go gofmt'd; go-enum v0.9.1 pinned and installed
(generate now runs; generated code byte-identical, only tool provenance headers
differ - committed artifacts kept, regeneration documented as header-drift-only).

Verification: full matrix green - build/vet/CGO_ENABLED=0/micro tag combo, unit
battery on ncruces (30 pkgs ok) and sqlite_glebarez, e2e (api + telegram suites
incl. TestWatchCatchUp with the resolution-gated cursor), go generate idempotent
(artifact-identical).

## Decision

- Deliberately NOT fixed: L13 (plans/ artifacts are the coordination surface,
  pruning is the user's call), gofmt drift in untouched upstream files (app/,
  cmd/ - pure-noise hunks in the fork diff), the two locked resume regressions
  (plan decision: upstream native resume wins), ncruces Windows absolute-path
  defect (upstream; skipf documented), API dedup-off and stream-mode hash N/A
  (locked by design).
- Commit: 10c72b5 on v1; ledger updated
  (plans/261110-1257-migrate-upstream-v1/reports/review-findings.md).

## Next steps

- Branch v1 remains USER-GATED for the v1->main merge: production migration per
  docs/fork/migration-v0-to-v1.md, validate, then user says merge.

> Historical work record — not durable authority. Prefer docs/specs/ADRs for current decisions.
