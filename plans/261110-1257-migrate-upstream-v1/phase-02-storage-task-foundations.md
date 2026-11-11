---
title: "Phase 2: Storage & Task Foundations"
status: todo
phase: 2
priority: P1
effort: "16h"
dependencies: [phase-01-start]
---

# Phase 2: Storage & Task Foundations

## Overview

Land, in one phase, every primitive later phases consume: DB migration #2 adding ALL fork tables/columns at once, TG-storage hardening (audited against v1 code first — port only what v1 still lacks), the SkipLarge sentinel reported through item results, forum-topic routing, resend-by-id with its ambiguous-failure taxonomy, the saved-files ledger, sanitizers, and `TGFile.FileUniqueID`.

**Internal milestones (dependency split, see plan.md):** M1 = steps 1–3 plus the table/helpers half of step 6 (migration 2, FileUniqueID, sanitizers, telegram_saved_files ledger tables) — unblocks phases 3/4/6. M2 = steps 4–5, the recording half of step 6, and steps 7–9 (SkipLarge, forum topics, ledger recording, hardening audit, resend, file_reference refresh) — blocks only phase 7. Step 10 closes both.

## Key Insights

- Migration is versioned, not bare AutoMigrate: `schema_migrations` table, entries `{version, up}` run in a transaction, currently only `{1, baselineSchema}`, `CurrentSchemaVersion = 1` — `database/migration.go:8-38`. Fork adds `{2, forkTables}` and bumps the constant (verified by reading upstream migration.go).
- v1 identity is storage **ID**: `storage.ID(st)` maps instance→DB id (`storage/load.go:58-66`); `TaskParams` already persists `StorageID` (`core/tasks/tfile/params.go:40-44`); `storages.name` is unique 64 chars (`database/model.go:59-68`) — this name column is the import-time mapping key (Phase 8).
- v1 tfile upload path: download to cache (`core/tasks/tfile/execute.go:38-42`), stat (≈:49), upload in retry loop calling `storage.SaveWithPath`/`SaveWithProgress` (≈:53-90), `core.MarkTaskElementDone` (≈:97), metadata sidecar, cache removal (≈:99-103). Batch: `recordDownloadComplete` per item then `saveBatchItems` → `SaveBatchWithProgress`/`SaveBatch` (`core/tasks/batchtfile/execute.go:218-280`, verified). CRITICAL ordering fact: `execute.go:29-31` dispatches `if t.stream { return executeStream(...) }` BEFORE all of these regions — every fork hook (dedup file-id check, resend, SkipLarge pre-check) must sit ABOVE the stream dispatch, mirroring fork ordering (dedup file-id check → tryResend → SkipLarge pre-check → THEN the stream branch); with `config.Stream = true` anything wired only into the download/stat/upload regions silently vanishes, and batch stream elements follow the same rule (`core/tasks/batchtfile/task.go:97-115`).
- Telegram storage: `SkipLarge` is silent nil-success (`storage/telegram/telegram.go:192-196`, batch ≈:615-619); album planning by `SourceGroupKey`/`MetadataGroupKey` (telegram.go:549-660); forum topics absent — only metadata *detection* exists (`pkg/metadata/metadata.go:138`), no `message_thread_id` on any send.
- Fork resend never re-downloads: it re-sends by current input location (id+access_hash+file_reference); ambiguous failure (e.g. timeout after server acceptance) is terminal because a fallback upload would mint a guaranteed duplicate (fork `core/tasks/tfile/execute.go:214-221` semantics).
- Fork hardening commits (c1ed571, f5c451d, e78c0b0, d3664f8, d9a304d, 45375da) are behavioral fixes inside send paths v1 rewrote — each must be re-verified against v1 code before porting (researcher-02 §11).

## Requirements

