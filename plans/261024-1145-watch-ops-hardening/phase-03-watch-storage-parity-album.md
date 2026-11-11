---
title: "Phase 3: watch storage/dir + parity + albums (v0.67.0 part 1)"
status: done
---

# Phase 3: Watch per-watch storage/dir + msgre parity + always-group albums

> Updated: Red Team Session 1 (2026-10-24) — findings A1/A2/A3/A4/A5/A9/A10/F2/F3/F4/S1/S6 applied. This is NOT a pure extraction of the batch shortcut; the core gains new modes. /save inherits two deliberate behavior fixes (≤1-file keep, collision suffix).

## Context Links

- Scout: `plans/reports/Explore-261024-1827-watch-pipeline.md`; Research: `plans/reports/researcher-261024-1827-upstream-drift.md`
- Decisions: plan.md #2 (flag syntax), #5 (always group), #6 (folder naming + collision suffix), #7 (parity)
- Depends: Phase 2 (`parseWatchArgs`, CutPrefix match fix)

## Overview

Per-watch destination override (`storage:`/`dir:` flags stored on `watch_chats`), msgre match-string parity with /save, and watched media groups always routed through ONE batch task (real album send on Telegram storage; first-file-name subfolder with `-HHMMSS` collision suffix elsewhere). Group filtering and album routing decisions happen per GroupedID, not per file.

## Key Insights (red-team verified)

- `CreateAndAddBatchTGFileTaskWithEdit` (shortcut/tftask.go:126-317) CANNOT be reused as-is for watch. Verified blockers: (a) album subfolders only when a rule returns the NEW-FOR-ALBUM sentinel (tftask.go:185) — rule-less callers get flat paths; (b) `applyRule` output clobbers the caller's target (tftask.go:170-186); (c) 7 unconditional `EditMessage(trackMsgID)` sites, no zero-ID guard; (d) conflict-ASK path prompts to message 0 and returns WITHOUT enqueueing (tftask.go:279-281) — silently drops files for unattended callers; (e) album groups with ≤1 file are silently dropped (tftask.go:236-239) — latent /save bug; (f) returns `dispatcher.EndGroups` sentinel. The extraction therefore EXTENDS the core (see Architecture).
- `pkg/tfile/sanitize.go` collapses any path to its LAST component — it is a filename sanitizer, not a path sanitizer. No multi-segment rel-path validator exists in the repo. WebDAV/Alist backends join user paths unguarded (webdav.go:91, alist.go:204); only local guards inside Save (local.go:55-62). A NEW sanitizer is a deliverable.
- Watch match string must be built with `tgutil.GetMediaFileName(event.File.Message().GetMedia())` — NOT `event.File.Name()`. Verified divergence: tfile names are sanitized and fall back to `GenFileNameFromMessage` when the doc has no filename attribute, while /save matches the raw `GetMediaFileName` output (`<docID>.<ext>` fallback) (pkg/tfile/tgfile.go:200-206,107; common/utils/tgutil/media.go:18-36; save.go:188-197). Photos agree; docs without attributes diverge.
- Album members: only one member carries the caption → with parity match string, members 2..N fail a per-file msgre filter. Filtering must be per GroupedID.
- `newWatchNotifyProgress` returns `tftask.ProgressTracker`; batch task needs `batchtfile.ProgressTracker` (different method sets/info types) — an adapter is a required deliverable, not a "check later".
- Live filter order today: msgre filter (:277-296) runs BEFORE the album gate (:350-363).

## Requirements

