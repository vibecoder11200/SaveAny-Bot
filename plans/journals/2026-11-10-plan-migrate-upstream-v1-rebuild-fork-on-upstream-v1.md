---
title: "Plan: migrate-upstream-v1 (rebuild fork on upstream v1)"
date: 2026-11-10
summary: "Hard-mode ak:plan for rebuilding the SaveAny-Bot fork on upstream v1.0.0-beta.5. 2 researchers (upstream v1 architecture + fork porting inventory), planner authored 11 phases (98h), 4-lens red team produced 26 accepted findings (2 Critical: same-dir migration mutates v0 DB pre-guard; sanitizer wiring layer) all applied, full-tier verification 58/60 claims verified, consistency sweep clean, plan validated and pinned. Open user decisions defaulted to Recommended: API tasks dedup OFF; stream keeps layer-1+record; no Docker CI workflow."
---

# Plan: migrate-upstream-v1 (rebuild fork on upstream v1)

Hard-mode ak:plan for rebuilding the SaveAny-Bot fork on upstream v1.0.0-beta.5. 2 researchers (upstream v1 architecture + fork porting inventory), planner authored 11 phases (98h), 4-lens red team produced 26 accepted findings (2 Critical: same-dir migration mutates v0 DB pre-guard; sanitizer wiring layer) all applied, full-tier verification 58/60 claims verified, consistency sweep clean, plan validated and pinned. Open user decisions defaulted to Recommended: API tasks dedup OFF; stream keeps layer-1+record; no Docker CI workflow.

> Historical work record — not durable authority. Prefer docs/specs/ADRs for current decisions.