**Functional**
- Migration 2 adds: tables `saved_file_dedups`, `telegram_saved_files` (both keyed by `storage_id`, not name); `watch_chats` columns `media_type`, `storage_id` (0 = inherit), `dir_path`, `last_message_id *int64` (nullable); `users.media_filter`. Idempotent; safe on existing v1 DBs and fresh DBs. Dedup uniqueness is enforced by PARTIAL UNIQUE indexes created via raw SQL (gorm `OnConflict DoNothing` only fires on unique-index violations, and plain indexes never deduplicate): unique on `(user_id, storage_id, file_unique_id) WHERE file_unique_id != ''` plus unique on `(user_id, storage_id, content_hash) WHERE content_hash != ''` — the WHERE clauses keep empty-string `FileUniqueID` (non-TG) and empty-string `ContentHash` (resends) rows from colliding — and a plain index on `chat_id`. This exact shape is the ONE canonical index shape, cited identically in Phases 3 and 8. `telegram_saved_files` keeps its plain unique `(storage_id, storage_path)` — that one is correct as-is.
- TGFile interface exposes `FileUniqueID()` (`"doc:<id>"`/`"photo:<id>"`), set only in the constructor(s) so no path can drop it.
- `SanitizeRelDirPath` (reject absolute/UNC/drive/`~`, drop `..` segments, cap 512) and `SanitizeFileName` (single safe component) available and wired at the fork's actual defense layer: `SanitizeFileName` is called INSIDE `pkg/tfile` at every name-mutation point — the `NewTGFile` constructor, `FromMediaMessage`, `SetName`, `WithName`, `WithNameIfEmpty` (fork pkg/tfile/tgfile.go:46-48,107; opts.go:15,22) — so every ingress (bot handlers, web API `api/tgfiles.go`, relay `client/relay/download.go`, resume rebuild `core/resume/resume.go:338` which calls `SetName(item.Name)` on persisted names, watch filename-strategy) is covered at construction time; flush-time `SanitizeRelDirPath` remains the check for dir values.
- `ErrSkippedLarge` sentinel: TG storage returns it instead of silent nil; tfile/batchtfile map it to a Skipped item result visible to the user; no ledger/dedup records for skipped uploads (fork semantics, researcher-02 §2d). The sentinel is returned ONLY from the SkipLarge check inside `saveWithContext` (`storage/telegram/telegram.go:187-193`, shared by single and batch-derived single saves); `inspectBatchItem`'s splitSize branch (telegram.go:612-619) conflates "SkipLarge on + oversize" with "bigger than splitSize" and must remain a pure routing decision — the latter case still saves via single-path splitting, never a skip.
- Telegram storage config gains `topic_id` (default 0); every send path (single, album, split parts, resend) targets the topic when set.
- `SendExistingMedia` on Telegram storage: resend-by-id with fallback-eligible vs ambiguous failure taxonomy preserved.
- Saved-files ledger `telegram_saved_files`: Record on every successful upload path, `Exists` consults it (fail-open), enabling conflict checks and recovery-skip.
- Verified audit of the six hardening fixes; each ported or explicitly documented as already-covered-by-v1 with file:line evidence.

**Non-functional**
- Fail-open on all ledger errors (dedup/exists checks log and proceed — a broken ledger must never block saves).
- No new storage type, no enum/factory registration changes (forum topics and SkipLarge are same-type config/behavior).

## Architecture/Approach

