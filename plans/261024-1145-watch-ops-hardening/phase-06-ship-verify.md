---
title: "Phase 6: ship & prod verify (3 releases)"
status: done
---

# Phase 6: Ship & prod verify — v0.66.6 → v0.67.0 → v0.68.0

## Context Links

- Deployment runbook (established, from journal 2026-10-24): sed CRLF entrypoint.sh → docker build with `VERSION/GitCommit/BuildTime` ldflags → docker save → scp → docker load → sed compose image → `docker compose up -d --force-recreate` → verify `docker exec saveany-bot /app/saveany-bot version`.
- Prod: root@160.30.113.48 (key `C:\Users\ankha\.ssh\anvps-meghost`), compose at /opt/saveany-bot; /app/{cache,config.toml,data,downloads} bind-mounted from /opt/saveany-bot.
- Decisions: plan.md #4 (GH release per ship), #8 (3 releases)

## Overview

Ship each phase as its own tagged release with GitHub release + docker deploy + explicit prod verification, so regressions isolate to one feature group.

## Release process (each version)

1. Gate: `go run ./cmd/geni18n` clean diff → `gofmt -l` empty → `go vet ./...` → `go test -race ./...` (skips: `TestCreateSplitZip|TestExtractThumbFrame|TestGetVideoMetadata`) → `GOOS=linux go build ./...`.
2. Commit discipline: stage files INDIVIDUALLY (no `git add -A`); confirm `git check-ignore plans/ plans/reports/` both ignored before push (public repo — plans contain server details).
3. Tag `v<X>-fork` + `gh release create` — **release MUST carry assets** (F5/S8): the linux-amd64 binary (filename with `_linux_amd64` suffix — `ghselfupdate.DetectLatest` skips asset-less releases entirely → /update would report "no release found") + a `checksums.txt` (sha256). **Never `--prerelease`** (DetectLatest skips prereleases). Notes from the phase summary only — no server details.
4. Docker build with ldflags — **`VERSION=<X>-fork` WITHOUT the leading `v`** (F5/A6: `semver.Parse(config.Version)` in update.go:22 rejects a leading `v`; with `v0.66.6-fork` /update dies at "version var invalid") + GitCommit + BuildTime → save/scp/load → compose up -d --force-recreate → `docker exec saveany-bot /app/saveany-bot version` must print a parseable semver (`0.66.6-fork`).
5. **Pre-deploy DB copy EVERY release** (F9 — AutoMigrate is `logger.Fatal` on failure → crash loop; 0.68.0 migrates too, not just 0.67.0): `cp /opt/saveany-bot/data/*.db /opt/saveany-bot/data/backup-pre-<X>.db`.
6. `docker logs saveany-bot --since 2m` clean boot (AutoMigrate lines OK, catch-up summary for v0.67.0).

## Prod verification checklists

**v0.66.6 (Phase 2)**
- [ ] `/help` — no `/import`; lists /dl /aria2dl /ytdlp /transfer /cancel /id /purge.
- [ ] `/update` — checks vibecoder11200/SaveAny-Bot: with the just-created release (binary+checksums assets attached) it reports "latest" for this version; Docker mode does NOT offer the button; `saveany-bot version` output parses as semver (no leading `v`).
- [ ] `/watch @testchannel msgre:(?i)^video:\s*\d+` (colon inside regex) → created; `/lswatch` shows the regex intact; a matching caption saves.

**v0.67.0 (Phases 3+4)**
- [ ] `/watch @test storage:<local-storage> dir:wtest msgre:...` → files land in that storage/dir (not user default).
- [ ] Album in watched channel → ONE batch task in `/task running` (`[tgfiles](N files/…MB)`); Telegram-storage watch → received as ONE album message; local storage → subfolder = first-file base; post a second album with same first-file name → folder gets `-HHMMSS` suffix (owner's collision concern).
- [ ] msgre matching a filename-only term (no caption) → saves (parity proof).
- [ ] Catch-up: post messages to a watched channel → `docker restart saveany-bot` → bot up → summary message per watch arrives AFTER album flush; missed files saved; **compare the STORAGE LISTING before/after (not the dedup table — same file_unique_id yields one row even if two files exist)**: exactly the new files, no duplicates, no `name (1)` renames; second restart with no new messages → NO summary (silent) and no fetch loop in logs.
- [ ] Existing pre-0.67.0 watches: first boot after upgrade logs cursor-init (no backfill), still save live messages as before.

**v0.68.0 (Phase 5)**
- [ ] `/status` — version/commit, uptime ≈ container age, workers, queued/running, cache+resume size (cross-check `du -sh /opt/saveany-bot/cache`), disk free, watch count.
- [ ] `/history` lists recent saves (name/storage/dir/size/time; source chat for new TG saves, "—" for pre-0.68.0 rows); `/history 2` paginates; `/history search <term>` filters; `/history stats` aggregates by storage + day.
- [ ] `/task queued` shows `#1 #2 …` positions with truncation footer.

## Requirements

- [ ] All 3 releases shipped, each gate-green before build, each with GH release + notes.
- [ ] Every checklist item verified on prod; failures → fix-forward patch release (v<X>.1) with same process.
- [ ] Journal addendum per release (plans/journals/2026-10-24-*.md append).

## Risk Assessment

- Regression isolation is the point of 3 releases — do NOT batch skips. If a verify step fails, stop, diagnose against that release only, patch-forward.
- AutoMigrate runs at first boot of EVERY release (v0.67.0 adds watch_chats cols; v0.68.0 adds saved_file_dedup cols) and is Fatal on failure — the per-release DB copy (step 5) is mandatory, not optional.
- /update + GH release: fork releases are public — notes must NOT include server IP/paths (plans/ stay local; write notes from phase summaries only). Binary + checksums assets are REQUIRED for DetectLatest; the checksums asset is the integrity story for non-Docker /update users (validator wiring is future hardening — noted, not blocking).

## Security Considerations

- Release notes public → no secrets, no server details, no user data. SSH deploy key usage unchanged. `.gitignore` verified before each push.

## Next Steps

- After v0.68.0 verify: retro + `ak plan archive`; upstream re-sync window (pull upstream/main per research recommendation).
