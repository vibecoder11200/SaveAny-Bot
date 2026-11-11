# Scout: watch pipeline + database layer

Source: Explore agent, 2026-10-24. Evidence for plan `261024-1145-watch-ops-hardening`.

## watch.go (client/bot/handlers/watch.go)

- `handleWatchCmd` (:31-133): `strings.Split(text, " ")`; requires `user.DefaultStorage != ""`; `mediatype:` tokens stripped (:73-79); msgre parse **bug** at :82-83 — joins rest with space, `strings.Cut` then `Split(filterArg, ":")[1]` → any regex containing `:` is truncated; only `msgre` type (:89); watch created :121-126 via `user.WatchChat(ctx, database.WatchChat{UserID, ChatID, Filter, MediaType})` — **no storage/dir fields exist**.
- `watchMediaGroupHandler` (:199-244): `groups map[chatID]map[userID][]tfile.TGFileMessage`, `timers` (`time.AfterFunc` reset on each addFile), flush → callback. Timeout = `max(config.C().Telegram.MediaGroupTimeout, 1)`s (:359).
- `listenMediaMessageEvent(ch chan userclient.MediaMessageEvent, botCtx)` (:246): per event, per matching WatchChat — mediatype filter (:264-276), then filter parse **bug** (:277-296): `Split(chat.Filter, ":")`, requires exactly 2 parts (:279) → regex with `:` silently skipped; `regexp.MatchString(filterData, msgText)` :287-290 where `msgText = event.File.Message().GetMessage()` (:260) — **caption only, no filename**.
- Storage: requires `user.DefaultStorage` (:308), `storage.GetStorageByUserIDAndName` (:312); dir: `user.DefaultDir != 0` → `database.GetDirByID` → `dir.Path` (:317-325).
- Filename strategy inline (:326-348). Rule/album gate (:350-363): album buffering ONLY when `isGroup && groupID != 0 && user.ApplyRule && user.Rules != nil && ruleutil.ApplyRule(...).NeedNewForAlbum()`; otherwise singles.
- Singles path (:365-394): rule applied (`ruleutil.ApplyRule`, storage re-resolved if `matchedStorageName.Usable()`), task = `coretfile.NewTGFileTask(taskid, injectCtx, file, stor, storagePath, newWatchNotifyProgress(...))`, `task.UserID = user.ChatID`, `core.AddTask`.
- Album flush `processWatchMediaGroup` (:399-494): re-applies rules per file, groups by GroupedID, album folder = first file base name w/o ext (:472), per-file `path.Join(af.dirPath, albumDir, af.file.Name())` (:478); groups with ≤1 file DROPPED (:467-469).

## Boot wiring

- `register.go` :41-43 commands (watch/unwatch/lswatch), :58-60 register loop; :71-73 `if config.C().Telegram.Userbot.Enable { go listenMediaMessageEvent(userclient.GetMediaMessageCh(), botCtx) }`.
- Producer `client/user/watch.go`: `MediaMessageEvent{Ctx, ChatID int64, MessageID int, File tfile.TGFileMessage}`; `mediaMessageCh = make(chan MediaMessageEvent, 100)`; `handleMediaMessage` only `*tg.MessageMediaDocument|Photo`; 5s debounce keyed (ChatID, MessageID) via `sendMediaMessageEvent`.
- Pre-filter gate `client/user/userclient.go:98-110`: drops update unless `database.GetWatchChatsByChatID(ctx, chatId)` ≥ 1.

## database/

- `model.go`: `WatchChat{gorm.Model; UserID uint; ChatID int64; Filter string; MediaType string}`; `User{... DefaultStorage string; DefaultDir uint; Dirs; ApplyRule; Rules; WatchChats; WatchNotify; FilenameStrategy; FilenameTemplate; ConflictStrategy; MediaFilter}`; `Dir{UserID; StorageName; Path}`; `Rule{UserID; Type; Data; StorageName; DirPath}`; `PendingTask{ID string xid; UserID int64; Payload string; Attempts; CreatedAt}`.
- `chat.go`: `WatchChat` (append+Save), `UnwatchChat` (Unscoped delete), `WatchingChat`, `GetWatchChatsByChatID(ctx, chatID)`.
- `db.go:46` AutoMigrate list: User, Dir, Rule, WatchChat, PendingTask, TelegramSavedFile, SavedFileDedup. SQLite, `SetMaxOpenConns(1)`.
- `user.go`: GetUserByChatID (Preload clause.Associations), GetUserByID, etc.
- Dedup ledger `saved_file_dedup.go`: `SavedFileDedup{ID; UserID int64 idx; StorageName; FileUniqueID; ContentHash; Size; StoragePath; CreatedAt}` — **no chat_id/message_id columns**; funcs: DedupHasFileID, DedupHasHash, RecordDedup, DedupCheckHash, DedupRecordSaved, DedupSkipByHash, RecordDedupAfterUpload, PurgeDedupByStorage (all fail-open). No list/search queries exist.
- `telegram_saved.go`: `TelegramSavedFile{StorageName, StoragePath uniqueIndex pair, CreatedAt}` + Record/Exists/Purge.
- Restart recovery: `pkg/journal` wraps PendingTask (TGTaskPayload, TGFileRef, Record/Complete/ListPending/BumpAttempts).

## /save resolution + msgre parity

- save.go :188-197: matches `caption + " " + filename` (`tgutil.GetMediaFileName(media)`), compiled raw (:119-126).
- Batch storage/dir resolution in `add_task.go handleAddCallback`: storage `GetStorageByUserIDAndName`; dir from data.DirID → dir.Path, else data.SelectedDirPath, else ""; rules NOT applied in batch save callback; album grouping + subfolders handled inside `shortcut.CreateAndAddBatchTGFileTaskWithEdit` (tftask.go:126) which applies rules per file, groups albumFiles by GroupedID + dirPath, conflict strategy, dedup.
- Storage lookups (`storage/load.go`): `GetStorageByName`, `GetStorageByUserIDAndName(ctx, chatID, name)` (gates `config.C().HasStorage(chatID, name)` — config/user.go:29), `GetUserStorages`; `storage.FromContext`.

## Supporting

- `ruleutil.ApplyRule(ctx, rules, ruleutil.NewInput(file)) → (matched, matchedStorName, MatchedDirPath)`; `NeedNewForAlbum()`, `Usable()`. Rule types: FileNameRegex, MessageRegex (caption only), IsAlbum.
- `coretfile.NewTGFileTask(id, ctx, file, stor, path, progress)`; Type = TaskTypeTgfiles.
- `batchtfile.NewTaskElement(stor, path, file)` / `NewBatchTGFileTask(id, ctx, []TaskElement, progress, ignoreErrors)`; Task.UserID int64; JournalPayload records TrackChatID/TrackMsgID for recovery.
- `mediautil`: ParseMediaFilter / IsMediaFilterSpec / UserMediaFilter / TfileOptions.
- watch_notify.go: `newWatchNotifyProgress(ctx, botCtx, user)` ProgressTracker.
