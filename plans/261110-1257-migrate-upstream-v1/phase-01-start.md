---
title: "Phase 1: Baseline & Branch Setup"
status: todo
phase: 1
priority: P2
effort: "2h"
dependencies: []
---

# Phase 1: Baseline & Branch Setup

## Overview

Cut the `v1` branch from `upstream/main` and prove the toolchain (build, vet, test) passes on Windows before any fork code lands, then stamp the two fork-identity touches (selfupdate repo, README note). Everything later assumes this verified baseline.

## Key Insights

- Upstream tip is 8ed4149 (v1.0.0-beta.5 line); fork `main` is at 6c87dc5, 49 commits on the v0 base 92e6072. `upstream/main` is fetched locally (verified: `git branch -a`).
- The v1 build is CGO-free on Windows: Makefile builds with `CGO_ENABLED=0` (Makefile:13-14), both SQLite drivers are pure-Go (`database/driver.go:1`, `database/driver_glebarez.go:1`), and DSN handling has explicit Windows drive-letter support (`database/dsn.go:24-28`).
- `go build ./...` compiles without the web UI because `web/dist/.gitkeep` is committed and embedded (`web/embed.go:5`); a UI-enabled binary needs pnpm, but tests do not.
- Selfupdate points at `config.GitRepo` (config/version.go:13); fork value is `vibecoder11200/SaveAny-Bot`, upstream's is `krau/SaveAny-Bot` — without this change `/update` and the update API would upgrade the binary to upstream releases, silently destroying the fork.

## Requirements

**Functional**
- Branch `v1` exists locally, based on 8ed4149 (`upstream/main`), and is the working branch for phases 2–10.
- Selfupdate target is the fork repo.

**Non-functional**
- Build/vet/test run without CGO, without Docker, without pnpm, on Windows Git Bash (Go 1.27.3).
- Baseline test inventory (packages with tests, pass/fail counts) recorded for regression comparison in later phases.

## Architecture/Approach

Minimal-touch phase: identity changes only (GitRepo constant + short README note); full docs polish is Phase 10. Rejected: porting any fork code in this phase — a clean verified baseline must exist so later regressions are attributable to fork commits only.

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Modify | `config/version.go` | `GitRepo = "vibecoder11200/SaveAny-Bot"` (line 13) |
| Modify | `README.md` | Short "this is a fork" banner paragraph; detailed docs in Phase 10 |

## Implementation Steps

1. From the repo root: `git switch -c v1 upstream/main` (verify `git log --oneline -1` shows 8ed4149).
2. Edit `config/version.go:13` to the fork repo value (source of truth: fork `main` config/version.go:13).
3. Add a brief fork note at the top of `README.md` (what the fork adds, link placeholder for the Phase 10 docs).
4. Run the verification commands below; record the baseline inventory: toolchain (`go version`), key module versions (`go version -m ./... | grep -E "mygotg|gorm|moejs|sqlite"`), and per-package test pass counts (`go test ./... 2>&1 | tail -40`). Keep this inventory in the phase-1 commit message so phases 2–10 can diff against it and pre-existing failures are never misattributed to fork commits.
5. Commit as a single commit, e.g. `fork: branch from upstream v1.0.0-beta.5, point selfupdate at fork repo`.

## Baseline Inventory (record these)

- `go version` output and OS (Windows 10, Git Bash).
- Module pins: `krau/mygotg`, `gorm.io/gorm`, `MoeForge/moejs`, sqlite driver versions from `go version -m`.
- `go test ./...` package-by-package result lines; flag any skip (e.g. ffmpeg-dependent) as pre-existing.
- Confirmation that `git status` on `main` is untouched and `v1` HEAD == 8ed4149.

## Todo

- [ ] Create `v1` branch from `upstream/main` (8ed4149)
- [ ] Point `config.GitRepo` at `vibecoder11200/SaveAny-Bot`
- [ ] Add fork banner to README (lightweight; Phase 10 does the rest)
- [ ] Run and record baseline: `go build ./...`, `go vet ./...`, `go test ./...`
- [ ] Record baseline inventory (toolchain, module pins, per-package results) in the commit message
- [ ] Confirm `CGO_ENABLED=0 go build ./...` also passes (the image-build condition from Phase 9)
- [ ] Confirm no pnpm/node step is needed for the Go matrix (empty-dist embed)
- [ ] Commit baseline; confirm `main` untouched

## Verification

```bash
git log --oneline -1 upstream/main   # expect 8ed4149
go build ./...
go vet ./...
go test ./...
go test -tags=e2e ./e2e              # optional baseline; builds real binary, no network
```

All green = done. If an upstream test fails on Windows out of the box, record it as a pre-existing baseline failure with output — do not fix upstream code in this phase.

## Risk Assessment

1. **Upstream moves under us** (low likelihood, medium impact): later `git fetch upstream` could tempt rebasing. Mitigation: pin to 8ed4149; no upstream pulls during the port; re-sync is a separate user decision after Phase 11.
2. **Windows-only test failures in upstream baseline** (medium likelihood, low impact): e.g. ffmpeg-dependent tests. Mitigation: record as pre-existing; research notes upstream tests avoid ffmpeg (`storage/telegram/video_split_test.go` skips/mocks).

## Security Considerations

- GitRepo pointing: ensures `/update` (client/bot/handlers/update.go:30) and the update API (api/update.go:58) pull signed fork releases, not upstream binaries — a wrong value is a supply-chain downgrade vector.
- No other code changes in this phase, so no new attack surface.

## Next Steps

Phase 2 (Storage & Task Foundations) — the DB migration #2 and storage/task primitives everything else consumes.
