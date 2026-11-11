# FORK PORTING INVENTORY — SaveAny-Bot fork (v0-based) → upstream v1 rebuild

**Scope**: working tree at `D:\SaveAny-Bot`, branch `main`, fork commits `92e6072..HEAD` (48 commits). Baseline facts verified against the tree: the fork's v0 base already had `/watch` (3-column model), the HTTP API, and `Dockerfile.micro/.pico`; everything documented below as "fork-added" was confirmed by diffing against `git show 92e6072:<path>`. Upstream v1 (`upstream/main`) facts used in hazard notes: framework is `github.com/krau/mygotg` (go.mod), config moved to `settings/` + `database/setting.go` (SQLite settings, no viper), storages are DB rows with IDs (`database/storage_record.go`), tasks persist as `database/task_record.go`, and `Dockerfile.micro/.pico` no longer exist upstream.

## 1. WATCH EXTENSIONS (largest feature group)

The v0 base had `/watch <chat>` with only `UserID/ChatID/Filter` (base `database/model.go`). The fork added per-watch destination overrides, media-type filters, a restart catch-up cursor, batch notifications, and a uniqueness invariant.

### (a) Files and key ranges
- `client/bot/handlers/watch.go` (705 lines): command handlers 39–229; intake plumbing 231–606; album buffer manager 608–705.
- `client/bot/handlers/watch_args.go` (115): flag grammar 12–47, `parseWatchArgs` 59–88, regex cache 90–115.
- `client/bot/handlers/watch_catchup.go` (374): bounds 26–31, page planner 59–112, boot walk 114–269, raw history helpers 271–340, summary 342–374.
- `client/bot/handlers/watch_notify.go` (161): `/watch notify` toggle 22–58, single-file notification tracker 60–161.
- `client/bot/handlers/watch_notify_batch.go` (99): album notification tracker 22–99.
- `client/user/watch.go` (100): live event source — `MediaMessageEvent` 14–19, 5-second debounce map 26–67, `handleMediaMessage` dispatcher hook 69–100 (document/photo only).
- `database/chat.go` (153): CRUD 10–54, `CountWatchChats` 58–62, cursor helpers 64–84, partial unique index migration 86–152.
- `database/model.go:38-56`: `WatchChat` model. Fork-added columns: `MediaType`, `StorageName`, `DirPath`, `LastMessageID *int64`.
- `client/bot/handlers/register.go:41-43` (command registration), `register.go:74-79` (goroutines: live listener first, then `runWatchCatchUp`).

### (b) Public API other code calls
- `database`: `WatchChat(ctx, chat)`, `UnwatchChat`, `WatchingChat`, `GetWatchChatsByChatID`, `ListAllWatchChats`, `CountWatchChats`, `TouchWatchCursor(ctx, watchID, messageID)` (guarded: never lowers, `chat.go:68-72`), `CurrentWatchCursor` (`chat.go:75-84`), `DedupWatchChats`, `EnsureWatchChatUniqueIndex` (`chat.go:143-152`, called from `database/db.go:55` every boot).
- handlers-internal (package-private, only within `handlers`): `parseWatchArgs`, `watchFilterRegex`, `resolveWatchTarget`, `processMediaEventForWatch`, `runWatchCatchUp`, `openWatchNotification` (a test-seam var, `watch_notify.go:62`), `watchMediaGroupMgr.FlushChat`.

### (c) Command syntax and parsing
`/watch <chat> [storage:<name>] [dir:<path>] [msgre:<regex>] [mediatype:<types>]`, flags in any order (`watch_args.go:50-58`). Colon handling (`watch_args.go:64-69`): a `msgre:` value is the rest of its own token plus every following *non-flag* token joined with `" "` — so regexes may contain colons and span words, and a regex ends only at the next `storage:`/`dir:`/`msgre:`/`mediatype:` token (`isWatchFlagToken`, lines 42–47). `storage:`/`dir:` are single-token flags; the command line is split quote-aware by `strutil.ParseArgsRespectQuotes` (`watch.go:41`). A second non-flag token outside a msgre tail is an error; a token containing `:` that is not a known flag is `errWatchFilterTypeUnsupported` (`watch_args.go:81-83`), mapped to distinct localized replies at `watch.go:65-72`. Regex capped at 512 chars (`maxWatchRegexLen`, `watch_args.go:23`) and compiled once per watch via a process-lifetime `sync.Map` cache (`watch_args.go:95-115`). `mediatype:` is fail-closed: unknown token or empty resolved set is rejected at creation (`watch.go:104-118`).

