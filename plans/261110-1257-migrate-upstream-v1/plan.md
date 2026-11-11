---
title: "migrate-upstream-v1"
description: "Rebuild the fork on upstream SaveAny-Bot v1 (beta.5): port all fork feature groups onto a new v1 branch, import v0 data, and validate before merging."
status: pending
priority: P1
effort: "98h (~12.5d)"
branch: "v1 (created from upstream/main in Phase 1)"
tags: [migration, telegram-bot, go, sqlite, upstream-rebase]
created: 2026-11-10
---

# migrate-upstream-v1

## Overview

The fork's `main` branch is a 49-commit v0-based fork; upstream has moved to v1.0.0-beta.5 (`upstream/main`, 8ed4149), a rewrite: SQLite settings + web console replace config.toml/viper, mygotg replaces gotgproto, storage IDs replace names, and native resumable downloads + restart recovery replace the fork's journal. This plan rebuilds the fork on upstream v1: a new `v1` branch carries upstream's implementations for overlapping features (resume, recovery, instance media filter, MinIO→s3) and ports only the fork deltas — watch extensions, 3-layer dedup + ledger + /history + /purge, ops commands, resend + SkipLarge + forum topics, download guards — plus a v0-DB→v1-DB importer so production data survives.

## Goals

| # | Goal | Priority |
|---|------|----------|
| 1 | `v1` branch builds, vets, and passes the full test matrix on Windows (CGO-free) | P1 |
| 2 | All fork feature groups ported with fork semantics preserved (dedup fail-open, cursor durability, resend ambiguity taxonomy, sanitizers) | P1 |
| 3 | Upstream wins overlaps: native resume/recovery used; MinIO via built-in `s3`; no journal, no v0 HTTP API, no minio storage type, no auth_terminal | P1 |
| 4 | v0 production DB (watches incl. cursors, rules, dirs, dedup ledger, telegram ledger) imported idempotently into v1 | P1 |
| 5 | Dockerfile.micro / Dockerfile.pico recreated for v1; user's MinIO verified through `s3` | P2 |
| 6 | Docs + production migration guide; merge v1→main only after user validation | P2 |

## Phases

| # | Phase | Effort | Depends on | Status |
|---|-------|--------|-----------|--------|
| 1 | [Baseline & Branch Setup](./phase-01-start.md) | 2h | — | Pending |
| 2 | [Storage & Task Foundations](./phase-02-storage-task-foundations.md) | 16h | 1 | Pending |
| 3 | [3-Layer Dedup + /history + /purge](./phase-03-dedup-history-purge.md) | 10h | 2 (M1) | Pending |
| 4 | [Download Guards](./phase-04-download-guards.md) | 6h | 2 (M1) | Pending |
| 5 | [Ops Commands (/status /id /task positions)](./phase-05-ops-commands.md) | 4h | 1 | Pending |
| 6 | [Media Filter Deltas](./phase-06-media-filter-deltas.md) | 6h | 2 (M1) | Pending |
| 7 | [Watch Extensions](./phase-07-watch-extensions.md) | 24h | 2 (M2), 3, 6 | Pending |
| 8 | [v0→v1 Data Importer](./phase-08-db-importer.md) | 12h | 2, 3, 7 | Pending |
| 9 | [Docker Images (micro/pico)](./phase-09-docker-images.md) | 4h | 1 | Pending |
| 10 | [Docs, Migration Guide & Final Validation](./phase-10-docs-final-validation.md) | 6h | 8, 9 | Pending |
| 11 | [Post-Cook Review Protocol](./phase-11-post-cook-review-protocol.md) | 8h | 10 | Pending |

## Dependencies

- **Branch topology**: `main` is the v0 maintenance line and stays untouched until the user validates the `v1` branch; merge v1→main is a manual, user-approved step proposed in Phase 10. `v1` is cut from `upstream/main` (8ed4149) in Phase 1.
- Phase 2 owns DB migration #2 (ALL fork tables/columns at once) and storage/task primitives, delivered as two internal milestones (defined in phase-02): M1 = migration 2 + FileUniqueID + sanitizers + telegram_saved_files ledger tables; M2 = SkipLarge + forum topics + ledger recording + hardening audit + resend + file_reference refresh. Phases 3, 4, and 6 depend on 2 (M1) only — the riskiest phase-2 item (mygotg resend) is M2 and does not gate them. Phase 7 (watch) depends on 2 (M2), 3, and 6 (task plumbing, dedup counters, media filter layering; phase 7 also absorbs ALL watch-loop filter wiring from phase 6, which no longer touches `watch.go`). Phase 8 (importer) needs the FINAL table shapes from 2+3+7. Phase 10 validates everything; Phase 11 is mandatory post-cook review.
- Parallelism: 5 and 9 touch disjoint file sets and may run alongside 2–4; 3 and 4 both edit `core/tasks/tfile/execute.go` and `core/tasks/batchtfile/execute.go`, so sequence them (3 before 4) to avoid conflicts.