- [ ] `/watch <chat> [storage:name] [dir:path] [msgre:regex] [mediatype:types]` — flags any order, optional; storage validated (`config.C().HasStorage` + `GetStorageByUserIDAndName`) at creation; `dir:` value passes the NEW `SanitizeRelDirPath` (reject absolute, strip leading `/`, drop any `..` segment, keep interior separators, cap 512 chars) at creation AND at save-time join.
- [ ] Precedence: per-watch override SET → rule layer skipped entirely for that watch (explicit beats implicit); override UNSET → rules as today over user default.
- [ ] msgre matches `caption + " " + tgutil.GetMediaFileName(media)` — byte-identical builder to /save (extract one shared helper, e.g. `mediautil.MatchStringForFilter(msg *tg.Message) string`, used by save.go and watch).
- [ ] Filtering per GroupedID: buffer members first; the group's filter decision comes from the FIRST member that carries a caption (fallback: any member); mediatype filter still per-file (a photo member must not enter a video-only watch's album? NO — decide: mediatype filters per file BEFORE buffering, group decision = majority/all-members-must-pass? → **per-file mediatype gate BEFORE buffer; msgre per-GROUP at flush**).
- [ ] ANY watched media group (GroupedID != 0) becomes ONE batch task via the extended core: Telegram storage → album send; other storages → `dir/albumName/` ALWAYS (not only on rule sentinel).
- [ ] Album groups with exactly 1 surviving member → routed to the SINGLES path (kept, never dropped) — for both watch and /save.
- [ ] Album folder = first file base name w/o ext; collision with non-empty dir → `-HHMMSS` suffix; unusable name → `album-YYYYMMDD-HHMMSS`.
- [ ] Watch flush pins a NON-INTERACTIVE conflict strategy (user's strategy if skip/rename/overwrite, else force `rename`); the ASK branch is unreachable for unattended flushes; every edit goes through a UI sink that is nil-safe (trackMsgID==0 → no API calls).
- [ ] `batchWatchNotifyProgress` adapter: ONE notification per album task (opens via existing watch-notify mechanism, renders batch header + per-item skipped/deduped counts); nil Progress when `/watch notify` off — verify batch execute nil-checks Progress before deref.
- [ ] `/lswatch` shows storage/dir/filter/mediatype; `/watch` help updated — both locales.
- [ ] AutoMigrate adds columns without data loss (ADD COLUMN, no not-null on new fields).

## Architecture

1. **Schema** — `WatchChat` += `StorageName string`, `DirPath string`, `LastMessageID int` (Phase 4 uses the cursor; one migration).
2. **Parser** — extend `parseWatchArgs` with `storage:`/`dir:` single-token flags; `/watch` arg split switches to `strutil.ParseArgsRespectQuotes` (quoted paths).
3. **Sanitizer** — NEW `client/bot/handlers/utils/dirutil/sanitize.go`: `SanitizeRelDirPath(in string) (string, error)` + exhaustive tests (`..`, absolute, leading `/`, `a/../b`, windows separators, unicode, length cap, empty→""). Applied at /watch creation; save-time joins use the sanitized stored value. Optional defense-in-depth (separate tiny commit): `..`-segment guard in WebDAV/Alist path joins mirroring local.go:55-62.
4. **Resolution** — `resolveWatchTarget(w, user, ruleStor, ruleDir)`: override set → (w.StorageName, w.DirPath), rules skipped; else rule result; else user default. Pure function + table test.
5. **Extended batch core** (extract from `CreateAndAddBatchTGFileTaskWithEdit` into `CreateAndAddBatchTGFileTask(ctx, userID, opts BatchOptions)`):
   ```go
   type BatchOptions struct {
       Stor storage.Storage; DirPath string; Files []tfile.TGFileMessage
       Progress batchtfile.ProgressTracker  // nil allowed
       UISink   func(edit func())           // nil → all edits skipped
       Override *Target                     // non-nil → skip rule layer (watch)
       ForceGrouping bool                   // watch: album subfolder by GroupedID even without rule sentinel
       Strategy  string                     // resolved non-interactive strategy for unattended callers
   }
   ```
   `WithEdit` wrapper = core with UISink wired, Override nil, ForceGrouping false, strategy from user — behavior IDENTICAL to today EXCEPT (documented /save changes): ≤1-file album groups are kept (routed to single-file element instead of dropped) and collision-suffix naming applies. Regression: re-run /save batch tests + dev /save; expectation updated for those two deltas.
6. **Parity** — shared match-string helper (GetMediaFileName-based); compiled-regex cache MANDATORY (from Phase 2) reused here.
7. **Group filtering** — live path: per-file mediatype gate stays where it is; msgre moves from per-file (:277-296) to per-GROUP: the buffer flush evaluates the group's match ONCE (string built from the caption-carrying member + GetMediaFileName of EACH member joined — exact semantics: same combined string as /save would build per file; group passes if the FIRST/caption member's string matches; document the choice). Singles (non-grouped) filter per-file as today.
8. **Album gate** — `groupID != 0` → `watchMediaGroupMgr.addFile` always (drop rule-sentinel condition). Flush → `CreateAndAddBatchTGFileTask` with Override (from resolveWatchTarget), ForceGrouping, nil-or-adapter Progress, non-interactive strategy. 1-member flush → singles path element instead.

