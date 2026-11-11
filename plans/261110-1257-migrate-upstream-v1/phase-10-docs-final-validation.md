---
title: "Phase 10: Docs, Migration Guide & Final Validation"
status: todo
phase: 10
priority: P2
effort: "6h"
dependencies: [phase-08-db-importer, phase-09-docker-images]
---

# Phase 10: Docs, Migration Guide & Final Validation

## Overview

Finish the fork's public surface — README, feature docs, the production v0→v1 migration guide, selfupdate pointing, i18n parity — then run the full verification matrix and write the merge proposal. This is the phase where "works on my branch" becomes "the user can migrate production and validate".

## Key Insights

- v1 docs live in a Hugo tree (`docs/content/`, built by the docs workflow, researcher-01 §10); the fork's added features deserve fork-authored pages that don't fight upstream's structure — a `docs/content/*/fork/` section or README-anchored docs are both viable; pick one and keep it consistent.
- i18n keys are generated: `go generate .` regenerates `common/i18n/i18nk/keys.go` from BOTH locale YAMLs and FAILS on parity mismatch (`cmd/geni18n/main.go:17-60`); the generated file is committed — parity is mechanically checkable.
- Full upstream CI matrix (`.github/workflows/ci.yml:29-101`): `go test ./...`, `go vet ./...`, `go test -tags=e2e ./e2e`, `go test -tags=e2e ./e2e/telegram`; e2e builds the real binary and boots it on a free port with a temp data dir (`.exe` handled); telegram e2e runs against an in-memory fake RPC server, no network. All runnable on this Windows box.
- Selfupdate: `config.GitRepo` (config/version.go:13) was pointed at the fork in Phase 1 — this phase verifies release compatibility (update flow checks `selfupdate.CheckTarget`, api/update.go:72-77) and documents that fork releases must carry the fork's own tags.
- Migration entry points (from Phases 8–9): `--config <old config.toml>` (upstream TOML import, once), `--fork-db <old saveany.db>` (our DB import, once), MinIO-as-s3 settings. The ONLY supported path is a FRESH data dir + `--config` + `--fork-db <copy-of-v0-db>`: same-data-dir reuse is forbidden (Phase 8's boot guard refuses v0 DBs before `database.Init` can mutate them), and `--config` is MANDATORY for a meaningful `--fork-db` import (TOML-imported storages rows feed the name→ID mapping; without them the importer refuses rather than writing a hollow all-skips marker). Bot/userbot sessions are NOT portable (v1 drops v0 session files with a warning, settings/legacy.go — re-login is a documented step, bot via BotFather token, userbot via web console login).

## Requirements

**Functional**
- README: fork section listing every ported feature group with one-line usage; link to detailed docs; images section (micro/pico) with the degraded-feature caveats from Phase 9.
- Docs: watch extended syntax page (full grammar + semantics: catch-up behavior, cursor guarantees, album handling, per-watch overrides); ops commands page (/status /id /history /purge + queue positions); dedup page (3 layers, fail-open semantics, /purge purpose).
- `docs/fork/migration-v0-to-v1.md` — PRODUCTION MIGRATION GUIDE: (1) backup the entire v0 data dir + config.toml; (2) stop the v0 bot AND drain the task queue before cutover (wait for running/queued tasks to finish or cancel them — in-flight `pending_tasks` rows are NOT migrated, and the import summary warns with the exact count); (3) COPY the v0 DB aside — "copy, never point at, the v0 DB" — or checkpoint WAL; (4) deploy the v1 fork binary/image with a FRESH data dir + `--config <old config.toml>` + `--fork-db <copy-of-v0.db>`: the same-data-dir option is REMOVED entirely (unsupported — `database.Init` would mutate the v0 file before any guard; Phase 8's boot guard refuses such boots), and `--config` is MANDATORY for a meaningful `--fork-db` import (it creates the `storages` rows the name→ID mapping needs); (5) first boot: watch logs for TOML import, minio→s3 migration, fork DB import summary (including the pending-tasks warning if the queue was not drained); (6) web console: set admin password, re-add bot token, log in userbot; (7) verification checklist: watches listed with cursors, `/history stats` shows ledger totals, one test save (dedup skip on repeat), one test watch catch-up after restart (manual restart check, kept alongside Phase 7's e2e watch tests), MinIO save via s3; (8) rollback: the v0 data dir is untouched by construction (v1 never opens it) — explicit rollback = rerun the v0 binary on the old dir; rollback to upstream v1 = the downgrade snippet below.
- Migration lock-in + downgrade snippet: once migration 2 is applied, upstream v1 binaries fatal on the fork DB (`migrate()` aborts when DB version > `CurrentSchemaVersion`, `database/migration.go:62-64`). The guide documents this lock-in and provides the rollback-to-upstream path as a snippet — delete the version-2 `schema_migrations` row and drop the fork tables — plus the guard invariant: selfupdate must never select upstream releases (the Phase 1 `config.GitRepo` fork constant is the guard).
- Selfupdate verified: version check hits the fork repo; documented release process (tag → binaries → GH release).
- i18n parity: `go generate .` clean, keys.go committed, no orphan keys (fork keys for dropped v0 features pruned).
- Full matrix green: build, vet, unit, e2e api, e2e telegram, both image tag combos compile.
- `MERGE-PROPOSAL.md` in the plan folder (not the repo): summary of changes, migration readiness, and the explicit recommendation that the USER runs the production migration and validates before `git merge v1` into `main`. It also documents two known resume regressions accepted by locked decision (upstream native resume wins): (a) completed-download resume state is wiped at boot when no tasks are queued (the fork spared it 7 days), and (b) v1 keys cache by taskID, so a failed task's partial download is not reusable by a retry (the fork reused it across tasks); plus one line noting CI Docker image builds as a possible post-validation follow-up (locked out of scope in Phase 9).

**Non-functional**
- No Vietnamese in shipped docs (fork repo language is English; user-facing).
- Docs must be versionable with the branch (live in the repo).

## Architecture/Approach

- Docs in-repo under `docs/fork/` (plain markdown, linked from README) rather than upstream's Hugo tree: fork pages shouldn't break upstream docs builds, and Hugo nav registration is upstream-owned surface. Rejected: editing upstream Hugo config for fork nav — merge friction with zero gain.
- Merge proposal as a plan-folder artifact, not a repo file: the repo stays clean of process documents; the plan folder is the coordination surface.

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Modify | `README.md` | Fork section (Phase 1 banner expanded) |
| Create | `docs/fork/watch.md`, `docs/fork/ops.md`, `docs/fork/dedup.md` | Feature docs |
| Create | `docs/fork/migration-v0-to-v1.md` | Production migration guide |
| Modify | `common/i18n/locale/{en,zh-Hans}.yaml` + `common/i18n/i18nk/keys.go` | Parity closure via `go generate .` |
| Create | `plans/261110-1257-migrate-upstream-v1/MERGE-PROPOSAL.md` | Plan-folder artifact (not repo) |

## Implementation Steps

1. Write the three feature docs from the actual implemented behavior (read the phase 2–7 code as written, not as planned — docs match reality).
2. Write the migration guide following the Key Insights sequence; include the exact boot flags, the expected log lines for each import, and the rollback section — with the fresh-dir-only path (same-data-dir removed), the queue-drain step, the "copy, never point at, the v0 DB" backup step, the `--config`-is-mandatory note, and the upstream-downgrade snippet with the migration lock-in explanation and the selfupdate guard invariant.
3. README: expand the Phase 1 banner into the fork section; add images + docs links.
4. i18n closure: grep for `i18nk.` references vs keys.go; prune orphaned keys (v0 leftovers the fork no longer uses); `go generate .`; commit regenerated keys.
5. Selfupdate verification: run `go run . version` style check (cmd/version.go:38 uses DetectLatest against config.GitRepo) in a sandbox where it cannot self-apply; confirm it queries the fork repo.
6. Run the full matrix (verification block); record outputs.
7. Write MERGE-PROPOSAL.md: diff stats vs upstream/main, feature inventory, known gaps (anything the hardening audit marked not-needed plus any Phase 4 degradation), the two known resume regressions (Phase 2 step 7(e) locked decision), the one-line CI-images follow-up note, migration readiness, user-validation checklist.

## Todo

- [ ] Feature docs (watch / ops / dedup) matching implemented behavior
- [ ] Production migration guide with verification checklist + rollback: fresh-dir-only path (same-data-dir removed), queue-drain step, copy-never-point-at backup, `--config`-mandatory note, upstream-downgrade snippet + migration lock-in + selfupdate guard
- [ ] README fork section + image caveats
- [ ] i18n parity closed, orphans pruned, keys regenerated
- [ ] Selfupdate target verified against fork repo
- [ ] Full matrix executed and recorded (outputs attached to the phase commit)
- [ ] MERGE-PROPOSAL.md written (user gate before merge)

## Verification

```bash
go build ./... && go vet ./...
go test ./...
go test -tags=e2e ./e2e
go test -tags=e2e ./e2e/telegram
go build -tags "no_jsparser,no_playwright" ./...
go build -tags "no_jsparser,no_playwright,sqlite_glebarez" ./...
go generate . && git status --porcelain common/i18n/   # clean after commit
```

## Risk Assessment

1. **Docs drift from implementation** (medium likelihood, low impact): phases evolve under review. Mitigation: step 1 reads the final code; Phase 11's Scope lens re-checks docs claims.
2. **Matrix red on e2e due to environment** (medium, medium): e2e boots real binaries; Windows firewall/port issues can flake. Mitigation: e2e picks a free port and temp dir by design (e2e/main_test.go:1-70); if an environment failure is identified, document and retry once on a different port rather than skipping.
3. **Merge pressure before user validation** (process risk): the plan's contract is user-gated merge. Mitigation: MERGE-PROPOSAL.md states it; Phase 11 reiterates; `main` stays the v0 line until the user says otherwise.

## Security Considerations

- Migration guide must instruct copying (not moving) the v0 data dir, and must warn that the v0 dir contains session files (secrets) — backups should be protected accordingly.
- Docs must not include real chat IDs, storage credentials, or the user's MinIO endpoint.

## Next Steps

Phase 11 (mandatory post-cook review) runs on the finished branch: reviewer team, fixes, simplifier, re-test, final report.
