# Scout: queue, ledger, help/i18n, /update, fs helpers

Source: Explore agent, 2026-10-24. Evidence for plan `261024-1145-watch-ops-hardening`.

## Task queue

- `pkg/queue`: `TaskQueue[T]` — `tasks *list.List` (FIFO), `taskMap`, `runningTaskMap`, cond. Status implicit: in list = queued, in runningTaskMap = running, gone after Done. `Task[T]{ID, Title, Data, ctx, cancel, created, element}`; `TaskInfo{ID, Created, Cancelled, Title}`. `QueuedTasks()` returns enqueue-order slice → **position = index+1 available today**. Methods: Add/Get/Done/Length/ActiveLength/RunningTasks/QueuedTasks/CancelTask/Close.
- `core/core.go`: `Run` spawns `config.C().Workers` workers with semaphore (concurrency = Workers, config/viper.go:21, clamp ≥1 :179-180). `AddTask` (:119-145) journals. Accessors: `GetLength`, `GetRunningTasks`, `GetQueuedTasks` (:158-166).
- `/task` handler (tasks.go): subcommands running/queued/cancel; `showQueuedTasks` caps at `maxShown = 10` with truncated note; no position numbering today. Queue length already surfaced in `BuildTaskAddedEntities` (msgelem/task.go:15-38) via `core.GetLength` at shortcut add sites.

## Ledgers (see watch-pipeline report for structs)

- Rich ledger for /history = `SavedFileDedup` (per-user, storage, file-unique-id, hash, size, path, created). No chat/msg columns; no list/search/stat queries exist yet. RecordDedup sites: tfile:172,207; batchtfile:408,452; aria2dl:217; ytdlp:222; directlinks:199; parsed:167; telegraph:109; transfer:134.
- Only existing ledger UI: `/purge` (purge.go).

## Help / commands

- help.go: `handleHelpCmd` renders `bot.msg.help_text_fmt` with Version + 7-char GitCommit; /start == /help.
- en.yaml `bot.msg.help_text_fmt` (:22-45) — ghost `/import` at :32 + ghost `cmd.import` :56 (zh-Hans :33). Help OMITS registered: /dl /aria2dl /ytdlp /transfer /cancel /id /purge.
- Registered commands (register.go:24-48, loop :58-60): start silent storage dir rule save dl aria2dl ytdlp transfer task cancel config fnametmpl help parser watch unwatch lswatch syncpeers id purge update (23; no /import). Callbacks: "update", Add/SetDefault/Cancel/Config (:61-65).

## /update

- update.go: `handleUpdateCmd` (:21-100) semver-parses config.Version → `ghselfupdate.DetectLatest(config.GitRepo)` → block major → release-notes blockquote → Docker=="true" report-only (:69-77) else inline button; callback `handleUpdateCallback` (:102-134) selfupdate.Update + RequestRestart + sentinel error.
- `config/version.go:13` `const GitRepo = "krau/SaveAny-Bot"`; usages: handlers/update.go:29, cmd/version.go:38, common/selfupdate/selfupdate.go:81. Version/BuildTime/GitCommit/Docker = ldflags vars (:5-10).

## i18n

- `common/i18n/i18n.go`: embed `locale/*`, go-i18n bundle, default zh-Hans, `T(key, templateData)` returns raw key on failure. Locales: en.yaml + zh-Hans.yaml (both required — gen fails otherwise, geni18n/main.go:63-79). `i18nk/keys.go` generated (`bot.msg.cmd.help` → `BotMsgCmdHelp`). Add-string steps: keys in BOTH yamls → `go run ./cmd/geni18n` (defaults -dir ./common/i18n/locale -out common/i18n/i18nk/keys.go -pkg i18nk) → use consts in CommandHandlers + handlers. Tasks i18n block en.yaml:189-210 is the pattern for new commands.

## FS helpers / uptime

- No dir-size or disk-free helper exists anywhere (only filepath.WalkDir uses in cmd/watch, ytdlp, geni18n). `common/utils/fsutil/fs.go`: Sha256File, RemoveAllInDir, DetectFileExt only.
- `config.C().Temp.BasePath` (config/temp.go:4), validated cmd/run.go:118-142; boot cache clean spares `.resume`. `ResumeDir()` = BasePath/.resume (tdler/resume.go:376-382) — holds `stem.part` + `stem.part.bitmap`.
- No process start-time tracking exists (only per-download timers) — /status needs its own `startTime`.
- go.mod: `golang.org/x/sys v0.48.0 // indirect` (promotable for windows disk-free).
