---
title: "Phase 2: bugfix-batch (v0.66.6)"
status: done
---

# Phase 2: Bugfix batch — v0.66.6

## Context Links

- Scout: `plans/reports/Explore-261024-1827-watch-pipeline.md` (watch.go msgre bugs), `plans/reports/Explore-261024-1827-queue-ledger-help.md` (help text, /update, i18n)
- Decisions: plan.md #4 (fork repo), #7 (parity — colon fix ships here, parity itself in Phase 3)

## Overview

Three low-risk fixes shipped as one small release: (a) watch `msgre:` regex truncation on `:` (both parse and match sites), (b) help text sync with registered commands (drop ghost `/import`, add missing commands), (c) `/update` points at the fork repo.

## Key Insights

- Bug sites: `client/bot/handlers/watch.go:82-83` (`strings.Split(filterArg, ":")[1]` truncates) and `:278-279` (requires exactly 2 parts after split → regex containing `:` silently skipped at match time).
- Ghost `/import`: `common/i18n/locale/en.yaml:32` + `:56` (`cmd.import`), mirrored in `zh-Hans.yaml:33` + its `cmd.import`. Help omits `/dl /aria2dl /ytdlp /transfer /cancel /id /purge` (registered list in `register.go:24-48`).
- `config/version.go:13` `const GitRepo = "krau/SaveAny-Bot"` → used by `handlers/update.go:29`, `cmd/version.go:38`, `common/selfupdate/selfupdate.go:81`. One-line change fixes all three.

## Requirements

- [ ] A watch regex containing `:` (e.g. `msgre:(?i)^(hd|4k):`) is stored, listed via `/lswatch`, and matched intact.
- [ ] `/help` lists exactly the 23 registered commands; no `/import` anywhere; both locales updated.
- [ ] `/update` checks releases of `vibecoder11200/SaveAny-Bot`; Docker mode remains report-only.
- [ ] `go test -race` green; new parser unit tests included.

## Architecture

Introduce a tiny flag scanner shared by parse and (later, Phase 3) `storage:`/`dir:` — put it in a new file `client/bot/handlers/watch_args.go` so upstream's watch.go edits stay mergeable:

```go
// parseWatchArgs splits /watch args into chat + flag values.
// Grammar: /watch <chat> [msgre:<regex may contain ':' and spaces if quoted>]
//          [mediatype:types]  (storage:/dir: added in Phase 3)
// The msgre regex extends until the next flag token; remaining args rejoin with " ".
```

Match-time fix in `listenMediaMessageEvent`: replace `strings.Split(chat.Filter, ":")` with `strings.CutPrefix(chat.Filter, "msgre:")` — remainder is the whole regex.

Help: rewrite `bot.msg.help_text_fmt` in BOTH `en.yaml` + `zh-Hans.yaml`; delete `bot.msg.cmd.import` key; `go run ./cmd/geni18n` regenerates `i18nk/keys.go` (removes `BotMsgCmdImport` const — verify no references, none exist today).

## Related Code Files

- Modify: `client/bot/handlers/watch.go` (parse :80-99, match :277-296)
- Create: `client/bot/handlers/watch_args.go`, `client/bot/handlers/watch_args_test.go`
- Modify: `common/i18n/locale/en.yaml`, `common/i18n/locale/zh-Hans.yaml`; regenerate `common/i18n/i18nk/keys.go`
- Modify: `config/version.go` (GitRepo const)

## Implementation Steps

1. Write `watch_args.go`: `type watchArgs struct { Chat string; MsgRe string; MediaType string }` + `parseWatchArgs(args []string) (watchArgs, error)`. Token scan: arg starting with a known prefix consumes it; first non-flag arg = chat; a `msgre:` flag's value = remainder of that arg + every following non-flag arg joined by `" "` (mirrors /save's whole-tail regex grammar). Reject unknown `xxx:` flags with the existing invalid-filter error.
2. Rewire `handleWatchCmd` (:73-99) to use it; keep storing `filter = "msgre:" + regex`.
3. Match site (:277-296): `data, ok := strings.CutPrefix(chat.Filter, "msgre:")`; if !ok skip as today; **compiled-regex cache is MANDATORY** (red-team S6: grammar widens in this release and Phase 4 feeds 500 backfilled messages × watches through it — `sync.Map` keyed by filter string, compiled once at watch creation and cached at match time) + cap regex length ≤512 chars at creation.
4. Locales: fix help_text_fmt (both), remove cmd.import (both); run `go run ./cmd/geni18n`.
5. `config/version.go`: `GitRepo = "vibecoder11200/SaveAny-Bot"`.
6. Tests (`watch_args_test.go`): table — flags any order; regex containing `:`; multi-word regex; unknown flag rejected; mediatype+msgre combined; match-time CutPrefix round-trip of stored form.
7. Gate: gofmt, `go vet ./...`, `go test -race ./...` (standard skips), `GOOS=linux go build ./...`.

## Todo

- [ ] watch_args.go + parse rewire + match-site CutPrefix fix
- [ ] Help text sync (both locales) + drop cmd.import + regen i18nk
- [ ] GitRepo → fork
- [ ] Unit tests + full gate

## Success Criteria

- `watch_args_test.go` passes incl. colon/space regex cases; `/lswatch` shows full regex; `grep -r "cmd.import\|/import" common/i18n/locale/` empty; `go test -race` green. Deploy + prod checks per Phase 6.

## Risk Assessment

- Compiled-regex caching adds a map — skip if it complicates; MatchString-per-event is today's behavior. Low risk overall: parse change is behind the same error paths; one behavioral change = regexes with `:` now VALID (previously truncated/skipped — strictly a fix).

## Security Considerations

- Owner-supplied regexes only (same trust as /save). This release WIDENS the regex grammar (`:` now legal, multi-word tails join) — mitigations shipped together: mandatory compiled-regex cache + 512-char cap (S6); catastrophic backtrack remains owner-self-inflicted, same as /save. No secrets involved.

## Next Steps

- Phase 3 builds on `parseWatchArgs` (adds `storage:`/`dir:`) and the same filter grammar.