### (d) Storage+dir resolution and intake
- Per-watch override precedence (`resolveWatchTarget`, `watch.go:257-274`): explicit `storage:`/`dir:` replaces the rule layer *entirely*; a partial override keeps the user default for the other axis; otherwise rule match → user defaults. Enforced again at flush (`resolveWatchFlushTarget`, `watch.go:443-492`) with a second `dirutil.SanitizeRelDirPath` pass as defense against out-of-band DB edits (459–467). Rule-layer `NEW-FOR-ALBUM` sentinel resolves to the base dir (478–483).
- Shared intake `processMediaEventForWatch` (`watch.go:354-412`): live events advance the cursor **before any filter** (362–366 — a busy channel must move the cursor even when nothing matches); mediatype filter first (375–390, falling back to the user's per-user default filter from `User.MediaFilter`), then album buffering for any `GroupedID != 0` (397–406) or per-file msgre for singles (408–411).
- Albums: ANY watched media group becomes ONE batch task at flush (`processWatchMediaGroup`, `watch.go:532-606`) — `ForceGrouping: true` batch via `shortcut.CreateAndAddBatchTGFileTask` (572–588) with a non-interactive pinned conflict strategy (`watchConflictStrategy`, 317–330: pin wins; user's ASK degrades to Rename). msgre evaluated once per group against the caption-carrying member (`groupMatchesFilter`, 299–315). A group reduced to one member is saved as a single file (561–565). Cursor advances to max member message ID only after the batch task is durably enqueued (593–599). The album buffer (`watchMediaGroupHandler`, 608–705) deduplicates buffered messages by message ID (640–646) — this plus the cursor overlap guard is how live×catch-up double-intake was closed (commit `b5be423`).
- Singles (`processWatchSingle`, `watch.go:497-524`): `xid` task id, `coretfile.NewTGFileTask`, `core.AddTask`; cursor touched only after `AddTask` succeeds (517–519) because the journal row is written inside AddTask — that is the durability boundary.

### (e) Catch-up cursor semantics (commits `0532a97`, `0e5c0d1`, `b5be423`)
- The cursor is `watch_chats.last_message_id`: "newest message ID already observed (live) or durably enqueued (backfill)" (`database/model.go:52-55`).
- First boot for a watch (cursor NULL/0): fetch the single newest message (`fetchNewestMessageID`, `watch_catchup.go:274-305` — `OffsetID: 0`, no `AddOffset` shift; the `0e5c0d1` fix), store it guarded, backfill **nothing** (owner decision, `watch_catchup.go:155-172`). Empty chat leaves it unset; first live event initializes.
- Existing watches: walk history ascending from the cursor with `planPagesWithSize` (`watch_catchup.go:72-112`) — real message IDs only as anchors (never ID arithmetic), `AddOffset: -100` page fetch (`fetchHistoryPage`, 310–340), stop on short batch / 500-message cap / stalled anchor / 10-minute budget. FLOOD_WAIT is left to the userbot middleware; no manual wrapper (comment at 21–25).
- Live×catch-up overlap guard: backfill skips any message `<= CurrentWatchCursor` (`watch_catchup.go:218-224`) because the live listener already advanced past it pre-filter; plus the buffer-level message-ID dedupe (`watch.go:640-646`).
- Post-flush cursor advance over non-matching messages (`watch_catchup.go:260-262`) happens **only when every fed album file actually enqueued** (`counters.albumFiles == groupFilesFed`) — a failed album flush stays re-fetchable.
- Backfill pins `ConflictStrategyRename` (`watch_catchup.go:241`) because overwrite disables dedup and would duplicate on live/backfill overlap.
- Summary message to the owner reports fetched/enqueued/albums/capped (`sendWatchCatchUpSummary`, 348–374) using a copied bot ext context (`newBotContext`, `watch_notify.go:103-105`) because gotgproto ext contexts hold a non-concurrency-safe message-ID random source.

### (f) Batch notification
- Single files: `watchNotifyProgress` (`watch_notify.go:84-154`) — notification opened lazily on first progress callback via `sync.Once`; nil tracker when `user.WatchNotify` is false; the task context keeps the userbot (it downloads/uploads) while edits ride a per-task copy of the bot context (`botCtxFor`, 118–120).
- Albums: `batchWatchNotifyProgress` (`watch_notify_batch.go:22-99`) wraps the standard `batchtfile.Progress`; until the notification message is sent, `inner.MessageID` stays 0 and all callbacks are no-ops; constructed via `ProgressFactory` with the creation-time skipped/deduped lists (`watch.go:585-588`).
- `/watch notify on|off` toggles `user.WatchNotify` (`watch_notify.go:22-39`); default off.

### (g) DB schema
Table `watch_chats` (gorm default naming): `id`, `created_at`, `updated_at`, `deleted_at` (gorm.Model), `user_id`, `chat_id`, `filter`, `media_type`, `storage_name`, `dir_path`, `last_message_id` (nullable). Index: **partial unique** `idx_watch_chats_user_chat_active ON watch_chats(user_id, chat_id) WHERE deleted_at IS NULL` (raw DDL, `database/chat.go:90`), created after `DedupWatchChats` sweeps legacy duplicates (highest cursor survives, ties keep newest id, hard delete; `chat.go:97-138`). Commit `6c87dc5`.

### (h) Tests
`watch_args_test.go` (7 tests incl. `TestParseWatchArgs`, `TestResolveWatchTarget`, `TestWatchConflictStrategyPin`), `watch_catchup_test.go` (6 incl. `TestPlanWatchCatchUpPages`, `TestTouchWatchCursorGuard`, `TestWatchIntakeLiveCursorAdvancesPreFilter`), `watch_notify_test.go` (5), `watch_notify_batch_test.go` (2), `database/watch_index_test.go` (`TestEnsureWatchChatUniqueIndex` — covers sweep, index creation, soft-delete non-blocking, idempotency).

### (i) Port hazards (v1)
- All handlers take `*gotgproto/ext.Context`; v1 uses mygotg contexts — every `ctx.Raw.MessagesGetHistory`, `ctx.Reply`, `ext.NewContext(...)` copy trick (`watch_notify.go:103-105`) must be re-expressed against mygotg's API.
- The cursor + partial unique index + dedup migration logic is framework-agnostic GORM and ports nearly as-is, but v1's migration path is versioned (`database/migration.go`); the raw partial-index DDL must be added to a migration entry there.
- The intake depends on fork-only task plumbing: `coretfile.NewTGFileTask`, `shortcut.CreateAndAddBatchTGFileTask(BatchOptions{ForceGrouping, ProgressFactory, Override})`, `tcbdata.ConflictStrategy*`, `mediautil` — all of which are themselves fork features that must land first (ordering constraint).

## 2. DEDUP, 3 LAYERS (commits `eb4207a`, `ccbdc79`, `db876da`, `2b59007`, `8d6da3b`)

**The three layers are: (1) Telegram file-unique-id, (2) per-(user,storage) ledger rows in `saved_file_dedups` keyed by that id, (3) sha256 content hash of the downloaded cache file — plus a path-based telegram ledger that exists for a different purpose (conflicts, section 5).**

### (a) Files
- `database/saved_file_dedup.go` (198): model 19–32; `dedupEnabled` 39–50 (userID<=0 → disabled, once-guarded INFO); `DedupHasFileID` 56–68; `DedupHasHash` 72–84; `RecordDedup(WithSource)` 88–118 (`OnConflict DoNothing`, idempotent); `DedupCheckHash` 124–133; `DedupSkipByHash` 146–161 (fail-open: hash failure logs and continues); `RecordDedupAfterUpload(WithSource)` 167–180 (failure logged as error, at-least-once); `PurgeDedupByStorage` 186–198.
- `pkg/journal/journal.go` (114): **not dedup** — this is the task journal (superseded by upstream v1's native resume/TaskRecords; do not port).
- `common/utils/fsutil/fs.go:16-31`: `Sha256File` — streamed, 1MB buffer.
- `pkg/tfile/tgfile.go:20-24, 87-89, 91`: `TGFile.FileUniqueID()` interface + impl (`"doc:<id>"` / `"photo:<id>"`), set only through the single `newTGFile` constructor so no path can drop it.

### (b) Hook points in task execution (all call sites verified by grep)
- **TG single file** `core/tasks/tfile/execute.go`: file-id check pre-download (43–51), hash check post-download pre-upload (115–124, `DedupSkipByHash`), record after upload (172–173), record after successful resend (209).
- **TG batch** `core/tasks/batchtfile/execute.go`: creation-time pre-check via `shortcut/tftask.go:398-400` (`dedupHas` → files dropped into a "deduped" list at partition time, surfaced in the creation message); element file-id skip `dedupSkipByFileID` (356–374); hash skip `dedupSkipByHash` (457–470 — with `DisableDedup`/overwrite it still hashes and records, never skips); record after upload (450–455).
- **Non-TG tasks (hash-only, no file id)**: `aria2dl/execute.go:204,217`; `parsed/execute.go:154,167`; `directlinks/execute.go:186,199`; `telegraph/execute.go:96,109`; `ytdlp/execute.go:209,222`; `transfer/execute.go:121,134`.

### (c) Duplicate found → what happens
Skip the save entirely and report success: single path calls `Progress.OnDone(ctx, t, nil)` and returns nil (`tfile/execute.go:44-50, 120-122`); batch path marks the item deduped and notifies (`batchtfile/execute.go:370-373`). All checks are **fail-open** (DB error = "not seen").

### (d) SkipLarge interplay
`storagetypes.ErrSkippedLarge` (`pkg/storagetypes/resend.go:14`) returned by `Telegram.save` (`storage/telegram/telegram.go:200-205`) is treated as success-with-skip: retry loop stops (152–157), and **neither the dedup ledger nor the telegram_saved ledger records anything** (`tfile/execute.go:166-171` — "Nothing was uploaded: no ledger or dedup record"). Pre-download skip via `StorageLargeFileSkipper.SkipLargeLimit` (58–70) likewise avoids spending the download.

### (e) Schema
Table `saved_file_dedups`: `id` PK, `user_id`, `storage_name`, `file_unique_id`, `content_hash`, `size`, `storage_path`, `chat_id`, `message_id`, `created_at` (no gorm.Model — no soft delete). Composite indexes from GORM tags (`saved_file_dedup.go:21-29`): `idx_dedup_scope` (`user_id`,`storage_name`), `idx_dedup_fileid` (`storage_name`,`file_unique_id`), `idx_dedup_hash` (`storage_name`,`content_hash`), plus a single-column index on `chat_id`.

### (f) Tests
`database/saved_file_dedup_test.go` (5: file-id, hash, no-user no-op, check-hash, purge), `common/utils/fsutil/sha256_test.go`, batch progress dedup counters rendered in `core/tasks/batchtfile/progress.go:136-260` with keys from commit `8d6da3b`.

## 3. FILE HISTORY + LEDGER OPS (commit `ac552ed`)

### (a) Files
- `database/saved_file_history.go` (104): `HistoryPageSize = 20` (line 10); `ListSavedFiles(ctx, userID, q, page)` 45–67 — newest first (`created_at DESC, id DESC`), literal-substring search with escaped LIKE wildcards (`escapeLike` 37–40, paired with `ESCAPE '\'`); `GetSavedFileStats` 71–104 — totals, per-storage group-by, per-day for the last 14 days (`historyDaysBack`, line 33).
- `client/bot/handlers/history.go` (175): grammar `/history [page] | /history search <text> [p<page>] | /history stats` (16–46); the explicit `p2` marker keeps trailing numbers in the query text (51–68); listing shows base name, storage, size, local time, and source `chat/msgid` when the dedup row carries `ChatID/MessageID` (98–112); page-out-of-range vs no-matches vs empty are distinct replies (76–91).
- `client/bot/handlers/purge.go` (52): `/purge <storage_name>` wipes BOTH ledgers for that storage — `PurgeDedupByStorage` (user-scoped) and `PurgeTelegramSavedByStorage` (global by storage name) — with an honest partial-failure note (39–44). Purpose (commit `00c6285`): after the user deletes messages by hand, the ledgers would keep blocking re-saves.

### (b) DB schema
Reads/writes `saved_file_dedups` only (schema in section 2); `telegram_saved_files` (section 5). No separate history table.

### (c) Tests
`database/saved_file_history_test.go` (3: list, search, stats), `client/bot/handlers/history_test.go` (2: search args, page marker). `purge.go` has no dedicated test file.

## 4. OPS COMMANDS (commits `ac552ed`, `70afe54`)

### `/status` — `client/bot/handlers/status.go`
- Gated to the FIRST whitelisted user only (`status.go:28-31`) — global stats must not leak to added accounts.
- Uptime from `core.StartTime()` (`core/core.go:26-33`, set in `cmd/run.go:38`); queued/running from `core.GetQueuedTasks`/`core.GetRunningTasks` (`core/core.go:177-183` → `pkg/queue/queue.go:142-159, 121-138`, which snapshot the list in insertion order, excluding cancelled).
- **Queue positions**: there is no per-task position API; `/task queued` computes positions as slice index+1 at render time (`client/bot/handlers/tasks.go:94-104`, max 10 rows shown, truncation note 114–116). Flood-paused tasks remain in the *running* map (not the queue), so positions are unaffected by flood pauses.
- Cache size via `fsutil.DirSize` (single-flight, TTL-cached — `common/utils/fsutil/dirsize.go`), resume size from `<cache>/.resume` (`tdler.ResumeDirName`, `common/tdler/resume.go:368-369`), DB file size from `config.C().DB.Path` (`status.go:51`), disk free for cache base + every LOCAL storage root via `fsutil.DiskFree`/`DiskID` (`fsutil/disk_free_unix.go`, `disk_free_windows.go`), deduped per filesystem identity (53–79). Every field degrades to "unknown".

### `/id` — `client/bot/handlers/id.go`
Resolves `@username` → title/type/numeric ID (18–48). Resolution path: prefer the **userbot** context (`user.GetCtx()`, lines 24–27) so public channels the bot isn't in resolve too; `ctx.ResolveUsername` then type classification for `types.Channel` (Megagroup→group), `types.User`, `types.Chat` (50–63).

### Tests
None for status.go/id.go (thin handlers over tested core/queue code; `pkg/queue/queue_test.go` exists upstream-v0-inherited).

## 5. RESEND + SKIPLARGE (commit `21fb859`)

### (a) Files
- `pkg/storagetypes/resend.go` (42, entirely fork-added): `ErrSkippedLarge` (14), `ErrResendPreflight` (19), `StorageLargeFileSkipper` interface (25–29), `ExistingMediaSender` interface (35–42 — `SendExistingMedia(ctx, sendCtx *ext.Context, media, storagePath, caption)`; `sendCtx` is the client that fetched the source, because access hashes/file references are account-scoped).
- `common/utils/tgutil/inputmedia.go` (83, fork-added): `ResendInputMedia` 18–41 — builds `InputMediaDocument`/`InputMediaPhoto` from the file's **current location** (id + access_hash + file_reference; *not* the source message's stale reference) with a `noforwards` gate; `ResendCanFallback` 48–50; `IsResendFallbackError` 58–82 (deterministic Telegram rejections only).
- `storage/telegram/telegram.go`: `SendExistingMedia` 127–164 (rate-limited, caption styling, records the path on success); `SkipLargeLimit` 184–189; premium-aware `maxUploadFileSize` 299–303 (`MaxUploadFileSize = 4000*512KB`, `PremiumMaxUploadFileSize = 8000*512KB`, lines 39–40).
- Callers: `core/tasks/tfile/execute.go:52-57,182-222` (single task) and `core/tasks/batchtfile/execute.go:385-426` (single-element path only; albums keep the upload so groups stay intact).

### (b) Flow — important correction
A "resend" does **not re-fetch and re-upload** anything: it re-**sends the source media by its input location** (`inputDocument`/`inputPhoto` id+access_hash+file_reference) straight into the Telegram storage chat via `messages.sendMedia` — zero download, zero upload. Outcome semantics: success → dedup record written (209, 409); fallback-eligible failure → classic download+upload; **ambiguous failure (e.g. timeout after server accepted) → task fails terminally**, because re-uploading on top of a possibly-delivered resend would create a guaranteed duplicate (214–221, 416–425).

### (c) `database/telegram_saved.go` (80)
Tracks **(storage_name, storage_path) pairs only — no file_id, no access_hash**. It exists because Telegram has no "does this file exist in the chat" primitive: `Telegram.Exists` consults the ledger (`telegram.go:108-110`), which is what makes conflict strategy and recovery-skip work against telegram storage (commit `d9a304d`). `RecordTelegramSaved` 25–44 (`OnConflict DoNothing`), `TelegramSavedExists` 49–61 (fail-open), `PurgeTelegramSavedByStorage` 68–80.

### (d) Schema
`telegram_saved_files`: `id` PK, `storage_name`, `storage_path`, `created_at`; composite unique index `idx_telegram_saved_files` (`storage_name`,`storage_path`) (model `telegram_saved.go:16-21`).

### (e) Tests
`storage/telegram/skip_large_test.go` (2: sentinel on oversize, capability disabled without SkipLarge — comment "RT-8" explains the nil-success poisoning bug this fixed), `database/telegram_saved_test.go` (2: index/purge).

## 6. FORUM TOPICS (commit `4e6636e`)

- Config: `config/storage/telegram.go:17` — `TopicID int` (`toml:"topic_id"`), documented as "the topic's root message id (the trailing number of a t.me/c/<chat>/<topic> link)"; **default 0** = no topic (General topic in forum groups, plain chat elsewhere). Validation: `TopicID < 0` rejected (`telegram.go:37-39`). Not present in the v0 base config and absent from `config.example.toml`.
- Usage: the single choke point is `sendBuilder` (`storage/telegram/telegram.go:772-777`) — when `TopicID != 0` it wraps the request builder with `rb.Reply(TopicID)` (a topic is addressed by replying to its root message, per core.telegram.org/api/forum). Every send path funnels through it: plain save (275–279), resend (158), album/group send (692), split uploads and lossless video parts.

Hazard: v1 telegram storage config keys live in `storage_records.config_json` (SQLite) with different naming; the reply-to-root-message mechanism itself is framework-independent.

## 7. STALL WATCHDOG + FLOODWAIT METER (commits `2ee5b39`, `8eaa47c`, `65c66cf`, `1b902ad`, `f6e3101`, `2f341b7`, `2c7a6a9`)

### Stall watchdog — `common/utils/dlutil/stall.go`
`DownloadStallTimeout = 15min` (15); `NewStallWatchdog` 33–65 (goroutine + resettable timer; `Kick` resets, non-blocking 69–75; `Stalled` flag); `DownloadFailure` 95–103 picks the most specific cause: stall → flood-crawl (`CrawlError`) → raw error.

### Floodwait meter — `common/utils/dlutil/floodwait_meter.go`
Detects "crawling" downloads where every RPC succeeds but each absorbed a multi-minute FLOOD_WAIT (bytes trickle, so the stall watchdog stays quiet). Window `MeterWindow = 15min`, `SlowRPCThreshold = 2min`, cancel when accumulated slow time ≥ `MeterWindow/2` per independent window (11–33, 81–99). `Observe(dur, err)` counts only *successful* slow RPCs (105–113); wired per-RPC in `common/tdler/dler.go:51`.

### Client middleware — `client/middleware/floodwait.go:12-19`
`floodwait.NewSimpleWaiter().WithMaxRetries(maxRetries)` + `ratelimit.New(100ms, 5)` — this is what surfaces long flood waits instead of silent retry (commit `65c66cf`); the catch-up code deliberately does NOT stack another wrapper on top (`watch_catchup.go:21-25`).

### Guarded download loops (integration in core/tasks)
- `core/tasks/tfile/execute.go:76-97` (plain download; `dlCtx` derived from watchdog ctx so RPCs observe cancellation; meter passed into `tdler.DownloadToCache` via `tdler.DownloadResumeKey(...)` args, `common/tdler/download_cache.go:46-51`)
- `core/tasks/tfile/stream.go:37-41` (stream path)
- `core/tasks/batchtfile/execute.go:496-523` (element download) and `685-710` (stream element) — `8eaa47c` added the plain per-element guard that was missed initially.

### Progress-edit flood backoff — `common/utils/tgutil/progress_edit.go`
Global 1 rps limiter (19, 27); on `FLOOD_WAIT` from an edit, `markProgressEditFlooded` pauses ALL progress edits process-wide via an atomic "flooded-until" timestamp, **clamped to [0, 1h]** (`progressEditMaxPause`, 25, 42–51). Non-final edits during a pause are dropped (132–137); the final edit waits out the pause bounded by cap+ctx (140–145). `MESSAGE_NOT_MODIFIED` is idempotent success (62–66). `ProgressEditQueue` coalesces to a 1-slot channel where a final edit can never be displaced (96–115). Single-task edits route through this shared queue (`f6e3101`).

Tests: `dlutil/stall_test.go` (4), `dlutil/floodwait_meter_test.go` (6), `storage/telegram/progress_test.go`.

## 8. MINIO STORAGE (inherited v0, deprecated)

- `storage/minio/client.go` (`//go:build !no_minio`): `Minio` struct 26–30; `Init` warns "deprecated, use S3" once (32–35), validates config, requires existing bucket (54–60). API surface is the **minimal** storage interface only: `Type`, `Name`, `JoinStoragePath`, `Save` (78–99, unique-path + `PutObject`), `Exists` (101–104). It does **not** implement `SaveWithProgress`, `SendExistingMedia`, `SkipLargeLimit`, or streaming — narrower than telegram/local/s3.
- `storage/minio/client_stub.go` (`//go:build no_minio`): compile-out stub returning errors (used by Dockerfile.micro/pico).
- `config/storage/minio.go:11-19`: fields `endpoint`, `access_key_id`, `secret_access_key`, `bucket_name`, `use_ssl`, `base_path`; all required by `Validate` (22–36).
- Note: this existed at the v0 base (verified `git ls-tree 92e6072`) — the fork did not touch it. Upstream v1 has already migrated MinIO configs to S3 (`database/storage_record.go` `MigrateMinioStorageType`). Port decision: drop the minio storage type; rely on upstream's minio→s3 record migration for existing data.

## 9. DOCKERFILE.MICRO / DOCKERFILE.PICO

Both existed at the v0 base; **the fork never modified them** and upstream v1 has removed them. They are carried artifacts:
- `Dockerfile.micro`: golang:alpine builder → alpine runtime with `curl` only (no ffmpeg/yt-dlp vs main Dockerfile); builds with `-tags=no_jsparser,no_minio` — trims JS parser + minio-go dependency for image size.
- `Dockerfile.pico`: golang:alpine builder → **`FROM scratch`** runtime with only ca-certificates copied and `SSL_CERT_FILE` env; builds with `-tags=no_jsparser,no_minio,sqlite_glebarez` — the pure-Go driver removes the ncruces/wasm SQLite machinery for the smallest static binary.
- `Makefile` has only `run`/`down` targets over `docker-compose.local.yml` — no targets reference micro/pico; they are built by direct `docker build -f`.

## 10. V0 DB CUSTOM TABLES (for the data importer)

All models in `database/model.go`; AutoMigrate list at `database/db.go:52` (fork extended it from `User, Dir, Rule, WatchChat` to include `PendingTask, TelegramSavedFile, SavedFileDedup`). Single SQLite connection (`db.go:49`) with WAL + busy_timeout pragmas injected into the DSN.

| Table | Columns | Indexes |
|---|---|---|
| `users` | gorm.Model + `chat_id`, `silent`, `default_storage`, `default_dir`, `apply_rule`, `watch_notify`, `filename_strategy`, `filename_template`, `conflict_strategy`, **`media_filter` (fork)** | uniqueIndex `chat_id` |
| `dirs` | gorm.Model + `user_id`, `storage_name`, `path` | — |
| `rules` | gorm.Model + `user_id`, `type`, `data`, `storage_name`, `dir_path` | — |
| `watch_chats` | gorm.Model + `user_id`, `chat_id`, `filter`, **`media_type`, `storage_name`, `dir_path`, `last_message_id` (fork; nullable)** | partial unique `idx_watch_chats_user_chat_active (user_id, chat_id) WHERE deleted_at IS NULL` (raw SQL) |
| `pending_tasks` (fork) | `id` PK = task xid (string), `user_id`, `payload` (JSON `journal.TGTaskPayload`), `attempts`, `created_at` | PK only; **DO NOT PORT** — superseded by upstream v1 TaskRecord/resume |
| `telegram_saved_files` (fork) | `id`, `storage_name`, `storage_path`, `created_at` | unique `idx_telegram_saved_files (storage_name, storage_path)` |
| `saved_file_dedups` (fork) | `id`, `user_id`, `storage_name`, `file_unique_id`, `content_hash`, `size`, `storage_path`, `chat_id`, `message_id`, `created_at` | `idx_dedup_scope`, `idx_dedup_fileid`, `idx_dedup_hash`, index `chat_id` |

**DB path for the importer**: viper default `"db.path": "data/saveany.db"` (`config/viper.go:132`), overridable via `[db] path` in TOML or `SAVEANY_DB_PATH` env; bot session `data/session.db`, userbot session `data/usersession.db`.

Hazard for the importer: v1 storages are rows with IDs (`database/storage_record.go`), so `storage_name` columns in all fork tables must be mapped name→v1 storage ID during import; `user_id` in dedup rows is the *chat ID* (int64), while `watch_chats.user_id` is the *DB user ID* (uint) — the two are different keys.

## 11. TG STORAGE HARDENING FIXES (symptom → fix → files)

- **`c1ed571` — albums mixing docs+media**: symptom: persistent `messages.sendMultiMedia` rejections (MEDIA_INVALID/MEDIA_EMPTY) even though identical singles succeed — a video whose metadata can't be read degrades to a plain document, and Telegram rejects any album mixing plain documents with photo/video outright. Fix: `saveMediaGroup` detects the mix (`albumMixesDocWithMedia`) and homogenizes the whole group into a pure file album, with per-item composition in rejection logs (`describeAlbum`). File: `storage/telegram/telegram.go`.
- **`f5c451d` — video metadata probing + photo slot guard**: symptom: iPhone HEVC clips forced an ffprobe roundtrip; gomedia results with zero duration/dimensions from odd track headers were trusted; non-jpeg/png stills sent as `InputMediaUploadedPhoto` died with PHOTO_EXT_INVALID. Fix: `getMP4Meta` accepts H265/HEVC; degenerate gomedia results fall through to ffprobe (covers MKV/WebM/AVI/MOV); only jpeg/png go to the photo slot and album eligibility matches the same rule. Files: `storage/telegram/telegram.go`, `storage/telegram/util.go`.
- **`e78c0b0` — survive album rejections + resume dir placement**: symptom: failed production items, all atomic sendMultiMedia rejections; and resume state landed inside the wiped cache dir. Fix: (1) a video with unparseable/zero metadata is sent as a plain document instead of a zero-valued `documentAttributeVideo` Telegram rejects; (2) on deterministic MEDIA_* album rejection the group is re-sent per-item (group send is atomic, nothing was delivered); (3) trailing separators stripped from temp base_path so the resume dir is a sibling of, not inside, the cache dir. Files: `storage/telegram/telegram.go`, `common/tdler/resume.go` (+ test).
- **`d3664f8` — resume state inside the cache mount**: symptom: the sibling-resume-dir layout put resume state on the container overlay while the cache dir is a bind mount — every completed download died at the final rename with EXDEV, stranding 400MB .part files. Fix: resume dir is now `<cache>/.resume` (same filesystem by construction); boot clean spares it and drops the legacy sibling. Files: `cmd/run.go`, `common/tdler/resume.go` (+ test).
- **`d9a304d` — saved-files conflict tracking**: symptom: `Telegram.Exists` always returned false (Telegram has no existence query), so conflict checks never fired, re-runs duplicated everything, recovery-skip had nothing to skip against. Fix: every successful upload path (plain, split zip, lossless video parts, albums) records (storage, path) in `telegram_saved_files` best-effort; `Exists` consults it. Files: `database/telegram_saved.go` (+ test), `database/db.go`, `storage/telegram/telegram.go`.
- **`45375da` — album path + cache boot clean review fixes**: symptom: wasted retries on deterministic album rejections before the per-item fallback; absolute `base_path` got mangled by the boot clean; resume GC didn't run under `no_clean_cache`. Fix: retry loop breaks on `isMediaRejectedByTelegram` (extended to PHOTO_EXT_INVALID/PHOTO_INVALID_DIMENSIONS/VIDEO_INVALID); fallback reuses already-uploaded prepared handles (zero new bytes); `filepath.Abs` for cache path; resume GC runs regardless. Files: `cmd/run.go` (+ test), `storage/telegram/telegram.go` (+ album_test).

## 12. I18N KEYS

Compared three ways: fork vs v0 base = **52 fork-added keys**; fork vs `upstream/main` = 82 keys the fork carries that v1 lacks (subset: the 52 plus ~30 v0 keys v1 deleted — e.g. `BotMsgCancelUsage`, `BotMsgCmdDl`, ytdlp message keys — which the fork still uses, so a v1 port must re-add those or re-key them). Locales: `common/i18n/locale/en.yaml` 544 lines, `zh-Hans.yaml` 548.

Fork-added keys grouped by feature (names verbatim):
- **/history (20)**: `BotMsgCmdHistory`, `BotMsgHistory{Usage,Empty,ErrorQuery,NoMatches,PageOutOfRange,Header,SearchHeader,Row,SourceKnown,SourceUnknown,Footer,StatsHeader,StatsByStorageTitle,StatsStorageRow,StatsByDayTitle,StatsDayRow,StatsFooter}`.
- **/purge (5)**: `BotMsgCmdPurge`, `BotMsgPurge{Usage,InfoDone,ErrorNotFound,ErrorFailed,ErrorPartial}`.
- **/status (8)**: `BotMsgCmdStatus`, `BotMsgStatus{NotPermitted,Body,CacheLabel,DiskRow,NoLocalDisks,Unknown}`.
- **/id (4)**: `BotMsgCmdChatid`, `BotMsgChatid{Usage,Info,ErrorResolveFailed}`.
- **watch extensions (5)**: `BotMsgWatchCatchupSummary`, `BotMsgWatchErrorDirPathInvalid`, `BotMsgWatchInfoWatchList{StoragePrefix,DirPrefix,MediatypePrefix}`, `BotMsgWatchNotifyAlbumDetected`.
- **dedup counters (4)**: `BotMsgCommonInfoAllDedupFilesSkipped`, `BotMsgCommonInfoBatchTasksAddedWithDedup`, `BotMsgCommonInfoBatchTasksAddedWithSkippedAndDedup`, `BotMsgProgressBatchSummaryDeduped`.
- **media filter / config UI (5)**: `BotMsgCommonErrorInvalidMediaTypeFilter`, `BotMsgConfigButtonMediaFilter`, `BotMsgConfigButtonMediaFilterAll`, `BotMsgConfigPromptMediaFilter`, `BotMsgConfigMediaFilterNone`.
- **recovery (2)**: `BotMsgRecoveryInfoTaskRecovered`, `BotMsgRecoveryInfoSummary`.

## 13. MISC

- **`client/user/auth_terminal.go` (57)**: `terminalAuthConversator` implementing gotgproto's auth conversator interface for terminal userbot login; invoked from `userclient.Login`. Hazard: pure gotgproto API surface; must be rewritten against mygotg's auth flow (check what v1's web-console userbot login already covers before porting).
- **`client/bot/handlers/utils/dirutil/sanitize.go` (60)**: `SanitizeRelDirPath` — rejects absolute/UNC/drive/~ forms, **drops** `..` segments rather than resolving, normalizes `\`, caps 512. Callers: watch creation and flush-time defense. Tested.
- **`pkg/tfile/sanitize.go` (31)**: `SanitizeFileName` — reduces to one safe path component (strips traversal to base name, empty→"file"). Callers: every TG file name at construction and rename, so journal payloads and destination paths can never escape via crafted names. Tested.
- **`common/tdler/refresh.go` (93)**: expired `file_reference` refresh. `IsFileReferenceExpired` (FILE_REFERENCE_EXPIRED); `SendCtxForFile` (the client that built the file must also refresh/send it — account-scoped hashes); `RefreshFileLocation` (re-fetches the source message and refreshes in place; `ErrSourceGone` for deleted sources); `DownloadParallelWithRefresh` (up to 2 refresh+restart cycles). Callers: tfile + batchtfile download paths.
- **`config.example.toml` fork-only keys**: `telegram.media_group_timeout = 10` (documented by fork) and the `[task]` section with `recovery = true` (fork-added, superseded by v1 native resume — do not port). `topic_id` is a fork-added config field not in the example file.
- **`api/handlers.go` / HTTP API**: the API itself is v0-inherited; the fork's only change is a startup warning when the API binds a non-loopback address with a bearer token (`api/server.go:150-155`). v1 has its own full API + web console; do not port the v0 API surface. The loopback warning concept may re-appear as a v1 web listen check if desired (optional).

## PORT EFFORT TABLE

| Feature | Effort | Hardest hazard |
|---|---|---|
| 1. Watch extensions | **XL** | Shared intake + cursor semantics span gotgproto ext contexts, the fork's task factories, and the partial-index migration; mygotg has no equivalent of the `ext.NewContext` per-goroutine copy trick, and the catch-up page planner must be re-proven against mygotg's raw API client. |
| 2. Dedup 3-layer | **M** | v1 storage-name→ID mapping touches every dedup call site (12 call sites in 8 task types); the (user,storage) scoping key changes shape. |
| 3. History + /purge | **S** | Reads the dedup table only; ports with the table. |
| 4. Ops /status /id /task-queue | **S** | `/id` depends on userbot resolve; v1 replaces viper config reads with settings/SQLite — the disk-free and DB-size readouts need new accessors. |
| 5. Resend + SkipLarge | **M** | `ExistingMediaSender.SendExistingMedia` signature takes a gotgproto ext.Context; the ambiguous-vs-fallback outcome taxonomy must be preserved exactly or it will mint duplicates. |
| 6. Forum topics | **S** | Single choke point (sendBuilder); only config key remapping in v1's storage records. |
| 7. Stall watchdog + floodwait meter | **S** | Framework-independent (`context` + gotd downloader); re-wiring `Observe` into mygotg-era download builders is the only work. |
| 8. Minio storage | drop | Deprecated; upstream v1 already migrates minio→S3 records. |
| 9. Dockerfile.micro/pico | **S** | Upstream v1 deleted them; recreate adapted (drop `no_minio` tag — no minio in v1; check whether `no_jsparser` still exists; `sqlite_glebarez` still supported). |
| 10. DB importer | **M** | Five tables, two different "user id" semantics (chat-ID vs row-ID), storage-name→v1 storage-ID mapping, WAL-mode sqlite read, viper-default path discovery (`data/saveany.db`). |
| 11. TG hardening fixes | **M** | Fixes are behavioral, not structural — but they live inside `storage/telegram`'s send paths, which v1 rewrote; each fix must be re-applied and re-tested there. |
| 12. I18n keys | **S** | ~52 keys + ~30 reused v0 keys v1 dropped; mechanical but the v1 key namespace was reorganized. |
| 13. Misc (sanitizers, refresh, api warning) | **S** | Sanitizers and refresh port as-is (framework-free); auth_terminal is likely unnecessary (v1 web console has userbot login). |

**Recommended port order** (dependency-driven): 2+5+11 (storage/task foundations) → 7 (download guards) → 10 (importer) → 3+4+6+12 (leaf commands) → 1 (watch, consumes everything above) → 13/8/9 (finishing).

## Limitations

- Did not run the test suite (`go test ./...`), so "tests covering it" reflects test files and function inventories, not pass status.
- Upstream v1 was assessed at tree level; mygotg API equivalents are identified at the "what must change" level, not exact function mappings.
- `core/tasks/batchtfile/execute.go` (~700 lines) and `shortcut/tftask.go` were read at dedup/resume-relevant sections only.
