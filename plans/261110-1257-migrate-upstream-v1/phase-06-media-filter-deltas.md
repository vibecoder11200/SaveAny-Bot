---
title: "Phase 6: Media Filter Deltas"
status: todo
phase: 6
priority: P2
effort: "6h"
dependencies: [phase-02-storage-task-foundations (M1)]
---

# Phase 6: Media Filter Deltas

## Overview

Layer the fork's media-filter deltas on top of upstream's instance-wide gate: a per-user default filter (`users.media_filter`, column landed in Phase 2), the `t:` per-command override syntax in `/save`, and a layered `AllowedFor` check applied at the save chokepoint in this phase — the watch-loop wiring lands entirely in Phase 7 together with the watch restructuring — keeping upstream's instance-wide `mediafilter.Allowed` untouched as the outer gate.

## Key Insights

- v1's filter is a package-level function over `config.C().Media.AllowedMediaTypes` with 5 `Allowed(media)` call sites (`common/mediafilter/mediafilter.go:12-33`; sites: `client/bot/handlers/media_group.go:61`, `save.go:145`, `utils/shortcut/message.go:38,100`, `watch.go:333`) plus 1 typed `Allows(mediatype.Image)` call (`utils/shortcut/message.go:177`, telegraph flow) that `AllowedFor` cannot substitute — NOT context-aware; a per-user layer cannot drop in without touching call sites (researcher-01 §9). `AllowedFor` scopes to the 5 `Allowed` sites only; the typed `Allows` site stays as-is.
- Fork `/save` grammar (fork `client/bot/handlers/save.go:91-110`): `/save <chat> <range> [t:types] [regex]` — a `t:` token is always a media-type filter; empty filter after `t:` is an explicit error (fail-closed); when no `t:` given, the per-user default applies (`UserMediaFilter(userDB)`, fork save.go:148-153); layering: command override > user default > (outer) instance gate.
- Fork helpers live in `client/bot/handlers/utils/mediautil/` (`ParseMediaFilter`, `IsMediaFilterSpec`, `UserMediaFilter`, `MediaType`) — framework-free, port as-is.
- The two user-aware chokepoints where the layered check belongs: the save flow after user resolution (v1 `save.go:145` region, via `handleSilentMode`-resolved user) and the watch loop per watch row (v1 `watch.go:333` → per-watch at ≈:350, where the watch row and user are in hand). DEPENDENCY NOTE: ALL watch-loop filter wiring — the per-user default AND the per-watch `mediatype`, composed through `AllowedFor` (fork ordering puts mediatype before msgre) — lands in Phase 7, which restructures exactly that region; wiring it here would mean a double-edit plus an ordering inversion. This phase wires ONLY the save chokepoint and does not touch `watch.go`. The other sites (shortcut message utils, media_group) keep the instance gate only — they run before a user/scope is known or inside flows that re-enter the two chokepoints.
- v1 user settings surface: bot-side `/config` command exists (register.go) and the web console/API has users CRUD — the per-user default needs an editing path; check whether v1's `/config` user section is extensible (fork had config-menu keys for it) before choosing where the toggle lives.

## Requirements

**Functional**
- `users.media_filter` (string, empty = no per-user restriction) parsed with the same grammar as `t:` values; invalid stored values are treated as "no filter" with a once-guarded warning (defensive against manual DB edits).
- `/save ... t:<types>`: per-command override, highest precedence; fail-closed on unknown type tokens or empty set (fork error key `BotMsgCommonErrorInvalidMediaTypeFilter`).
- Layered evaluation: command `t:` > per-user default > per-watch filter > instance-wide `Allowed` (all layers must pass; most-specific acts as additional restriction, never as bypass of the instance gate). This phase wires the SAVE chokepoint only (command `t:` + per-user default over the instance gate); the entire watch-loop wiring (per-user default + per-watch mediatype through `AllowedFor`) is Phase 7's per the dependency note in Key Insights.
- An editing path for the per-user default: v1 `/config` menu section if v1's config menu covers per-user fields, else API `PATCH` users field + document web-console editing; either way the field round-trips through settings load/save (`settings/settings.go:205-307` rebuilds `cfg.Users` from the users table — verify new column is included or ignorable).
- i18n: 5 fork keys (media-filter config UI/error) in both YAMLs.

**Non-functional**
- No behavior change when no filters are configured anywhere (all layers empty ⇒ identical to upstream).
- Filter parsing is pure string work; no config reload needed for per-command `t:`.

## Architecture/Approach

