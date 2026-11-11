---
title: "Watch hardening + Ops commands (fork v0.66.6 → v0.68.0)"
description: "Watch catch-up/album/per-watch storage/msgre parity + /status + /history + queue position + quick bugfix batch, shipped as 3 releases"
status: done
priority: P1
effort: 16h
tags: [feature, backend, database, bugfix]
created: 2026-10-24
---

# Watch hardening + Ops commands

## Overview

Close the watch-vs-save feature gap and add ops visibility to the fork, in 3 independently verified releases: **v0.66.6** (bugfix batch), **v0.67.0** (watch: per-watch storage/dir, msgre parity, always-group albums, restart catch-up), **v0.68.0** (ops: /status, /history, queue position). All design decisions below were confirmed by the owner in 3 interview rounds (11 questions) on 2026-10-24.

## Decisions Log (owner-confirmed)

| # | Decision | Choice |
|---|----------|--------|
| 1 | Catch-up cap | 500 msgs/watch per boot; older skipped with notice; manual /save for the rest |
| 2 | Per-watch storage/dir syntax | flags `storage:`/`dir:` like `msgre:`, any order, optional |
| 3 | /history scope | paginated list + search + stats + record source chat_id/message_id (new saves only) |
| 4 | /update | point GitRepo at fork `vibecoder11200/SaveAny-Bot` + create a GitHub release per ship; Docker stays report-only |
| 5 | Watch albums | ALWAYS group watched media groups (batch task → real album send on Telegram storage, subfolder elsewhere) |
| 6 | Album folder name | first file's base name; on collision with a non-empty dir → suffix `-HHMMSS`; fallback `album-<timestamp>` if name unusable |
| 7 | msgre parity | watch matches `caption + " " + filename` exactly like /save; fix `:`-in-regex bug both at parse and match time |
| 8 | Ship cadence | 3 releases (bugfix → watch → ops), each gated + verified on prod |
| 9 | Catch-up notify | 1 summary message per watch after backfill; silent when 0 new |
| 10 | First-boot cursor | existing watches (no cursor) record cursor = newest on first v0.67.0 boot, NO historical backfill |
| 11 | Album collision concern | owner raised duplicate-name folders — resolved by decision 6 suffix scheme |

## Constraints