## Related Code Files

- Modify: `database/model.go` (WatchChat cols), `database/chat.go` (`TouchWatchCursor` guarded UPDATE)
- Create: `client/bot/handlers/utils/dirutil/sanitize.go` + `sanitize_test.go`
- Create: `client/bot/handlers/utils/mediautil/matchstring.go` + test; `album_dir.go` + test (from original plan)
- Modify: `client/bot/handlers/watch.go`, `watch_args.go`
- Modify: `client/bot/handlers/utils/shortcut/tftask.go` (extraction + core modes), `watch_notify.go` (+ `batchWatchNotifyProgress` adapter file)
- Modify: `client/bot/handlers/save.go` (use shared match-string helper — no behavior change)
- Modify: msgelem lswatch renderer, locales (en/zh-Hans) + regen i18nk

## Implementation Steps

1. Model cols + `TouchWatchCursor` (guarded: `UPDATE ... WHERE id=? AND (last_message_id IS NULL OR last_message_id<?)`).
2. `SanitizeRelDirPath` + tests (traversal matrix) — FIRST, it gates the /watch flag.
3. Parser extension + creation validation (storage allowlist + sanitize dir).
4. Shared match-string helper + switch save.go + watch to it (parity done here).
5. Core extraction with BatchOptions (UISink/Override/ForceGrouping/Strategy); adapt WithEdit wrapper; update /save regression expectations for the two documented deltas; add tests: ForceGrouping subfolder without rules; Override skips rules; ≤1-keep; ASK unreachable when Strategy pinned.
6. `batchWatchNotifyProgress` adapter + nil-Progress path test.
7. `albumDirName` + collision tests.
8. Live rewiring: msgre per-group at flush; album gate always; flush → extended core; 1-member → singles.
9. `/lswatch` + help i18n; full gate.

## Todo

- [ ] WatchChat cols + guarded TouchWatchCursor
- [ ] SanitizeRelDirPath + traversal tests
- [ ] Parser storage:/dir: + creation validation
- [ ] Shared match-string helper (save.go + watch parity)
- [ ] Core extraction with BatchOptions + /save regression w/ 2 documented deltas
- [ ] batchWatchNotifyProgress adapter
- [ ] albumDirName collision helper + tests
- [ ] Per-group msgre + always-album gate + 1-member→singles
- [ ] /lswatch + help i18n (en + zh-Hans) + regen
- [ ] Full gate green

## Success Criteria

- Tests: sanitize matrix; resolveWatchTarget precedence; albumDirName base/collision/fallback; ForceGrouping album subfolder WITHOUT rules; Override beats rule; ASK prompt unreachable with pinned strategy; ≤1-file kept; match-string helper byte-equal for /save fixtures.
- Dev: watch `storage:`+`dir:a/b/c` → nested subdirs honored; album → 1 batch task + subfolder; second same-name album → `-HHMMSS`; msgre filename-only match saves; album where only member 3 matches mediatype → member 3 saved via singles, others not.
- AutoMigrate on prod DB copy: columns added, rows intact.

## Risk Assessment

- The extended core changes /save behavior in 2 documented ways (≤1-keep, collision suffix) — deliberate, owner-approved (decision 6 + red-team F2/A4); release notes must call them out.
- Group-level msgre semantics choice (caption-member decides) differs subtly from "every member must match" — captured as explicit requirement; if owner sees wrong albums saved, flip to all-members-must-pass (single-line change at flush).
- Gotgproto boundary: changes stay in handlers/shortcut; adapter isolates batch progress types.

## Security Considerations

- `SanitizeRelDirPath` closes `dir:` traversal for ALL storage backends (WebDAV/Alist joins were unguarded — latent, now gated at creation + sanitized value stored). Regex: compiled cache mandatory + length cap (≤512 chars) at creation (S6). No new logging of file references. Single-user assumption documented in plan.md constraints.

## Next Steps

- Phase 4 consumes `LastMessageID`/`TouchWatchCursor` and the shared intake; its cursor-advance must move to the flush (see phase-04).