- **One migration for everything**: fork tables land in migration 2 even though phases 3–7 fill them in, so no later phase touches `database/migration.go` again and Phase 8 imports into final shapes. Rejected: per-phase migrations — more entries, more conflict surface, no benefit for a single-user fork.
- **StorageID canonical, name only for import mapping**: fork v0 keyed by `storage_name`; v1 keys by `storages.id`. We do NOT add a legacy name column to fork tables; Phase 8 resolves names via the unique `storages.name`. Rejected: keeping name columns — duplicated state that orphans on rename (upstream already warns names leak in rule/watch paths, researcher-01 §12).
- **Hardening = verify-first audit**: for each fork fix, read the v1 code at the cited region; port only missing behavior. Rejected: blind cherry-pick — v1 rewrote these paths and may already handle the case.
- **Resend on mygotg**: re-express `ExistingMediaSender` with mygotg's `*ext.Context` as the sending client (the client that fetched the source owns the access hashes). Rejected: a "re-upload" fallback-first design — duplicates are the exact bug the taxonomy prevents.

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Modify | `database/migration.go` | Entry `{2, forkTables}`, bump `CurrentSchemaVersion` to 2; partial unique indexes via raw SQL (canonical shape, Requirements) |
| Modify | `database/model.go` | `SavedFileDedup`, `TelegramSavedFile` models; `WatchChat`/`User` new columns |
| Modify | `database/storage_record.go` | Extend `StorageReferences` (:76) to also count `watch_chats.storage_id`, `saved_file_dedups`, and `telegram_saved_files` references so console deletes cannot silently orphan fork rows |
| Create | `pkg/storagetypes/resend.go` | `ErrSkippedLarge`, `ErrResendPreflight`, `StorageLargeFileSkipper`, `ExistingMediaSender` (port fork pkg/storagetypes/resend.go) |
| Create | `common/utils/tgutil/inputmedia.go` | `ResendInputMedia`, `IsResendFallbackError` (port; adapt to mygotg) |
| Create | `common/tdler/refresh.go` | file_reference refresh helpers — ONLY if v1 lacks equivalents (verify step 9) |
| Create | `client/bot/handlers/utils/dirutil/sanitize.go` | `SanitizeRelDirPath` + test (port, framework-free) |
| Create | `pkg/tfile/sanitize.go` | `SanitizeFileName` + test (port, framework-free) |
| Create | `database/telegram_saved.go` | Ledger Record/Exists/Purge (port; re-key by StorageID) |
| Modify | `pkg/tfile/tgfile.go` | `FileUniqueID()` on interface + TGFileMessage impl; `SanitizeFileName` inside `NewTGFile`/`FromMediaMessage`/`SetName` |
| Modify | `pkg/tfile/opts.go` | `SanitizeFileName` inside `WithName`/`WithNameIfEmpty` (construction-time coverage of every ingress) |
| Modify | `storage/telegram/telegram.go` | SkipLarge sentinel, ledger recording, topics, hardening ports |
| Modify | `config/storage/telegram.go` | `TopicID int` json `topic_id`; `Validate` rejects `< 0` |
| Modify | `core/tasks/tfile/execute.go` | Sentinel→Skipped result; resend caller; skip-report — ALL pre-upload hooks sit ABOVE the stream dispatch (:29-31) |
| Modify | `core/tasks/batchtfile/execute.go` | Same for batch/single-element paths |

## Implementation Steps

