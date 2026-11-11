---
title: "Phase 3: 3-Layer Dedup + /history + /purge"
status: todo
phase: 3
priority: P1
effort: "10h"
dependencies: [phase-02-storage-task-foundations (M1)]
---

# Phase 3: 3-Layer Dedup + /history + /purge

## Overview

Build the 3-layer dedup ledger on the Phase 2 tables — (1) Telegram file-unique-id, (2) per-(user,storage) ledger rows, (3) sha256 of the downloaded cache file — hooked into v1's file-completion call sites with fork fail-open semantics, then surface the ledger through `/history` and `/purge` commands.

## Key Insights

- Fork layering and semantics (researcher-02 §2): pre-download file-id skip, post-download pre-upload hash skip, record after upload; duplicate = skip save and report success; every check fail-open (DB error = "not seen"); overwrite/disable-dedup still hashes and records but never skips.
- v1 single-file hook points — with one critical ordering fact: `execute.go:29-31` dispatches `if t.stream { return executeStream(...) }` BEFORE the download/stat/upload regions, so with `config.Stream = true` anything wired only into those regions silently vanishes. All layer-1 pre-checks sit ABOVE the stream dispatch, mirroring fork ordering: dedup file-id check → tryResend (Phase 2) → SkipLarge pre-check (Phase 2) → THEN the stream branch; the plain-path hooks remain before `tdler.DownloadToCache` (`core/tasks/tfile/execute.go:38`), after the stat (≈:49) / before the upload retry loop (≈:53), and after `core.MarkTaskElementDone` (≈:97). Batch stream elements follow the same rule (`core/tasks/batchtfile/task.go:97-115`).
- v1 batch hook points: creation-time pre-check where the batch task is built from the message selection (fork did it at `shortcut/tftask.go:398-400`; v1 equivalent = the batch task construction path feeding `core/tasks/batchtfile`), per-element skip around `saveBatchItems` (`core/tasks/batchtfile/execute.go:237-280`), record after each element completes.
- Hash-only layers for non-TG tasks: all 8 task types exist in v1 (`core/tasks/{aria2dl,batchtfile,directlinks,parsed,telegraph,tfile,transfer,ytdlp}/execute.go`, verified by ls-tree) — hook each after its local file is complete and before/after upload as fork did.
- **User identity gap**: v1 `tfile.Task` has no user field (`core/tasks/tfile/tftask.go:21-35`, verified) and `TaskParams` persists `StorageID` but no user (`core/tasks/tfile/params.go:34-46`). Dedup scope is (user, storage), so the user must be added to the task and its persisted params — this also survives v1's restart recovery (`core/resume/resume.go` rebuilds from `Params`). The rebuild/construction paths are part of the plumbing surface: `core/resume/resume.go:356` (single) and `:364` (batch) reconstruct tasks from persisted params, `core/retry/retry.go:112,118` rebuilds for retry, and `api/factory.go:283,298` constructs API-created tasks — all must deserialize/carry `UserID` into the constructors; `e2e/telegram/resume_test.go:135,162,266,280` covers the resume cycle those constructors feed.
- Item outcomes persist per file (`core/core.go:148-176` status resolution; `database/task_record.go:82-160` item records) — dedup skips report through the existing Skipped status rather than a parallel channel.

## Requirements

