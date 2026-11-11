# Watch extensions

The fork extends upstream's `/watch` with per-watch destination overrides,
message filters, media-type filters, catch-up after restart, and true album
saves. This page describes the implemented behavior on the v1 branch.

## Syntax

```
/watch <chat_id/@username> [storage:<name>] [dir:<path>] [msgre:<regex>] [mediatype:<types>]
```

- Flags appear in any order, all optional. The chat argument is positional
  (first non-flag token).
- `msgre:` — a Go regex matched against `<caption> <media filename>` (the
  exact same string `/save` matches, so a regex that works in one place works
  in both). It may contain `:` and spaces; it ends at the next flag. Max 512
  characters (a longer pattern is rejected at creation: live events and
  catch-up feed every observed message through it).
- `mediatype:` — comma-separated fine tokens: `photo`, `video`, `animation`,
  `audio`, `voice`, `sticker`, `document` (aliases `gif`, `music`, `vid`,
  `img`, `doc`). Unknown tokens are rejected (fail-closed).
- `storage:` — destination storage by name; resolved to its database ID at
  creation, so renaming the storage never orphans the watch (deleting and
  recreating it under the same name does: the watch falls back to your
  default storage and notifies you once).
- `dir:` — relative directory inside the storage; sanitized at creation and
  re-sanitized at save time.
- Overriding one axis keeps your default for the other (`storage:` alone
  targets the storage root; `dir:` alone keeps the default storage). An
  override replaces the rule layer entirely for that watch; without an
  override, rules and defaults apply as upstream.

## Layered media-type filtering

Three layers apply, outermost first:

1. **Instance gate** — the global media filter (config) is never bypassable.
2. **Per-watch `mediatype:`** — when set, it REPLACES your per-user default
   for this watch (override semantics, not conjunction).
3. **Per-user default** — set via `/config`; applies when the watch has no
   `mediatype:`.

## Albums

Any watched media group is saved as ONE batch task after a short debounce:
on Telegram storage the album is sent as a real album; on every other storage
the members land in a folder named after the first file. The `msgre:` filter
is evaluated once per group against the caption-carrying member. A group
reduced to one member (filtered/skipped) is saved as a plain file.

Conflict handling never prompts: your configured strategy applies unless it
is `ask` (degrades to `rename` — a prompt to a nonexistent message would
silently drop files). Album members that hit the dedup ledger or the skip
strategy are dropped from the batch; the save proceeds with the rest.

## Catch-up after restart

The boot catch-up backfills what watched chats delivered while the instance
was down, through the exact same intake path as live events:

- **First boot on a watch** records the newest message as the cursor and
  backfills NOTHING — history before the watch is never fetched.
- Existing watches walk history ascending from the cursor, with a per-boot
  cap of 500 messages (older leftovers are picked up on the next restart) and
  a 10-minute per-watch budget. Nothing is enqueued twice: anything at or
  below the current cursor is skipped.
- The cursor advances only on durable enqueue (or on a live event merely
  being observed), so a crash mid-catch-up never loses or duplicates files.
- Backfill pins the `rename` strategy: overwrite disables dedup, and the
  live/catch-up overlap window would then duplicate files.
- A summary message (fetched / enqueued / albums / capped) is sent when
  something was fetched; quiet restarts are silent.

The catch-up requires the userbot (it fetches history) and runs once per
process start — it never re-runs when telegram settings are saved in the web
console.
