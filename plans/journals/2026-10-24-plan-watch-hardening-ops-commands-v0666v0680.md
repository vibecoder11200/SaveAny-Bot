---
title: "Plan: watch hardening + ops commands (v0.66.6→v0.68.0)"
date: 2026-10-24
summary: "ak:plan --hard complete: 11 owner decisions, red-team 15 findings applied, 5-phase 3-release plan ready for cook"
---

# Plan: watch hardening + ops commands (v0.66.6→v0.68.0)

## What happened
Ran ak:plan --hard for the watch/ops gap-closing batch confirmed by the owner (watch A+B+C+D, ops A+B+C, bugfix batch; HTTP resume deferred).

- Interview: 3 rounds, 11 decisions logged in plan.md Decisions Log (catch-up cap 500, storage:/dir: flags, /history full scope incl. source cols, /update→fork+GH releases, always-group albums, album folder first-file name with -HHMMSS collision suffix, full msgre parity, 3 releases, summary notify, first-boot cursor-only).
- Research: gotd v0.149.0 catch-up recipe (raw MessagesGetHistory forward paging OffsetID/AddOffset=-limit; query iterator drops MinID/MaxID — unusable); upstream fork-drift (fork 100% synced at merge-base 92e6072; upstream lacks catch-up/history/per-watch storage → additive columns rebase-safe; roadmap #251 gotgproto replacement → keep gotgproto at handler boundary).
- Plan: plans/261024-1145-watch-ops-hardening/ — 5 phases, 73 tasks, 3 releases (v0.66.6 bugfix, v0.67.0 watch, v0.68.0 ops), reports in plans/reports/.
- Red team: 3 hostile reviewers, 29 findings → 15 accepted (all applied): no path sanitizer existed for dir: (new SanitizeRelDirPath deliverable); cursor must advance only after durable enqueue (album flush), not emit; msgre must filter per GroupedID not per member; batch core extraction is an EXTENSION (ForceGrouping + Override + nil-safe UI sink + non-interactive strategy + ≤1-keep); userbot already has FLOOD_WAIT SimpleWaiter (no manual wrapper); PeerStorage marked-key miss → reuse tryGetInputPeer; VERSION must drop leading 'v' (semver.Parse); GH releases need binary+checksums assets (DetectLatest skips asset-less/prerelease); /status admin-gated; per-release DB backup (AutoMigrate is Fatal).
- 3 post-red-team questions got no UI answer → recommended dispositions applied by best judgment and recorded in plan.md Red Team note (reversible).

## Decision
Ship as 3 independently verified releases. Shared batch-core changes /save behavior in 2 documented ways (≤1-file album groups kept instead of silently dropped — latent bug fix; collision suffix on album dirs). Single-user deployment assumption documented; /status gated to first whitelisted user.

## Next steps
- /ak:cook D:\SaveAny-Bot\plans\261024-1145-watch-ops-hardening\plan.md (Phase 2 first: bugfix batch → v0.66.6)
- Owner may override the 3 best-judgment calls before cook (apply-all findings, /save spillover fixes, single-user assumption).
- Optional: run /save @phimsexvietnama fresh range on 0.66.5 to check album composition logs (still outstanding from previous session).

> Historical work record — not durable authority. Prefer docs/specs/ADRs for current decisions.
