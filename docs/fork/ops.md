# Ops commands

Fork-added operator commands on the v1 branch. All are owner-gated where
noted; the owner is the FIRST whitelisted user, captured once at first boot
and persisted (`meta.owner_chat_id`) — it never silently reassigns when the
whitelist changes.

## /status (owner only)

Instance health readout, every field degrades to `unknown` on error:

- Uptime, running + queued task counts (queued tasks show their position in
  `/tasks` too, `#N` prefix).
- Cache directory size (single-flight measured, 60 s TTL cache).
- Resume-state size: total `.part`/`.bitmap` sidecar bytes under the cache
  dir (v1's native resume artifacts).
- Database file size.
- Disk free for the cache base and every local storage root, deduped per
  filesystem identity.

## /id [@username | id]

Resolves an entity: numeric ID, type (channel / group / user / chat), and
title. Prefers the **userbot** client, so public channels the bot is not a
member of still resolve; falls back to the bot context.

## /history [page] | search <text> [p<page>] | stats

Reads the dedup ledger (see [dedup.md](dedup.md)):

- `/history` — paginated list of saved files (what / where / when / source
  message).
- `/history search <text>` — substring match over path and file id.
- `/history stats` — totals, per-storage breakdown, recent activity.

## /purge <storage name> (owner only)

Clears BOTH ledgers of one storage: the content dedup ledger (scoped to the
storage) and the telegram saved-path ledger. Use it after deleting files by
hand outside the bot — the ledgers would otherwise block re-saving the same
content. Reports per-ledger counts and warns on partial failure.
