# Research: gotd/td v0.149.0 watch catch-up (channel history backfill)

Source: researcher agent, 2026-10-24. Verified against pinned versions: `gotd/td@v0.149.0`, `gotd/contrib@v0.21.1`, `gotgproto@v1.0.0-beta22` (module cache) + core.telegram.org.

## Headline

Use raw `tg.Client.MessagesGetHistory` with forward paging: `OffsetID = lastSeen, AddOffset = -limit, Limit = 100`, reverse each batch, `len(batch) < limit` = done. Do **not** use gotd's `telegram/query` iterator — in v0.149.0 its builder silently drops `MinID`/`MaxID` (`telegram/query/messages/queries.gen.go:213-222`) and pages descending only, with default `BatchSize` 1.

## Key facts

- `tg.MessagesGetHistoryRequest{Peer, OffsetID, OffsetDate, AddOffset, Limit, MaxID, MinID, Hash}`. Results **descending**. `offset = positionOf(offset_id) + add_offset` over existing (non-deleted) messages; `min_id`/`max_id` are strict bounds applied **after** slicing.
- Forward paging recipe (official): `offset_id=lastSeen, add_offset=-limit, limit=limit` → the `limit` messages immediately newer than anchor, descending. Reverse to process chronologically. Re-anchor on the smallest returned ID each batch — never ID arithmetic (deletes leave permanent gaps).
- Deleted messages: never an error, just absent. `len(batch) < limit` only near the true end → reliable terminator. Belt-and-braces: set `MinID: lastSeen`; treat `MSG_ID_INVALID` as "done".
- Message IDs: monotonic per channel, channel-local, never reused. Forum topics share one sequence. `(channelID, msgID)` is the right cursor key. Only `*tg.Message` carries `GroupedID`; skip `MessageEmpty`/service messages.
- Albums: `tg.Message.GroupedID` identical in history and live updates. Album member IDs are consecutive (atomic send) BUT a getHistory page can split an album, and an album can straddle the `lastSeen` boundary (partial processed live before restart). Buffer by `GroupedID`, flush on change, force-flush at end.
- Flood: no `telegram.WithFloodWait` auto-retry in v0.149.0. Use `tgerr.AsFloodWait(err) (d, ok)` → sleep `d+1s`, retry (idempotent GET). Pace ~1 req/s. Contrib `ratelimit.New(rate.Every(...), burst)` / `floodwait.NewWaiter()` available; gotgproto accepts middlewares at construction.
- Spot re-fetch: `channels.getMessages` (NOT `messages.getMessages` for channels) with `[]tg.InputMessageID`; deleted IDs silently skipped (fewer messages returned, no error). Good for album sibling refetch, not for range enumeration.
- `Limit` effectively capped at 100 (server fails silently above).

## Recommended algorithm

1. Persist `lastSeenID` per watch row; update after each batch durably enqueued (at-least-once + dedup = safe).
2. Resolve peer (access hash from gotgproto PeerStorage; response `Chats` can bootstrap).
3. `newest` := getHistory(Peer, Limit=1) → batch[0].ID.
4. Loop: `{Peer, OffsetID: lastSeen, AddOffset: -limit, Limit: limit, MinID: lastSeen}`; reverse; buffer albums; process; `lastSeen = max processed ID`. Stop when `len(batch) < limit` OR max processed ≥ `newest`.
5. Pace 1 req/s; `AsFloodWait` → sleep + retry.

## Pitfalls

Descending order (reverse!); no ID arithmetic for anchors; min/max post-slice short pages; GroupedID only on `*tg.Message`; album split across pages + partial at boundary; persist cursor per (channel); `channels.getMessages` not `messages.getMessages`; wire FLOOD_WAIT yourself; limit ≤ 100.

Sources: core.telegram.org/api/offsets, /method/messages.getHistory, /method/channels.getMessages, /method/messages.getMessages; local module cache paths listed above.