## Success Criteria

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` green on Windows; `go test -tags=e2e ./e2e` and `./e2e/telegram` green
- [ ] Every fork feature demonstrable on v1: watch with `storage:/dir:/msgre:/mediatype:` + catch-up after restart; dedup skip on re-save; /history /purge /status /id; resend-by-id; forum topic routing; SkipLarge reported not silent
- [ ] Importer fixture test: v0 fixture DB → v1 rows + marker; re-run is a no-op; unresolvable storages skipped with counts, never fatal
- [ ] Dockerfile.micro/.pico build CGO-free with v1 tags; MinIO reachable via `s3` (path-style) per user verification
- [ ] Phase 11 exits with zero open Critical/High review findings and a green matrix

## Red Team Review

### Session — 2026-11-10
**Reviewers:** 4 parallel code-reviewer agents (Security Adversary + Fact Checker, Failure Mode Analyst + Flow Tracer, Assumption Destroyer + Scope Auditor, Scope & Complexity Critic + Contract Verifier). Tier: Full (11 phases).
**Findings:** 29 raw → 26 after dedup (2 Critical, 10 High, 14 Medium) + 2 benign fact corrections
**Disposition:** 26 accepted, 0 rejected, 0 evidence-filter rejections (all findings carried file:line evidence)

| # | Finding | Severity | Disposition | Applied To |
|---|---------|----------|-------------|------------|
| 1 | Same-dir migration mutates v0 DB before importer guards (boot opens+migrates active path first) — found independently by all 4 reviewers | Critical | Accept | Phases 2, 8, 10 |
| 2 | Sanitizer wiring pointed at handler layer; fork defense lives in pkg/tfile constructors (covers API/relay/resume ingresses) | Critical | Accept | Phase 2 |
| 3 | UserID plumbing omitted resume.go/retry.go/api/factory.go rebuild sites | High | Accept | Phase 3 |
| 4 | Dedup/SkipLarge/resend hooks anchored below v1's stream early-return | High | Accept | Phases 2, 3 |
| 5 | Importer-created users auto-whitelisted (Enabled default true) | High | Accept | Phase 8 |
| 6 | /purge wipes shared telegram ledger storage-wide | High | Accept | Phase 3 |
| 7 | Imported watch rows not re-validated (regex cap, media grammar, dir sanitize) | High | Accept | Phase 8 |
| 8 | --fork-db without --config = hollow import + permanent marker | High | Accept | Phases 8, 10 |
| 9 | AddTask deletes TaskRecord on queue-closed → shutdown-corner watch message loss | High | Accept | Phase 7 |
| 10 | Storage delete+recreate orphans watches; pre-filter cursor eats messages | High | Accept | Phases 2, 3, 7 |
| 11 | OnConflict DoNothing inert on non-unique indexes; empty-string keys break unique | High | Accept | Phases 2, 3, 8 |
| 12 | Resume-state step 7(e) false premise; .resume port would violate Goal 3 | High | Accept | Phases 2, 5, 10 |
| 13–26 | e2e watch coverage claim; catch-up re-fire latch; mid-fork schema fixtures; pending_tasks drain warning; migration v2 lock-in + downgrade snippet; live-side cursor guard; ffmpeg/yt-dlp LookPath warnings; owner persistence for /status//purge; SkipLarge vs splitSize conflation; simplifier re-review; edit-limiter scope; phase 6/7 watch-loop ownership; CI workflow scope + Docker-dependent todo | Medium | Accept | Phases 2–11 |
| C1/C2 | watchNotifyProgress is a type not a var; mediafilter = 5 Allowed + 1 Allows (non-substitutable) | correction | Accept | Phases 6, 7 |

### Whole-Plan Consistency Sweep
- Files reread: plan.md + all 11 phase files (post-application)
- Decision deltas checked: 15 (same-dir removal, sanitizer locus, API dedup OFF, stream-layer policy, /purge gating, partial unique index shape, .resume N/A + .part/.bitmap size field, phase-6→7 watch wiring move, M1/M2 split, ErrQueueClosed divergence, no CI workflow, boot latch, live-side guard, owner persistence, SkipLarge placement)
- Reconciled stale references: 0 remaining (verified by keyword sweep: "same data dir", ".resume size", "lowest chat id", "build-docker-fork", "6 call sites", "swappable", ApplyTelegram-hook phrasing, phase-6 watch.go references)
- Unresolved contradictions: 0

## Validation Log

### Verification Results
- **Tier:** Full (4 verification roles, executed inside the red-team reviewers)
- **Claims checked:** 60+ (30 Fact-Check + 12 Flow-Trace + 12 Scope-Audit + 12 Contract-Verification)
- **Verified:** 58 | **Failed:** 2 (both benign, corrected in plan: #26 watchNotifyProgress var claim, #14 mediafilter count) | **Unverified:** 0
- **Failures:** both corrected during red-team application (see C1/C2 above)

### Interview — 2026-11-10
Pre-plan interview (4 questions) locked: rebuild-on-v1 strategy, upstream-wins-overlaps, all feature groups ported, keep production data. Post-red-team interview (4 questions: apply findings, API dedup policy, stream dedup policy, CI images) — user did not respond; orchestrator proceeded with the Recommended options per session policy: **apply all 26 findings; API tasks dedup OFF (userID=0); stream keeps layer-1 + record-after-upload, hash N/A-by-design; no CI workflow**. These remain user-overridable before `/ak:cook` starts.

## Post-Cook Protocol (MANDATORY)

After phases 1–10 complete, Phase 11 MUST run before reporting done. Sequence: (a) spawn a code-reviewer agent TEAM in parallel — four lenses: Security Adversary, Failure Mode Analyst, Assumption Destroyer, Scope/Complexity — each reviewing the full `git diff upstream/main..v1` with file:line evidence; (b) adjudicate findings and fix all Critical/High issues; (c) run a code-simplifier agent pass (behavior-preserving, keeping fork code idiomatic against upstream style); (d) run ONE focused re-review (Failure Mode Analyst lens) over just the simplifier's diff, then fix its Critical/High findings; (e) re-run the full test matrix from Phase 10; (f) only then deliver the final report to the user. Details and per-lens prompts: [phase-11-post-cook-review-protocol.md](./phase-11-post-cook-review-protocol.md).

<!-- slug: migrate-upstream-v1 -->