**Functional**
- `database/saved_file_dedup.go`: `RecordDedup(WithSource)` (OnConflict DoNothing against the Phase 2 PARTIAL UNIQUE indexes — unique `(user_id, storage_id, file_unique_id) WHERE file_unique_id != ''` and unique `(user_id, storage_id, content_hash) WHERE content_hash != ''`, plus plain `chat_id` index; this canonical shape is cited identically in Phases 2 and 8 — against plain indexes OnConflict DoNothing never deduplicates), idempotent; `DedupHasFileID`, `DedupHasHash`, `DedupCheckHash`, `DedupSkipByHash` (fail-open), `PurgeDedupByStorage` (by StorageID + user), `dedupEnabled` gate (userID <= 0 → disabled, once-guarded log) — re-keyed from fork `storage_name` to `StorageID` + `users.id`.
- Dedup recording is gated on `storageID > 0`: a task whose `storage.ID() == 0` (unresolved storage) must never record ledger rows — they would be scoped to a nonexistent storage and poison future dedup checks.
- `fsutil.Sha256File` (streamed, 1MB buffer) ported to `common/utils/fsutil/`.
- tfile: file-id check pre-download AND pre-stream-dispatch; hash check post-download pre-upload (plain path only); record after successful upload in BOTH the plain and stream paths; resend success also records (Phase 2 wiring). Locked stream decision (encode in code comments and docs): with `config.Stream = true`, the layer-1 file-id dedup check runs BEFORE stream dispatch, record-after-upload happens in both paths, and the hash layer is N/A-by-design for stream — a documented limitation (streamed uploads have no completed local cache file to hash).
- Locked decision — API-created tasks: tasks created through the web API (`api/factory.go:283,298`) run with `userID = 0`, which keeps the existing `userID <= 0 → dedup disabled` gate semantics — API-created tasks run dedup OFF. This is intended behavior, documented as such (not a bug).
- batchtfile: creation-time pre-check producing a "deduped" list surfaced in the creation message; per-element file-id and hash skips; record after upload; overwrite path hashes+records but never skips.
- Non-TG tasks: hash-only check + record (6 execute paths).
- `/history` grammar: `/history [page]`, `/history search <text> [p<page>]`, `/history stats`; literal-substring search with escaped LIKE wildcards; newest-first; page size 20; distinct replies for empty / no-matches / page-out-of-range; stats = totals + per-storage + last 14 days.
- `/purge <storage>`: OWNER-gated (the persisted owner identity from Phase 5 — `meta.owner_chat_id` capture-once accessor — NOT merely whitelist-gated), wipes dedup ledger (user-scoped) AND telegram ledger (by storage) with honest partial-failure reporting. Blast radius is documented: v1 storages are shared by default (empty user blacklist = all users), so a purge wipes the telegram ledger storage-wide for everyone — hence the owner gate.
- Batch progress messages include dedup counters (deduped/skipped counts in the completion summary).

**Non-functional**
- Fail-open everywhere; ledger writes are at-least-once with logged failures, never task-fatal.
- All new user-facing strings via i18n keys present in BOTH `common/i18n/locale/en.yaml` and `zh-Hans.yaml`, followed by `go generate .` (generator fails on parity mismatch — `cmd/geni18n/main.go:17-60`).

## Architecture/Approach

- **User in task params, not context**: add `UserID` to `tfile.Task` and `batchtfile.Task` (set at construction from the creating user) and serialize it into `taskspec` params so resume-rebuilt tasks keep dedup scope. Rejected: context-key lookup at execute time — lost on resume rebuild, and watch/api-created tasks cross goroutine boundaries where only params are durable.
- **Hooks at call sites, not a storage decorator**: researcher-01 proposed a `Storage` decorator wrapping `NewStorage` as the systemic option; rejected because the dedup decision needs the user and the item-result reporting both live in task code, and non-TG tasks never touch TG storage — a decorator would only cover half the layers.
- **Creation-time pre-check in batch construction**: mirrors fork UX (files dropped into a "deduped" list before the task exists). Alternative (check-only-at-execute) rejected: fork surfaced dedup counts in the creation message; that is part of the ported behavior.

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Create | `database/saved_file_dedup.go` | Port of fork file, re-keyed (StorageID, users.id) |
| Create | `database/saved_file_dedup_test.go` | Port tests (file-id, hash, no-user no-op, check-hash, purge) |
| Create | `database/saved_file_history.go` | `ListSavedFiles`, `GetSavedFileStats`, `escapeLike` (port) |
| Create | `client/bot/handlers/history.go` | `/history` command (port, mygotg contexts) |
| Create | `client/bot/handlers/purge.go` | `/purge` command (port; purge by storage name → resolve to ID) |
| Modify | `common/utils/fsutil/fs.go` | Add `Sha256File` (+ test file) |
| Modify | `core/tasks/tfile/{tftask,params,execute}.go` | UserID field, params, hooks (all pre-checks above the stream dispatch, execute.go:29-31) |
| Modify | `core/tasks/tfile/stream.go` | Record-after-upload inside the stream path (locked stream decision) |
| Modify | `core/tasks/batchtfile/` (task build + execute) | UserID, creation-time pre-check, element hooks (same above-stream rule for batch stream elements, task.go:97-115) |
| Modify | `core/resume/resume.go` | Deserialize the `UserID` param and pass it into the single (:356) and batch (:364) constructors so rebuilds keep dedup scope |
| Modify | `core/retry/retry.go` | Carry `UserID` through retry rebuilds (:112, :118) |
| Modify | `api/factory.go` | Constructors (:283, :298) run with userID=0 (locked: API tasks dedup OFF); document in code |
| Modify | `e2e/telegram/resume_test.go` | Extend the resume-cycle tests (:135,162,266,280 region): dedup scope survives rebuild |
| Modify | `core/tasks/{aria2dl,directlinks,parsed,telegraph,transfer,ytdlp}/execute.go` | Hash-only hooks |
| Modify | `client/bot/handlers/register.go` | Append `history`, `purge` to `CommandHandlers` (register.go:17-36) |
| Modify | `common/i18n/locale/{en,zh-Hans}.yaml` | ~29 keys (20 history, 5 purge, 4 dedup counters) |

