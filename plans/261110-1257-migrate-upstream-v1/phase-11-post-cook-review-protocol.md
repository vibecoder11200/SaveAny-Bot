---
title: "Phase 11: Post-Cook Review Protocol"
status: todo
phase: 11
priority: P1
effort: "8h"
dependencies: [phase-10-docs-final-validation]
---

# Phase 11: Post-Cook Review Protocol

## Overview

Mandatory orchestration phase after phases 1–10: run a four-lens code-reviewer agent team in parallel over the full `git diff upstream/main..v1`, adjudicate and fix all Critical/High findings, apply a behavior-preserving code-simplifier pass, re-review the simplifier's diff with one focused Failure Mode pass, re-run the full test matrix, and only then report done. This phase produces review coordination and fixes — no new features.

## Key Insights

- The port's risk concentrates where fork semantics met rewritten upstream code: resend ambiguity taxonomy (Phase 2), cursor durability and live×catch-up overlap (Phase 7, the fork's own hardest bug class), fail-open ledgers (Phases 2–3), and importer transactionality (Phase 8). A single general review pass under-examines these; four adversarial lenses with file:line evidence requirements do.
- The diff baseline is exact: `git diff upstream/main..v1` (upstream pinned at 8ed4149 since Phase 1) — reviewers see ONLY fork-added changes, keeping findings attributable.
- Upstream style is the house style: fork additions that read as transplanted v0 code are a maintenance liability (future upstream merges). The simplifier pass must preserve behavior while making fork code read like the surrounding v1 code (error wrapping, logging prefixes, i18n usage, naming).
- The test matrix from Phase 10 is the regression net for simplifier changes: behavior-preserving is verified by identical green results, not by assertion.

## Requirements

**Functional**
- (a) Four code-reviewer agents spawned in parallel, each with a lens-specific brief and the full diff; every finding must cite file:line and include a concrete failure scenario (no style nits without impact).
- (b) Adjudication: each finding triaged Critical / High / Medium / Low; all Critical/High fixed before proceeding; Medium recorded with rationale (fix or explicit accept); Low logged.
- (c) One code-simplifier agent pass over fork-added code: behavior-preserving transformations only; any change it cannot prove preserving is flagged instead of applied.
- (d) Focused re-review of the simplifier's diff: ONE reviewer with the Failure Mode Analyst lens over just the simplifier's commits (`git diff` of the simplifier commit range) — the locked order is reviewers-then-simplifier, so without this step the simplifier's rewrites are never adversarially reviewed; its Critical/High findings are fixed before the matrix.
- (e) Full matrix re-run identical to Phase 10; any delta (new failure or removed failure) investigated.
- (f) Final report to the user: findings summary by lens and severity, fixes applied, simplifier changes (including re-review findings), matrix results, remaining known gaps, merge recommendation.

**Non-functional**
- Reviewer agents receive the research reports' hazard sections (researcher-01 §12 "WILL FIGHT THE PORT", researcher-02 port hazards) as priming — those are the known minefields; the lenses must look beyond them.
- No scope additions during fixes: a Critical finding may REQUIRE touching a file outside the planned set; everything else stays put.

## Architecture/Approach

Parallel lenses beat one sequential mega-review: independent blind spots, no anchoring, and the diff is small enough (~fork additions over a pinned base) for four full passes. Rejected: one reviewer with a checklist — anchoring and fatigue collapse coverage.

## Lens Briefs (spawn prompts)