- **Layered function, not context injection**: add `mediafilter.AllowedFor(scopes, media)` taking explicit scope values (command filter, user default, watch filter) instead of researcher-01's context-carried override alternative. Rejected: `mediafilter.WithOverride(ctx, ...)` — the two chokepoints already have all scopes as values; context plumbing would leak the concept into the 5 `Allowed` call sites for no gain.
- **Keep the instance gate first** at all 5 `Allowed` sites (upstream behavior intact; the typed `Allows` site at shortcut/message.go:177 is untouched — not substitutable by `AllowedFor`); the layered check is additive at the save chokepoint here and at the watch chokepoint in Phase 7. Rejected: rewriting all sites — scope creep, and the others lack a user scope.
- **Explicit non-goal**: media_group and shortcut/message util sites keep the instance gate alone; they either re-enter the two chokepoints or run pre-scope. Widening them changes upstream behavior for zero fork value.

**Layering order (conjunction, most specific first):**
`instance Allowed(media)` AND `per-watch mediatype` (Phase 7) AND `per-user default` AND `command t:` — any layer that resolves to a non-empty set not containing the media type blocks it; empty/unset layers pass through.

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Create | `client/bot/handlers/utils/mediautil/media_type.go` (+ test) | Port fork filter grammar helpers |
| Modify | `common/mediafilter/mediafilter.go` | Add `AllowedFor(scopes, media)` layered helper (+ test) |
| Modify | `client/bot/handlers/save.go` | `t:` parsing + layered check at ≈:145 |
| Modify | `client/bot/handlers/config.go` (or api users handler) | Editing path for the per-user default |
| Modify | `common/i18n/locale/{en,zh-Hans}.yaml` | 5 keys + `go generate .` |

## Implementation Steps

1. Port `mediautil` helpers + tests (fail-closed grammar: unknown token → error; empty set → error).
2. Add `AllowedFor` to `common/mediafilter` with a table-driven test covering: each layer alone restricting, most-specific-wins ordering, instance gate still enforced, empty-everywhere = allow (when instance gate allows).
3. Wire `/save`: parse `t:` per fork grammar (quote-aware splitter already used by v1 handlers); store on the save flow; apply layered check where v1 currently calls `mediafilter.Allowed` at save.go:145 — command filter > user default > instance.
4. No watch-loop edits in this phase: ALL watch-loop filter wiring (user default + per-watch mediatype, through `AllowedFor`, fork ordering mediatype-before-msgre) moves to Phase 7, which restructures exactly that region — wiring it here would be double-edited and order-inverted. Phase 6 delivers only: the `users.media_filter` column, the `AllowedFor` helper, the `/save t:` syntax, and the config editing path.
5. Editing path: inspect v1 `handleConfigCmd` scope; if it edits user fields (silent, filename strategy…), add a media-filter section with the 5 fork keys; else add the field to the API users update handler and document web-console editing. Verify `settings.Load` users rebuild tolerates the new column.
6. i18n keys, `go generate .`, verification.

## Todo

- [ ] mediautil helpers ported with fail-closed tests
- [ ] `AllowedFor` layered helper + tests
- [ ] `/save t:` syntax + precedence wired
- [ ] No `watch.go` edits — all watch-loop filter wiring lives in Phase 7 (dependency note in Key Insights)
- [ ] Per-user default editing path (config menu or API field) verified round-trip
- [ ] Instance gate still enforced at all 5 `Allowed` sites + the typed `Allows` site (no site weakened)
- [ ] i18n keys + `go generate .`

## Verification

```bash
go build ./... && go vet ./...
go test ./common/mediafilter/...
go test ./client/bot/handlers/... -run "Media|Save"
go generate . && git diff --stat -- common/i18n/i18nk/keys.go
```

Behavioral matrix to assert in tests (table-driven): (no filters) allow; (instance blocks video, others empty) block; (user default photos-only) block document; (t:document overrides user photos-only) allow document when instance allows; (t: empty) error; (t: bogus) error.

## Risk Assessment

1. **Settings quartet omission** (low likelihood, high impact): if the per-user default is exposed via settings groups rather than the users table, it must join `coreGroup`/`applyGroup`/`validateGroup`/`restartRequired` (researcher-01 §12) — unknown fields are rejected. Mitigation: keep the filter on the users table row (already migrated), which the users CRUD path already serializes; step 5 verifies.
2. **Silent bypass of instance gate** (low, high): a mis-ordered layer could let a user default override the instance allowlist. Mitigation: `AllowedFor` composes conjunctions only; the test matrix asserts the instance gate always wins.

## Security Considerations

- Filters are restrictions, not permissions — a bug can only over-block or under-block media types, never grant access to chats or storages. Keep it that way: do not couple filter state to any authorization decision.
- Stored `users.media_filter` is operator/user-supplied; parse defensively (invalid ⇒ no filter + warning) so a corrupted row cannot brick saving.

## Next Steps

Phase 7 (watch extensions) wires the entire watch-loop filter layering (per-user default + per-watch `mediatype:` through this phase's `AllowedFor`, fork ordering mediatype-before-msgre) on top of this helper, and builds on Phase 3's task plumbing.