- Upstream compatibility: additive schema only (new nullable columns on `watch_chats`, new columns on fork-only `saved_file_dedup`); fork commands appended at END of `CommandHandlers`; i18n keys in BOTH en.yaml + zh-Hans.yaml; fork logic in separately-named small funcs (upstream roadmap #251 will replace gotgproto — keep gotgproto types at handler boundary).
- Never log `InputMediaDocument`/file reference/access hash structs. No `git add -A` (CRLF phantoms) — stage files individually. `plans/` + `plans/reports/` must remain gitignored (they contain server details; fork repo is public).
- DB: SQLite `SetMaxOpenConns(1)`; GORM AutoMigrate must stay data-loss-free (ADD COLUMN only).
- Dedup (`SavedFileDedup`) is the double-save guard between live events and catch-up — keep fail-open semantics.
- **Single-user deployment assumption** (red-team S5): whitelist contains only the owner; watch/catch-up flow through the owner's userbot is trusted. `/status` is additionally gated to the first whitelisted user. If a second user is ever whitelisted, revisit watch creation + catch-up scoping first.

## Non-goals

- HTTP resume for directlinks/parsed (explicitly deferred by owner). vi locale. Web UI. gotgproto migration. Changing /save album behavior beyond the shared collision-suffix helper.

## Phases

| Phase | Name | Status |
|-------|------|--------|
| [Phase 2](./phase-02-bugfix-batch.md) | Bugfix batch — v0.66.6 | Pending |
| [Phase 3](./phase-03-watch-storage-parity-album.md) | Watch: per-watch storage/dir + msgre parity + always-group albums — v0.67.0 | Pending |
| [Phase 4](./phase-04-watch-catchup.md) | Watch: restart catch-up — v0.67.0 | Pending |
| [Phase 5](./phase-05-ops-commands.md) | Ops: /status + /history + queue position — v0.68.0 | Pending |
| [Phase 6](./phase-06-ship-verify.md) | Ship & prod verify (3 releases) | Pending |

## Success Criteria

- [ ] v0.66.6 on prod: `/help` matches registered commands (no ghost /import); a watch regex containing `:` round-trips through `/lswatch` un-truncated; `/update` reports against the fork repo.
- [ ] v0.67.0 on prod: `/watch @c storage:X dir:Y msgre:re` saves into X under Y; a watched album produces ONE batch task (album send on Telegram storage, subfolder on local; `-HHMMSS` suffix on collision); msgre matches filenames like /save; `docker restart` mid-stream produces a catch-up summary per watch and the missed files are saved with no duplicates.
- [ ] v0.68.0 on prod: `/status` shows uptime/version/workers/queue/cache+resume sizes/disk free/watch count; `/history`, `/history search <q>`, `/history stats` work and show chat/msg source for new saves; `/task queued` shows positions.
- [ ] Full gate green before each release: `go generate` + `gofmt` + `go vet` + `go test -race` (standard skip list) + `GOOS=linux go build`.

## Red Team Review

### Session — 2026-10-24
**Reviewers:** Security Adversary (Fact Checker), Failure Mode Analyst (Flow Tracer), Assumption Destroyer (Scope Auditor) — 29 raw findings, deduped to 15 adjudicated below.
**Note:** the user did not respond to the adjudication prompt; per tool guidance the recommended dispositions were applied by best judgment (apply-all, shared-core fix for /save, single-user documented). All three are visible here and reversible before cook.

| # | Finding | Severity | Disposition | Applied To |
|---|---------|----------|-------------|------------|
| 1 | No path sanitizer exists — `dir:` traversal unguarded (WebDAV/Alist joins raw; tfile sanitize flattens paths) [S1+A10] | Critical | Accept | Phase 3 (`SanitizeRelDirPath` + tests) |
| 2 | Cursor advanced at emit time — album buffer is in-memory; crash loses files permanently [F1] | Critical | Accept | Phase 4 (advance only after AddTask; per-group at flush) |
| 3 | msgre filter runs per-member before album gate → album members 2..N dropped; ≤1 groups dropped [F2] | Critical | Accept | Phase 3 (per-GroupID filtering; ≤1 → singles) |
| 4 | Batch core album subfolder is rule-gated, not group-gated — rule-less callers get flat paths [A1] | Critical | Accept | Phase 3 (ForceGrouping mode) |
| 5 | Per-watch override unimplementable while "keeping rules inside core" [A2] | Critical | Accept | Phase 3 (Override option = skip rule layer) |
| 6 | Manual FLOOD_WAIT wrapper stacks on existing SimpleWaiter middleware (~1h stall) [S2] | High | Accept | Phase 4 (no manual wrapper; wall-clock budget) |
| 7 | Enqueue ≠ saved: cursor semantics + check-then-record dedup race under concurrency [S3+F6] | High | Accept (scoped) | Phase 4 ("enqueued" wording, documented loss window, storage-listing checklist; in-flight set = contingency) |
| 8 | Overwrite strategy disables dedup → duplicates on overlap [S4] | High | Accept | Phase 4 (non-overwrite pin for backfill) |
| 9 | trackMsgID=0 edits fail; conflict-ASK prompts to message 0 and silently drops files [F3+A5] | High | Accept | Phase 3 (UI sink nil-safe; non-interactive strategy pin) |
| 10 | tftask vs batchtfile ProgressTracker incompatible — adapter required [F4+A9] | High | Accept | Phase 3 (`batchWatchNotifyProgress` spec) |
| 11 | `VERSION=v…-fork` breaks semver.Parse; asset-less releases → DetectLatest fails; --prerelease skipped [F5+A6] | High | Accept | Phase 6 (no leading v; binary+checksums assets; never prerelease) |
| 12 | PeerStorage marked-key miss for plain channel IDs; use existing tryGetInputPeer variant-retry [A7] | High | Accept | Phase 4 (export + reuse; InputPeerEmpty = miss) |
| 13 | Parity: File.Name() ≠ GetMediaFileName for attribute-less docs [A3] | High | Accept | Phase 3 (GetMediaFileName primary, shared helper) |
| 14 | Live cursor must advance on every observed event (not just enqueued) or busy watches re-walk forever [A8+F10] | Medium | Accept | Phase 4 (pre-filter advance; guarded init) |
| 15 | Misc: regex cache mandatory [S6]; DirSize single-flight [S7]; /status admin gate [S9]; /purge-resets-history doc [F8]; per-release DB backup [F9]; multi-user exfil documented as single-user assumption [S5]; RecordDedup count 10 not 9 [S-fact]; /update checksum assets [S8-light] | Medium | Accept | Phases 2/4/5/6 |

**Rejected:** none (all evidence-backed findings accepted; S3 scoped to documentation + wording rather than full task-completion cursor — design hook left via flush-callback pattern).

### Whole-Plan Consistency Sweep
- Files reread: plan.md, phase-02…phase-06 (post-edit).
- Decision deltas checked: 15 findings + 3 best-judgment calls.
- Reconciled stale references: 6 (phase-03 "pure extraction" language replaced by extended-core contract; phase-04 cursor/flood/peer/summary sections rewritten; phase-06 VERSION/assets/backup steps; RecordDedup count; register.go line refs :76-78; /save two documented behavior deltas surfaced in plan.md Non-goals note + phase-03 risks).
- Unresolved contradictions: 0.

Full reviewer report (consolidated): `plans/reports/code-reviewer-261024-redteam-consolidated.md`.

## Validation Log

### Verification Results (via red-team Full tier)
- Tier: Full (5 phases) — 22 fact-checks + 10 flow traces across 3 reviewers.
- Verified: 27 | Failed: 7 (all 7 became accepted findings above and were applied) | Unverified: 1 (PeerStorage API name — resolved by finding 12).
- Post-edit state: no `[UNVERIFIED]` tags remain except explicitly-marked implementation-time verifications inside phases.

### Validation Interview
- 11 design questions answered by owner (3 rounds, logged in Decisions Log).
- 3 post-red-team questions (apply findings / /save spillover / multi-user) received no UI response — recommended dispositions applied by best judgment and recorded in the Red Team section note; owner may override before `/ak:cook`.

### Post-implementation code review — 2026-10-25 (45375da..ac552ed, v0.66.6→v0.68.0)
- Gates: `go run ./cmd/geni18n` green · `go vet ./...` clean · gofmt clean for all changed files · `go test -race` green on touched packages · `GOOS=linux go build` green.
- Verdict: FIX BEFORE RELEASE on 2 items; rest ship as follow-ups.
  1. HIGH — live×backfill double intake: catch-up filters pages against the boot-snapshot cursor (`runWatchCatchUpOne` stale `cursor` + fixed MinID), so messages that arrive live during the catch-up window are processed twice; album members duplicated inside ONE batch task defeat dedup entirely. Fix: skip backfill messages ≤ the CURRENT DB cursor before intake (live pre-advance makes that lossless).
  2. MEDIUM — catch-up summary wording inverted: the cap keeps the OLDEST 500 and defers the NEWEST to next boot, but en/zh text says "older messages were skipped — /save manually". Text-only fix.
- Follow-ups (non-blocking): shared-botCtx SendMessage in `sendWatchCatchUpSummary` (violates newBotContext invariant), backfill livelock past >500 non-matching backlog, watch-notify album adapter drops creation-time skipped/deduped lists, /history search grammar eats trailing numbers as page, sameDiskFree equality heuristic, ASK-with-trackMsgID-0 landmine in `CreateAndAddBatchTGFileTask`, missing tests for FlushChat / batchWatchNotifyProgress / intake cursor semantics.
- Confirmed good: additive schema, commands appended at END, i18n keys complete both locales, dedup fail-open preserved at all 4 TG record sites, guarded cursor never lowers, traversal guarded at creation and join sites, no forbidden struct logging, SQLite single-conn safe.

## Key References

- Research: [gotd catch-up](../reports/researcher-261024-1827-gotd-catchup.md) · [upstream drift](../reports/researcher-261024-1827-upstream-drift.md)
- Scout: [watch pipeline + DB](../reports/Explore-261024-1827-watch-pipeline.md) · [queue/ledger/help/update](../reports/Explore-261024-1827-queue-ledger-help.md)

<!-- slug: watch-ops-hardening -->
