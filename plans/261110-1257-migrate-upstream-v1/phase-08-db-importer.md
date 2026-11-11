---
title: "Phase 8: v0→v1 Data Importer"
status: todo
phase: 8
priority: P1
effort: "12h"
dependencies: [phase-02-storage-task-foundations, phase-03-dedup-history-purge, phase-07-watch-extensions]
---

# Phase 8: v0→v1 Data Importer

## Overview

Write the one-shot v0-DB→v1-DB importer (`settings/legacy_db.go`) that carries production data — watch rows with cursors, rules, dirs, the dedup ledger, the telegram ledger — into the v1 schema, mirroring upstream's `ImportLegacy` shape: marker-guarded, single transaction, idempotent, with explicit per-table collision/skip policies. Fork production runs the v0 fork; the data must survive.

## Key Insights

- Template exists upstream: `settings/legacy.go` — marker key `meta.legacy_config_imported` written inside the same transaction as the imported data (legacy.go:23, 203-207); `boot.Init` calls it once at boot.go:36-43 (verified). Our importer copies that shape with `meta.fork_db_imported` and slots in immediately after the TOML import and `MigrateMinioStorageType` — ordering matters: the config import populates `storages` rows, which our name→ID mapping needs, and both run before `settings.NewService` so imported user rows land in the loaded config.
- v0 schema (researcher-02 §10, verified against fork `database/model.go`): `users` (chat_id, silent, default_storage, default_dir, apply_rule, watch_notify, filename_strategy, filename_template, conflict_strategy, media_filter), `dirs` (user_id=users.id, storage_name, path), `rules` (user_id, type, data, storage_name, dir_path), `watch_chats` (user_id=users.id, chat_id, filter, media_type, storage_name, dir_path, last_message_id nullable; partial unique), `saved_file_dedups` (user_id = CHAT ID int64, storage_name, file_unique_id, content_hash, size, storage_path, chat_id, message_id), `telegram_saved_files` (storage_name, storage_path).
- **Two different "user id" semantics in v0** (the importer's core hazard): `watch_chats.user_id`/`rules.user_id`/`dirs.user_id` are v0 users table row IDs (uint); `saved_file_dedups.user_id` is the Telegram chat ID (int64). Both must map to v1 `users.id`.
- v1 users come from the TOML config import (whitelist) — v0 users created later via /start may not exist in v1; the importer must create missing user rows (defaults + mapped flags, with `Enabled` gated on TOML whitelist membership — see Requirements) so watches/dirs/rules/dedup rows have owners.
- `storages.name` is unique in v1 (`database/model.go:59-68`); `MigrateMinioStorageType` (boot.go:41-43) has already rewritten fork-era `minio` rows to `s3` — both TOML-imported storages and migrated ones are resolvable by their v0 names.
- Same-filename hazard: v0 default DB path was `data/saveany.db` (viper `db.path`, researcher-02 §10) and v1's default is ALSO `<data-dir>/saveany.db` (`boot/flags.go:44-56`). Pointing `--data-dir` at the old directory would make v1 adopt the v0 file as its own DB. The importer must distinguish: v0 DBs have `saved_file_dedups`/`telegram_saved_files` tables and NO `schema_migrations`/`storages` tables. Worse, upstream boot order runs `database.Init` (opens AND migrates the active DB, `database/db.go:17-44` → migration.go AutoMigrate) BEFORE any importer hook — so pointing `--data-dir` at the old v0 dir mutates the v0 production file (adds v1 tables/columns) before any `smellsLikeV0`/refusal guard inside the importer can run, permanently defeating the import and corrupting the only rollback artifact. The guard must therefore be a BOOT-TIME check in `boot/boot.go` BEFORE `database.Init` (Requirements); the importer's own discovery/refusal logic stays as defense-in-depth.

## Requirements

**Functional**
- Boot-time v0-refusal guard (phase-critical): in `boot/boot.go` BEFORE `database.Init`, open the resolved DB path read-only via sqlite; if it contains v0 markers (e.g. `pending_tasks`/`saved_file_dedups` tables) and lacks `schema_migrations`, REFUSE to boot with a remediation message telling the user to use a fresh data dir + `--config` + `--fork-db <copy-of-v0-db>`. This prevents `database.Init` from migrating — and thereby corrupting — a v0 production file; the importer's own discovery/refusal logic stays as defense-in-depth.
- `settings/legacy_db.go`: `ForkDBImported(ctx) bool` + `ImportLegacyDB(ctx, srcPath string) error` mirroring the legacy.go pattern (marker inside the same `database.Transaction`).
- Discovery: explicit `--fork-db <path>` flag (new, `boot/flags.go` + env `SAVEANY_FORK_DB`) > auto-detect `<data-dir>/saveany.db` when it smells like v0 (per Key Insights) AND the fork marker is absent AND it is not the v1 DB path. Refuse (error log, no import) when candidate == active v1 DB path.
- Read v0 sqlite read-only with WAL support: open `file:<path>?mode=ro` via the same pure-Go driver (`glebarez` or ncruces per build tag — reuse `database.GetDialect` machinery or open a second gorm session); tolerate missing fork tables (v0 base without them → import base tables only). Also tolerate MID-FORK production schemas: fork releases before commits 62d3049/c7f3583 lack the watch extension columns (`media_type`/`storage_name`/`dir_path`/`last_message_id`) and `users.media_filter` — probe `PRAGMA table_info` per table and build each column list dynamically (absent column ⇒ default value, warning counted).
- Mappings: v0 user row → v1 user (by chat_id; create missing with mapped silent/apply_rule/filename/conflict/media_filter/watch_notify). Created users get `Enabled = true` ONLY if their chat_id is in the imported TOML whitelist (mirror `settings/legacy.go`≈198-203, which disables non-TOML users) — v1's `User.Enabled` defaults true (`database/model.go:25`) and v0 never deleted rows on whitelist removal, so blind creation would auto-whitelist every v0 DB user including soft-deleted ones; skip v0 soft-deleted users AND their dependent rows with counters. `storage_name` → `storages.id` via name lookup; per-table skip policies with counters for unresolvable names (never fatal).
- Import: `watch_chats` (skip `deleted_at NOT NULL` rows; import-time validation block — msgre regex must compile AND be ≤ 512 chars (invalid/overlong → drop the filter, keep the watch, count); `media_type` parsed with the fail-closed grammar (invalid → clear the column, keep the watch, count); `dir_path` run through `SanitizeRelDirPath` (invalid → clear the column, keep the watch, count); the compile-once regex cache and flush-time sanitize apply to ALL rows regardless of origin; storage/cursor mapped; collision on (user, chat): keep higher cursor); `rules` (unresolvable storage → import as dir-only rule IF v1 rule semantics allow StorageID 0, else skip + count — verify in step 2); `dirs` (OnConflict DoNothing on the v1 unique triple); `saved_file_dedups` (user by CHAT ID; OnConflict DoNothing against the Phase 2 partial unique indexes — canonical shape cited in Phases 2 and 3); `telegram_saved_files` (name→ID; OnConflict DoNothing on its plain unique `(storage_id, storage_path)`).
- `pending_tasks` (v0 in-flight journal rows at cutover) are NOT imported as TaskRecords (out of scope, KISS): the summary surfaces the pending-row count with an explicit "these N in-flight tasks were not migrated" warning; the Phase 10 guide instructs draining the task queue before cutover.
- Refuse-on-hollow-import: the name→ID mapping depends on TOML-imported `storages` rows (`settings/legacy.go:121-131`); `--fork-db` without `--config` would produce an all-skips import that still writes the permanent marker. Before writing the marker, compare the resolved-storage count against v0 `storage_name` references: if ZERO storages resolve while v0 referenced any names, REFUSE the import with a boot-visible error naming the missing `--config` step and DO NOT write the marker.
- Boot log summary: per-table imported/skipped counts, pending-tasks warning, + marker timestamp.
- Idempotent: second boot with same DB is a no-op (marker); re-import after manual marker deletion duplicates nothing (OnConflict everywhere).

**Non-functional**
- The v0 file is NEVER written (read-only handle; recommend a clean shutdown or `PRAGMA wal_checkpoint` before import — document it). The boot guard extends this guarantee to the pre-`database.Init` window, where AutoMigrate would otherwise write v1 tables into a v0 file.
- Importer failure leaves v1 DB unchanged (single transaction) and does not block boot (log error, continue — same tolerance as the TOML importer at boot.go:36-39).

## Architecture/Approach

- **Read v0 directly, no TOML round-trip**: v0 DB is plain sqlite with a known schema; a direct read avoids lossy config-file re-encoding. Rejected: exporting v0 to TOML and reusing `ImportLegacy` — fork tables (dedup ledger, cursors) have no TOML representation.
- **Marker + idempotent upserts (both)**: the marker prevents wasted work; OnConflict DoNothing guarantees correctness even if the marker is deleted. Rejected: marker-only — a crashed-then-retried import must be safe.
- **Per-table skip policies over a staging layer**: unresolvable storages are skipped with counts, not fatal, not silently-dropped. Rejected: a full staging/validate/commit pipeline — single-user data volume, transaction already gives atomicity.

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Create | `settings/legacy_db.go` | Importer (marker, mappings, policies, summary) |
| Create | `settings/legacy_db_test.go` | Fixture-DB tests (below) |
| Modify | `boot/flags.go` | `--fork-db` flag + `SAVEANY_FORK_DB` env |
| Modify | `boot/boot.go` | v0-refusal guard BEFORE `database.Init` (read-only sqlite probe for v0 markers; refuse with remediation message); call `ImportLegacyDB` after TOML import + MinIO migration (≈boot.go:39-44) |
| Modify | `cmd/run.go` (flag plumbing only) | Pass fork-db option through |

## Implementation Steps

1. Add the flag/env (mirror `boot/flags.go:11-26,44-56` patterns); plumb into `boot.Options`. Add the boot-time guard: in `boot/boot.go` BEFORE `database.Init`, read-only-open the resolved DB path via sqlite and refuse to boot on v0 markers (see Requirements) with the remediation message (fresh data dir + `--config` + `--fork-db`).
2. Verify two v1 semantics before finalizing policies: (a) can a `Rule` have `StorageID = 0` and still match dir-only (read `ruleutil.ApplyRule` + rule model usage); (b) does `User` creation require any v1-mandatory fields beyond chat_id (read the users CRUD path used by `ImportLegacy` for the minimal row shape).
3. Write `legacy_db.go`: open v0 readonly; `smellsLikeV0` check (fork tables present, `schema_migrations`/`storages` absent) as defense-in-depth; probe `PRAGMA table_info` per table and build column lists dynamically (mid-fork schemas without watch extension columns / `users.media_filter`); build maps (chat_id→v1 user incl. created-missing with `Enabled` gated on TOML whitelist membership; skip soft-deleted users + dependents with counters; name→storage ID); run the watch validation block (msgre compile + ≤512; media_type fail-closed parse; dir_path `SanitizeRelDirPath`); inside one `database.Transaction`: insert all rows per policies; count `pending_tasks` rows for the not-migrated warning; REFUSE (no marker) when zero storages resolve while v0 referenced names; otherwise write `meta.fork_db_imported` = RFC3339.
4. `boot.Init` integration: after `MigrateMinioStorageType`, guarded by `!ForkDBImported` and discovery rules; log summary (per-table counts, pending-tasks warning) at Info.
5. Tests with a fixture v0 DB created in-test by raw SQL DDL (v0 schema from Key Insights; include 2 users, 2 storages-worth of name references [one resolvable, one not], 3 watches [one soft-deleted, two colliding on user+chat with different cursors], rules, dirs, dedup rows keyed by chat-id, telegram_saved rows): assert row counts, marker, skip counters, no writes to the fixture (open a copy, compare bytes). Second run: no-op. v0-base variant (no fork tables): imports only base tables. MID-FORK variant "fork v0.x without watch extension columns": base-columns-only import with warnings counted. Enabled-gating assertion: NO created user row has enabled=1 without TOML membership. Hollow-import variant (no `--config`, zero resolvable storages while v0 referenced names): refuses, marker absent. Wrong-file variant (v1-shaped file): refuses.
6. Verification run against a COPY of the real production DB (user provides backup copy) with counts eyeballed against `/history stats` after Phase 10.

## Todo

- [ ] `--fork-db` flag + env + plumbing
- [ ] Boot-time v0-refusal guard before `database.Init` (read-only probe + remediation message)
- [ ] v1 semantics verified (rule StorageID=0, minimal user row)
- [ ] Importer with marker/transaction/mappings/policies/summary; created users `Enabled` only for TOML-whitelist chat_ids; soft-deleted users + dependents skipped with counters
- [ ] Watch import validation block (msgre compile + ≤512, media_type fail-closed, dir_path sanitize) with counters; compile-once regex cache + flush-time sanitize stated to cover all rows
- [ ] Hollow-import refusal: zero resolvable storages while v0 referenced names → boot-visible error, no marker
- [ ] `PRAGMA table_info` dynamic column probing for mid-fork schemas
- [ ] `pending_tasks` count surfaced as "these N in-flight tasks were not migrated" warning (never imported)
- [ ] boot integration with discovery + refusal guard (defense-in-depth)
- [ ] Fixture tests: happy path, idempotent re-run, unresolvable-storage skips, soft-delete skip, collision policy, v0-base subset, mid-fork variant without watch extension columns, Enabled-without-TOML assertion, hollow-import refusal, wrong-file refusal, readonly guarantee
- [ ] Dry-run against a copy of production data; counts cross-checked with `/history stats`

## Verification

```bash
go build ./... && go vet ./...
go test ./settings/... -run LegacyDB -v
# manual: copy production v0 db aside, then
go run . --data-dir <tmp> --config <old-config.toml> --fork-db <copy-of-v0.db> --no-web
# expect boot log: storage rows imported, per-table counts, marker written; ctrl-C
sqlite3 <tmp>/saveany.db "SELECT key FROM settings WHERE key LIKE 'meta.%';"
```

## Risk Assessment

1. **WAL-readonly read fails on a hot v0 DB** (medium likelihood, medium impact): reading a WAL database read-only requires readable `-wal`/`-shm`; a copied-while-running DB may be inconsistent. Mitigation: document "stop the v0 bot (or checkpoint) before copying"; importer treats unreadable WAL as a clean skip-with-error, never a partial import.
2. **User-mapping gaps orphan rows** (low, high): a dedup row whose chat-id has no v0 users row (deleted user) has no owner. Policy: skip + count (never invent users from ledger rows); surfaced in the summary.
3. **Collision on (user, chat) watches** (low, medium): only possible when v0 held duplicates despite its partial index (pre-index legacy). Policy in step 3 (highest cursor wins) matches the fork's own sweep rule (researcher-02 §1g).

## Security Considerations

- Read-only handle on the v0 file (mode=ro DSN) — the importer must never mutate production history; test asserts byte-identical fixture.
- Do not log ledger contents (paths may be sensitive); counts only.
- The importer runs post-auth-creation but pre-web-start in boot; no network exposure. Refuse-by-default on ambiguous files protects against importing a v1 DB into itself (unique-violation storm or worse, silent row merging).

## Next Steps

Phase 10 uses this importer for the production migration guide; its dry-run output feeds final validation.