1. **Migration 2** (database/migration.go): `forkTables(tx)` = `tx.AutoMigrate(&SavedFileDedup{}, &TelegramSavedFile{})` + `tx.AutoMigrate(&WatchChat{}, &User{})` (AutoMigrate adds the new columns; idempotent) + raw-SQL index creation inside the same migration transaction. Models: `SavedFileDedup{UserID uint, StorageID uint, FileUniqueID, ContentHash string, Size int64, StoragePath string, ChatID, MessageID int64, CreatedAt time.Time}` — no gorm.Model (hard rows, fork semantics); no struct-tag indexes for the dedup table (partial uniqueness is not expressible as a gorm tag) — create instead: `CREATE UNIQUE INDEX IF NOT EXISTS idx_dedup_fileid ON saved_file_dedups(user_id, storage_id, file_unique_id) WHERE file_unique_id != ''`, `CREATE UNIQUE INDEX IF NOT EXISTS idx_dedup_hash ON saved_file_dedups(user_id, storage_id, content_hash) WHERE content_hash != ''`, plus a plain index on `chat_id`. This canonical partial-unique shape is what makes `OnConflict DoNothing` actually deduplicate for dedup inserts, importer re-runs, and resend double-record protection (empty-string values must not collide). `TelegramSavedFile{StorageID, StoragePath, CreatedAt}` unique `(storage_id,storage_path)`. `WatchChat` += `MediaType string`, `StorageID uint`, `DirPath string`, `LastMessageID *int64`; `User` += `MediaFilter string`.
2. **FileUniqueID**: add method to the `TGFile` interface and `TGFileMessage` (pkg/tfile/tgfile.go fork equivalent — locate v1's message-file constructor and set it there only; `"doc:<id>"`/`"photo:<id>"`).
3. **Sanitizers**: port the two files + tests verbatim (framework-free). Wire `SanitizeFileName` INSIDE `pkg/tfile` at every name-mutation point — `NewTGFile`, `FromMediaMessage`, `SetName`, `WithName`, `WithNameIfEmpty` (fork pkg/tfile/tgfile.go:46-48,107; opts.go:15,22) — NOT at handler call sites: construction-time sanitizing covers every ingress at once (bot handlers, web API `api/tgfiles.go`, relay `client/relay/download.go`, resume rebuild `core/resume/resume.go:338` which calls `SetName(item.Name)` on persisted names, and the watch filename-strategy); upstream v1 has raw unsanitized assignment at all these points, and wiring at the handler layer would miss the API/relay/resume ingresses. Keep flush-time `SanitizeRelDirPath` for dir values (Phase 7 consumes it). Acceptance criteria: traversal tests via resume-rebuild (a `SetName` call with a persisted hostile name — `..`, absolute, UNC, drive-letter — yields a single safe component) and via API-created task names.
4. **SkipLarge sentinel**: define in `pkg/storagetypes/resend.go`; return it ONLY from the SkipLarge check inside `saveWithContext` (telegram.go:187-193, shared by single and batch-derived single saves); leave `inspectBatchItem`'s splitSize branch (telegram.go:612-619) a routing decision — a file with `splitSize < size <= maxUploadSize` still saves via single-path splitting. Map the sentinel to Skipped in tfile/batchtfile item results (see `core/result.go` ItemStatus / `database/task_record.go` item persistence). Preserve "nothing uploaded → no ledger records". Batch acceptance test: file with `splitSize < size <= maxUploadSize`, SkipLarge on → Skipped; SkipLarge off → saved-with-split.
5. **Forum topics**: `TopicID` in `config/storage/telegram.go` (+ json tag, Validate `< 0` reject); find v1's request-builder choke point (fork used a single `sendBuilder`, telegram.go:772-777; v1's sends funnel through `prepareMedia` ≈:441-547 and `saveMediaGroup` ≈:689+ — verify the actual choke point and thread `Reply(TopicID)` semantics: a topic is addressed by replying to its root message).
6. **Saved-files ledger**: port `database/telegram_saved.go` re-keyed by StorageID; record on every successful upload path in `storage/telegram/telegram.go` (plain, split zip, lossless video parts, albums); implement `Exists` via ledger (fail-open). Also extend `StorageReferences` (`database/storage_record.go:76`) to count `watch_chats.storage_id`, `saved_file_dedups`, and `telegram_saved_files` references so console storage deletes cannot silently orphan watches, dedup rows, or ledger rows (pairs with Phase 7's flush-time fallback for already-orphaned watches).
7. **Hardening audit** (each: read v1 code → port if missing → note evidence): (a) album doc+media homogenization — check v1 album planning at telegram.go:549-660 for doc+media mix handling; (b) zero-attribute video guard — check whether v1's media typing can emit zero-valued video attributes; port the degrade-to-document guard; (c) per-item fallback on deterministic MEDIA_* rejection (MEDIA_INVALID/MEDIA_EMPTY/PHOTO_EXT_INVALID/PHOTO_INVALID_DIMENSIONS/VIDEO_INVALID) — check v1's album send retry path; port fallback reusing prepared handles; (d) ledger+Exists (step 6); (e) resume-dir placement — NOT-APPLICABLE by locked decision (upstream native resume wins; porting the fork's `<cache>/.resume` layout would reintroduce journal-adjacent machinery Goal 3 forbids): v1's resume sidecars are `cachePath+".part"` + `.bitmap` colocated in the cache dir (`common/tdler/resume.go:45`), and boot cache-clean does NOT spare anything (`cmd/run.go:46-49` wipes via RemoveAllInDir whenever no unfinished tasks). Record this verdict in the audit table as "not-applicable by locked decision (upstream native resume wins)" and document the two known regressions in Phase 10's MERGE-PROPOSAL: (a) completed-download resume state is wiped at boot when no tasks are queued (the fork spared it 7 days), (b) v1 keys cache by taskID, so a failed task's partial download is not reusable by a retry (the fork reused it across tasks).
8. **Resend-by-id**: port `common/utils/tgutil/inputmedia.go` (input location builder, noforwards gate, deterministic-rejection set) and implement `Telegram.SendExistingMedia` on mygotg (`*ext.Context` sender, caption styling, rate limit, ledger record on success). Wire into tfile execute (single) and batchtfile single-element path; the resend caller sits ABOVE the stream dispatch (execute.go:29-31) — hook order with `config.Stream = true` is dedup file-id check (Phase 3) → tryResend → SkipLarge pre-check → THEN the stream branch, and batch stream elements follow the same rule (locked decision; full stream-mode dedup semantics in Phase 3). Ambiguous failure → terminal task error, no fallback, no dedup record.
9. **file_reference refresh**: grep v1 (`common/tdler/`, `client/`) for existing refresh handling (`FILE_REFERENCE_EXPIRED`); port `refresh.go` helpers only if absent; the `SendCtxForFile` principle (sender = fetcher client) guides the resend wiring.
10. Run verification; write the audit table (fix → v1 evidence file:line → ported/not) into the commit message.

## Todo

- [ ] (M1) Migration 2 + models + partial unique indexes via raw SQL (canonical shape) + `CurrentSchemaVersion = 2`; upgrade test on a copy of a v1-shaped DB; re-insert dedup test proves the partial uniques deduplicate
- [ ] (M1) `FileUniqueID()` in interface + constructor-only assignment
- [ ] (M1) Sanitizers ported; `SanitizeFileName` wired inside pkg/tfile constructors/`SetName`/opts; traversal tests via resume-rebuild + API-created names green
- [ ] (M1) telegram_saved_files ledger tables + helpers ported
- [ ] (M2) SkipLarge sentinel returned from `saveWithContext` only + Skipped item results (tfile + batchtfile) + splitSize-vs-SkipLarge batch test
- [ ] (M2) `topic_id` config + all send paths
- [ ] (M2) Ledger `Exists` + recording on all upload paths
- [ ] (M2) `StorageReferences` extended to count watch_chats/saved_file_dedups/telegram_saved_files references
- [ ] (M2) Hardening audit done with file:line evidence; missing fixes ported; 7(e) recorded as not-applicable by locked decision
- [ ] (M2) Resend-by-id on mygotg with fallback/ambiguous taxonomy; caller above stream dispatch
- [ ] (M2) Refresh helpers ported only if v1 lacks them

## Verification

```bash
go build ./...
go vet ./...
go test ./database/... ./storage/... ./pkg/tfile/... ./core/tasks/...
go test ./client/bot/handlers/... -run Sanitize
# migration check on a scratch copy of a v1 DB (no fork tables):
go run . --data-dir <tmp> --db-path <copy-of-v1.db> --no-web & sleep 3; kill %1
# then inspect: SELECT version FROM schema_migrations;  -> rows 1 and 2
# and confirm the canonical partial unique indexes exist:
# sqlite3 <tmp>/saveany.db ".indexes saved_file_dedups"  -> idx_dedup_fileid, idx_dedup_hash (+ plain chat_id index)
```

## Risk Assessment

1. **mygotg resend API mismatch** (medium likelihood, high impact): fork resend is deeply tied to gotgproto contexts. Mitigation: verify mygotg send primitives in step 8 before writing callers; keep the interface narrow (`SendExistingMedia(ctx, sendCtx, media, path, caption)`) so only one file knows mygotg.
2. **Hardening ports collide with v1 album rewrite** (medium, medium): v1 album planning differs from fork's. Mitigation: audit-first (step 7) with evidence recorded; port minimal behavior, not fork code shape.
3. **Migration on pre-existing v1 DBs** (low, high): AutoMigrate column adds are additive and idempotent, but test against a real v1-shaped DB (verification above). Two lock-in facts to carry into docs: (a) `migrate()` fatals when the DB version is greater than `CurrentSchemaVersion` (`database/migration.go:62-64`), so once migration 2 is applied, upstream v1 binaries are permanently blocked on the fork DB — the rollback-to-upstream path is the documented downgrade snippet (delete the version-2 `schema_migrations` row and drop the fork tables) in the Phase 10 guide, and selfupdate must never select upstream releases (the Phase 1 `config.GitRepo` fork constant is the guard); (b) `database.Init` opens and migrates the ACTIVE DB before any importer hook can run, so a stray `--data-dir` pointing at the v0 dir would mutate the v0 production file with v1 tables/columns — Phase 8 adds a boot guard BEFORE `database.Init` for exactly this case; never rely on the importer's own refusal alone.

## Security Considerations

- Sanitizers are the path-traversal defense for watch `dir:` args and crafted filenames — port before any Phase 7 code consumes user paths; tests must include `..`, absolute, UNC, drive-letter, `~`, and empty-segment cases (fork tests cover these; port them). Construction-time coverage inside `pkg/tfile` is the invariant: no ingress (handler, web API, relay, resume rebuild) may construct a TGFile name without passing through `SanitizeFileName`.
- Resend uses account-scoped access hashes; never log file_reference values. `inputmedia.go` must not leak secrets in error strings.
- Ledger `Exists` fail-open means a DB outage degrades to duplicate saves, not blocked saves — intended availability trade-off; document it.

## Next Steps

Phases 3 (dedup hooks on these tables), 4 (guards around these task paths), and 6 (media filter) unblock in parallel; run 3 before 4 (shared files).