## Implementation Steps

1. Port `Sha256File` + test; port `database/saved_file_dedup.go` re-keyed to `StorageID`/`users.id` with the Phase 2 model; port its test file.
2. Add `UserID` to tfile/batchtfile tasks: constructor param, field, and `taskspec` serialization (extend `pkg/taskspec` TGFile/TGFileItem/batch spec structs; bump params version if the spec carries one). The rebuild paths MUST deserialize it: `core/resume/resume.go:356,364` and `core/retry/retry.go:112,118` pass the persisted `UserID` back into the constructors; `api/factory.go:283,298` constructs with userID=0 (locked decision — API tasks run dedup off; document in a code comment). Add a resume-cycle test asserting dedup scope survives rebuild (extend `e2e/telegram/resume_test.go`).
3. tfile hooks (execute.go): ALL layer-1 pre-checks sit ABOVE the stream dispatch at execute.go:29-31 (hook order: dedup file-id check → tryResend (Phase 2) → SkipLarge pre-check (Phase 2) → THEN `if t.stream { return executeStream(...) }`; with `config.Stream = true` the file-id check still runs before streaming — locked decision): (a) pre-download and pre-dispatch — `DedupHasFileID(ctx, userID, storageID, file.FileUniqueID())` → report Skipped via `Progress.OnDone` + item result, return nil; (b) post-stat pre-upload — `DedupSkipByHash` (plain path only; the hash layer is N/A-by-design for stream, documented); (c) post-`MarkTaskElementDone` — `RecordDedupAfterUpload` with size/path/source in BOTH the plain and stream paths (stream recording hooks `core/tasks/tfile/stream.go`); gate recording on `storageID > 0`; skip all checks when overwrite/disable-dedup except (c)-record (fork: still hash+record, never skip).
4. batchtfile hooks: creation-time pre-check in the batch build path (drop deduped files into a list, render counts in the creation message using the 4 dedup-counter i18n keys); per-element skip at execute (batch stream elements follow the same above-the-stream-dispatch rule, `batchtfile/task.go:97-115`); record after each element upload.
5. Non-TG hash-only hooks in the 6 execute paths (fork line references: aria2dl:204,217; parsed:154,167; directlinks:186,199; telegraph:96,109; ytdlp:209,222; transfer:121,134 — locate the analogous complete/upload points in v1 code).
6. Port `database/saved_file_history.go` (search/stats queries against the new table).
7. Port `/history` and `/purge` handlers onto mygotg (`*ext.Context` reply API); `/purge <name>` resolves name→storage ID via `storage.GetStorageByUserIDAndName` and errors with the not-found key when absent. Gate `/purge` on the persisted owner identity from Phase 5 (`meta.owner_chat_id` capture-once accessor) — the wipe destroys shared telegram ledger state and must not be available to every whitelisted user; if Phase 3 lands before Phase 5, bring the small `settings/owner.go` accessor forward and let Phase 5 reuse it.
8. Register both commands in `CommandHandlers`; add i18n keys to both YAMLs; run `go generate .`.
9. Port batch progress dedup-counter rendering into v1's batch progress message composition (fork `core/tasks/batchtfile/progress.go:136-260`).