1. **Security Adversary**: path traversal via `dir:`/filenames (sanitizer bypass — `..`, UNC, drive letters, unicode look-alikes, empty segments); whitelist bypass in new commands (/history //purge //status must inherit `checkPermission`; /status owner gate first-statement); regex injection (512-cap bypass, catastrophic backtracking on crafted captions); importer SQL (parameterized only; v0-sourced names as VALUES never identifiers); secrets in logs (file_reference, storage credentials, session paths); i18n template injection in user-controlled data.
2. **Failure Mode Analyst**: dedup fail-open paths that could fail CLOSED under error wrapping; cursor races (live×catch-up double-enqueue; cursor advancing past a failed enqueue; never-lower guard vs concurrent touches); resend ambiguous-failure misclassification minting duplicates or killing valid tasks; importer partial-failure (is the transaction truly atomic? is the marker written with the data?); migration 2 on every DB shape (fresh, v1-existing, imported); watchdog/meter false positives on slow-but-healthy downloads; middleware stack double-waiting; SkipLarge reporting regressions (upstream silently skipped → now surfaced: does any caller treat Skipped as failure?).
3. **Assumption Destroyer**: every mygotg API assumption (context goroutine-safety where Phase 7 dropped the copy trick; getHistory offset/anchor semantics; ResolveUsername availability without dialog cache); upstream-behavior assumptions (queue snapshot ordering for positions; `AddTask` persist-before-enqueue as the durability boundary; `Unscoped` unwatch semantics; settings load tolerating new columns); Windows assumptions (path separators in storage paths, dsn drive-letter handling for the importer's second DB handle); test-environment assumptions (e2e fake-RPC covering getHistory paths used by catch-up).
4. **Scope/Complexity**: dead ported code (journal remnants, minio remnants, unused v0 keys); duplicated upstream functionality (a second waiter, a second debounce, re-implemented exists checks); fork style vs v1 idiom divergence; over-engineering vs the locked decisions; missing pieces of locked scope (verify ALL locked feature groups present: watch extensions, dedup+ledger+history, ops commands, resend+SkipLarge+topics, guards, importer, images).

## Related Code Files

| Action | Path | Notes |
|--------|------|-------|
| Create | `plans/261110-1257-migrate-upstream-v1/reports/review-findings.md` | Adjudicated findings ledger (severity, file:line, decision) |
| Modify | any fork file | Only via Critical/High fixes or simplifier pass |

## Implementation Steps

1. Freeze: confirm phases 1–10 committed, working tree clean; `git diff upstream/main..v1 --stat` recorded.
2. Spawn four code-reviewer agents in parallel (one per lens brief above), each returning findings as `severity | file:line | scenario | suggested fix`.
3. Adjudicate into `reports/review-findings.md`; fix Critical/High (commit per finding cluster, referencing finding ids); Medium: fix if cheap, else record accept-rationale.
4. Spawn code-simplifier agent with the constraint brief (behavior-preserving; v1 idiom; flag-don't-change when unprovable); review its diff manually before committing.
5. Record the simplifier commit range; spawn ONE focused re-reviewer with the Failure Mode Analyst brief over `git diff <pre-simplifier>..<post-simplifier>` (the simplifier's diff only, not the full branch diff); adjudicate its findings like step 3 and fix Critical/High before the matrix — this closes the gap that the locked reviewers-then-simplifier order leaves the simplifier's rewrites unreviewed.
6. Re-run the full Phase 10 matrix; compare to Phase 10 recorded results.
7. Final report to the user (summary + pointer to the findings ledger + merge recommendation restating the user-gated merge).

## Todo

- [ ] Diff frozen and stats recorded
- [ ] 4 reviewer agents run in parallel, findings collected with file:line evidence
- [ ] Adjudication ledger written; Critical/High fixed (zero open)
- [ ] Simplifier pass applied and manually reviewed
- [ ] Simplifier's diff re-reviewed (one Failure Mode Analyst pass over the simplifier commit range); its Critical/High fixed
- [ ] Full matrix re-run, results identical-or-better than Phase 10
- [ ] Final report delivered to user (findings, fixes, matrix, merge recommendation)

## Verification

```bash
git status --porcelain          # clean tree before review
git diff upstream/main..v1 --stat
# after fixes + simplifier:
go build ./... && go vet ./... && go test ./...
go test -tags=e2e ./e2e && go test -tags=e2e ./e2e/telegram
go build -tags "no_jsparser,no_playwright" ./...
go build -tags "no_jsparser,no_playwright,sqlite_glebarez" ./...
```

Exit criteria: zero open Critical/High in the ledger, matrix green, report delivered. "Done" is observable: the ledger and the matrix output are artifacts, not opinions.

## Risk Assessment

1. **Review findings demand design rework** (low likelihood, high impact): e.g. cursor semantics flaw. Mitigation: that is the protocol WORKING — fix against the Phase 7 semantics spec (which encodes fork-proven behavior); if truly blocked, escalate to the user with options rather than improvising a redesign.
2. **Simplifier introduces subtle behavior change** (medium, high): mitigated by manual review of its diff + identical matrix results; any suspicious transformation is reverted, not argued.
3. **Lens sprawl / duplicate findings** (high, low): adjudication dedupes; findings mapped to one owner file each.

## Security Considerations

- Reviewer agents get read access to the repo, not credentials; the findings ledger must not paste secrets found in logs (reference file:line instead).
- Fixes for security findings take precedence over all other work in this phase and are verified with a targeted test where feasible (e.g. a traversal-payload test for sanitizers).

## Next Steps

Report done to the user with the merge recommendation; the v1→main merge itself remains a manual user decision after production validation.
