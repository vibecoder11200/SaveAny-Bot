# Content dedup (3 layers)

The fork deduplicates saves per user across three layers, checked in order —
the first hit skips the work of the layers below it. A re-save of identical
content is blocked BEFORE any download or upload happens.

## Layer 1 — Telegram file ID

Checked at task creation. Telegram assigns a stable file ID to identical
media; a hit means the exact file was already saved by this user to this
storage. Zero network I/O to decide.

## Layer 2 — Content hash

Checked at task creation for sources that have no file ID (the sha256 of
content seen before). Applies across renames and name collisions.

## Layer 3 — Re-save ledger (telegram saved paths)

For telegram storage: a ledger of every path this bot has saved through that
storage. Telegram offers no way to ask whether a file already exists in a
chat, so conflict detection and skip-on-recovery ride on this ledger. Files
placed into the chat by hand are invisible to it.

## Semantics

- **Scoping**: every layer is scoped per (user, storage). Users never see
  each other's saves; the same content on another storage re-saves.
- **Fail-open**: a ledger error never blocks a save — the worst case is a
  duplicate, not a lost file.
- **API-created tasks carry no user context** (`userID = 0`): dedup is
  disabled for them by design, so the web API cannot be poisoned by or poison
  user-scoped ledger rows.
- **Recording is idempotent** against partial unique indexes; a resend
  (file re-referenced without moving bytes) records an empty hash — the bytes
  never touched this disk.
- **Overwrite disables creation-time checks**: choosing overwrite means the
  user asked to replace, so dedup does not second-guess it.
- Watch tasks run the same layers, except that the skip strategy's
  exists-check replaces the skip prompt in unattended flushes.

## Inspecting and clearing

- `/history` shows what the ledgers remember (see [ops.md](ops.md)).
- `/purge <storage>` clears both ledgers for one storage — after deleting
  files by hand outside the bot.