## Todo

- [ ] Dedup DB layer + tests (fail-open, idempotent record against the partial unique indexes, purge)
- [ ] UserID plumbed into tfile/batchtfile tasks + persisted params; rebuild paths (resume :356/:364, retry :112/:118) deserialize and pass it; resume-cycle test asserts dedup scope survives rebuild
- [ ] API-created tasks documented: userID=0 → dedup off (locked decision, intended behavior)
- [ ] tfile hooks above the stream dispatch + overwrite semantics; record-after-upload in BOTH plain and stream paths; hash layer N/A-by-design for stream documented
- [ ] Dedup recording gated on storageID > 0
- [ ] /purge owner-gated (Phase 5 owner identity); shared-storage blast radius documented
- [ ] batchtfile creation-time + element hooks + counters (batch stream elements follow the above-stream rule)
- [ ] 6 hash-only task hooks
- [ ] /history with pagination/search/stats + i18n
- [ ] /purge dual-ledger wipe + partial-failure note + i18n
- [ ] `go generate .` green; command registration

## Verification

```bash
go build ./... && go vet ./...
go test ./database/... -run "Dedup|History|Purge"
go test ./common/utils/fsutil/...
go test ./core/tasks/...
go test ./client/bot/handlers/... -run "History|Purge"
go generate . && git diff --exit-code -- common/i18n/i18nk/keys.go || git diff --stat -- common/i18n/i18nk/keys.go
```

Functional spot-check (optional, needs a running bot): save the same document twice to the same storage; second save reports skipped; `/history` lists one row; `/history stats` shows per-storage totals.

## Risk Assessment

1. **Creation-time pre-check UX divergence** (medium likelihood, low impact): v1's batch construction path differs from fork's `shortcut` layer. Mitigation: locate the exact build path first (step 4); if a pre-check cannot be cleanly inserted pre-task, fall back to execute-time-only checks and keep counters — do not fork v1's shortcut layer just for the message.
2. **Params-version skew on resume** (low, high): adding `UserID` to taskspec changes persisted params shape. Mitigation: additive JSON field with `omitempty`-safe decoding; test a resume cycle for a pre-phase-3 record (graceful: missing user → dedup disabled, once-guarded log — same as fork's userID<=0 gate).
3. **Double-counting with Phase 2 ledger**: resend path records dedup (Phase 2 step 8) AND the upload hook records — ensure idempotency via OnConflict DoNothing against the canonical partial unique indexes defined in Phase 2 before wiring both; against plain indexes both recordings would insert duplicate rows (which is exactly why the indexes are partial uniques).

## Security Considerations

- `/history` and `/purge` go through the global whitelist gate automatically (registered commands run after `checkPermission`, `client/bot/handlers/register.go:60-89`); `/history` additionally scopes every query to the requesting user's rows — verify no cross-user leak in `ListSavedFiles`.
- LIKE search must escape `%`/`_` (fork `escapeLike` + `ESCAPE '\'`) to keep search literal and avoid query-shape surprises.
- `/purge` blast radius: v1 storages are shared by default (empty user blacklist = every whitelisted user), so a purge wipes shared telegram ledger state storage-wide; the owner gate (persisted `meta.owner_chat_id`, Phase 5) is the control, and the Phase 10 docs state the blast radius explicitly.
- Hash ledger stores sha256 of content only — no sensitive data; keep `StoragePath` values sanitized (they come from task paths built through sanitizers).

## Next Steps

Phase 4 (download guards) follows on the same execute files — run it after this phase to avoid conflicts; Phase 7 consumes the batch counters and task plumbing built here.
