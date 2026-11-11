# Migrating production data from v0 (fork) to v1

This guide moves a running v0 fork install (users, dirs, rules, watch chats
with their catch-up cursors, the dedup ledger, the telegram saved ledger,
storages) onto the v1 branch. The v0 machine is NEVER written: the source
database is opened read-only and the import lands in one transaction of the
new v1 database.

## What carries over

| v0 | v1 | Notes |
|----|----|-------|
| users (flags: silent, apply_rule, watch_notify, filename strategy/template, conflict strategy, media filter) | users | joined by chat_id; existing v1 rows keep their enabled flag |
| default storage / default dir | remapped by name / imported dir | unresolvable names are skipped and counted |
| dirs | dirs | `(user, storage, path)` deduplicated |
| rules | rules | `storage_name` → storage ID; an unresolvable name degrades to a dir-only rule |
| watch_chats (+ cursors) | watch_chats | invalid filters/mediatypes/dirs are dropped (field only, watch kept); `(user, chat)` collisions keep the higher cursor |
| saved_file_dedups | saved_file_dedups | keyed by chat ID in v0 — remapped to the v1 user row |
| telegram_saved_files | telegram_saved_files | name → storage ID |
| pending_tasks | **NOT migrated** | in-flight journal rows are counted and reported — drain the queue first (step 2) |
| storages | storages | imported from `config.toml` by the upstream TOML import (step 4) |
| bot/userbot session files | **NOT portable** | re-login required (step 6) |

## Prerequisites

- A v0 data dir you can stop and copy (contains `saveany.db`, sessions,
  downloads). **The v0 dir contains session files (secrets) — protect the
  backup accordingly.**
- The v0 `config.toml`.
- The v1 fork binary or image (this branch).

## Steps

### 1. Back up

Copy the ENTIRE v0 data dir and `config.toml` somewhere safe. Copy — never
move — the v0 database; v1 never opens the v0 dir, so the original stays a
working rollback artifact.

### 2. Stop the v0 bot and drain the task queue

Wait for running/queued tasks to finish (or cancel them). In-flight
`pending_tasks` rows are NOT migrated; the import summary reports the exact
count if any remain. A clean stop also lets SQLite checkpoint its WAL, so
the copied DB is consistent.

### 3. Copy the v0 database aside

`copy saveany.db v0-snapshot.db` (or `cp`). Copy, never point at, the v0 DB.
If the bot had to be killed, run the v0 binary once more and shut it down
cleanly so the WAL is checkpointed before copying.

### 4. Deploy v1 with a FRESH data dir + both import flags

```bash
saveany --data-dir /data-v1 \
        --config /path/to/config.toml \
        --fork-db /path/to/v0-snapshot.db
```

- `--config` is **mandatory** for a meaningful `--fork-db` import: the TOML
  import creates the `storages` rows that the name→ID mapping needs. Without
  it the importer refuses (no marker written) instead of importing an
  all-skips snapshot — pass the old config and retry.
- The same-data-dir path is **unsupported and actively refused**: pointing
  `--data-dir` at the v0 dir would make v1 open (and migrate) the v0
  database before any guard could run, destroying the rollback artifact. The
  boot guard detects a v0-shaped file at the active path and refuses with the
  remediation message.
- Docker: pass the same flags; mount the fresh dir and the snapshot read-only
  (`-v /backups:/srv/v0:ro`, `--fork-db /srv/v0/v0-snapshot.db`).

Docker images: `micro` and `pico` build with `-tags=no_jsparser,no_playwright`
(+`sqlite_glebarez` on pico); they bundle NO ffmpeg/yt-dlp (telegram
auto-split of oversized videos and yt-dlp parsing degrade — absence is warned
at config time) and no web UI bundle (the API serves, the UI route is blank).

### 5. First boot — expected log lines

```
Legacy configuration imported into the database
... Migrated N MinIO storage record(s) to S3     (only if v0 had minio rows)
Fork database imported  users_created=... dirs=... rules=... watches=...
                        dedup_rows=... tg_saved_rows=... storage_name_skips=...
Fork database import: N in-flight task(s) were NOT migrated...
```

The import runs exactly once (marker `meta.fork_db_imported`); later boots
skip it.

### 6. Re-authenticate

- Web console: set the admin password (first boot prints a generated one at
  Warn).
- Bot token: re-add in the web console (v0 session files are not portable).
- Userbot: log in through the web console.

### 7. Verify

- Watches: web console or `/watch` list shows every watch with its
  storage/dir/mediatype/filter prefixes.
- `/history stats` shows ledger totals; spot-check a few v0-era files
  appear.
- Restart once and watch the log: `Watch catch-up` should report fetched=0
  for quiet watches (silence in the chat), proving cursors carried over.
- One test save; repeat it — the second attempt should hit the dedup
  ledger.
- MinIO: configure it as an **S3** storage (v1 has no minio type; legacy
  minio rows were auto-migrated). MinIO needs path-style addressing unless
  behind a virtual-host-capable proxy:

```json
{
  "name": "minio",
  "type": "s3",
  "endpoint": "http://minio.example:9000",
  "access_key": "...",
  "secret_key": "...",
  "bucket": "saveany",
  "base_path": "",
  "region": "us-east-1",
  "virtual_host": false
}
```

  Verify in order: test-connection (`POST /api/v1/storages/test`), save one
  file, confirm the object in the bucket.

## Rollback

- **Back to v0**: the v0 data dir is untouched by construction — stop v1 and
  start the v0 binary on the old dir.
- **To upstream v1**: once migration 2 is applied, upstream v1 binaries
  refuse the fork database (schema version newer than supported). To hand the
  DB to upstream, delete the fork schema version row and drop the fork
  tables/indexes:

```sql
DELETE FROM schema_migrations WHERE version > 1;
DROP TABLE IF EXISTS saved_file_dedups;
DROP TABLE IF EXISTS telegram_saved_files;
DROP INDEX IF EXISTS idx_dedup_fileid;
DROP INDEX IF EXISTS idx_dedup_hash;
DROP INDEX IF EXISTS idx_dedup_chat;
```

  Watch extension columns and `users.media_filter` are additive; upstream's
  AutoMigrate tolerates them but its code ignores them. The watch cursor
  (`last_message_id`) is preserved by this downgrade.
- Selfupdate only selects releases from the fork repository
  (`vibecoder11200/SaveAny-Bot`), so it can never pull an upstream binary
  over a fork install. Fork releases must carry fork tags.
