package settings

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/krau/SaveAny-Bot/common/mediafilter"
	"github.com/krau/SaveAny-Bot/common/utils/dirutil"
	"github.com/krau/SaveAny-Bot/database"
)

const forkDBImportedKey = "meta.fork_db_imported"

// maxImportWatchRegexLen mirrors maxWatchRegexLen in the handlers package
// (not imported here to avoid a settings→handlers dependency direction).
const maxImportWatchRegexLen = 512

func ForkDBImported(ctx context.Context) bool {
	_, ok, err := database.GetSetting(ctx, forkDBImportedKey)
	if err != nil {
		log.Errorf("Failed to read fork DB import marker: %v", err)
		return false
	}
	return ok
}

// ImportLegacyDB imports production data from a v0 database exactly once:
// users, dirs, rules, watch chats (with catch-up cursors), the dedup
// ledger and the telegram saved ledger. The source file is opened
// READ-ONLY and never mutated; the import lands in one transaction of the
// active v1 database behind the meta.fork_db_imported marker, so a failure
// leaves the v1 DB unchanged and a re-run is a no-op.
func ImportLegacyDB(ctx context.Context, srcPath string) error {
	if srcPath == "" {
		return nil
	}
	if database.DBPath() != "" {
		active, err := filepath.Abs(database.DBPath())
		if err == nil {
			src, aerr := filepath.Abs(srcPath)
			if aerr == nil && strings.EqualFold(active, src) {
				return fmt.Errorf("refusing to import %s into itself: it is the active v1 database", srcPath)
			}
		}
	}
	src, err := database.OpenReadOnly(srcPath)
	if err != nil {
		return fmt.Errorf("open v0 database read-only: %w", err)
	}
	sqlDB, err := src.DB()
	if err == nil {
		defer sqlDB.Close()
	}
	if !database.SmellsLikeV0(src) {
		return fmt.Errorf("refusing to import %s: it does not look like a v0 database (has v1 tables, or no users table)", srcPath)
	}

	// Column probing: fork releases before the watch extension lack
	// media_type/storage_name/dir_path/last_message_id on watch_chats and
	// media_filter on users; build each query from what actually exists.
	userCols, err := database.ColumnNames(src, "users")
	if err != nil {
		return fmt.Errorf("probe users: %w", err)
	}
	has := func(cols []string, name string) bool {
		for _, c := range cols {
			if strings.EqualFold(c, name) {
				return true
			}
		}
		return false
	}
	userHasMediaFilter := has(userCols, "media_filter")
	watchCols, err := database.ColumnNames(src, "watch_chats")
	if err != nil {
		return fmt.Errorf("probe watch_chats: %w", err)
	}
	watchHasMediaType := has(watchCols, "media_type")
	watchHasStorageName := has(watchCols, "storage_name")
	watchHasDirPath := has(watchCols, "dir_path")
	watchHasCursor := has(watchCols, "last_message_id")

	// --- read v0 rows ---------------------------------------------------
	type v0User struct {
		ID               uint
		ChatID           int64
		Silent           bool
		DefaultStorage   string
		DefaultDir       uint
		ApplyRule        bool
		WatchNotify      bool
		FilenameStrategy string
		FilenameTemplate string
		ConflictStrategy string
		MediaFilter      string
		Deleted          bool
	}
	selectCols := "id, chat_id, silent, default_storage, default_dir, apply_rule, watch_notify, filename_strategy, filename_template, conflict_strategy"
	if userHasMediaFilter {
		selectCols += ", media_filter"
	}
	if has(userCols, "deleted_at") {
		selectCols += ", deleted_at IS NOT NULL AS deleted"
	}
	var v0Users []v0User
	if err := src.Table("users").Select(selectCols).Scan(&v0Users).Error; err != nil {
		return fmt.Errorf("read v0 users: %w", err)
	}

	// --- mappings ---------------------------------------------------------
	// Storage name → v1 ID, from the rows the TOML import created.
	type storageRow struct {
		ID   uint
		Name string
	}
	var storages []storageRow
	if err := database.DB().WithContext(ctx).Table("storages").Select("id, name").Scan(&storages).Error; err != nil {
		return fmt.Errorf("list v1 storages: %w", err)
	}
	storageIDs := make(map[string]uint, len(storages))
	namesByID := make(map[uint]string, len(storages))
	for _, s := range storages {
		key := strings.ToLower(s.Name)
		if prev, ok := storageIDs[key]; ok && prev != s.ID {
			log.Warnf("Storage names %q and %q differ only by case; v0 references resolve to the LAST one", namesByID[prev], s.Name)
		}
		storageIDs[key] = s.ID
		namesByID[s.ID] = s.Name
	}

	// Referenced-but-unresolvable names feed the hollow-import refusal and
	// the summary.
	referencedNames := make(map[string]bool)
	countName := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" {
			referencedNames[strings.ToLower(name)] = true
		}
	}

	sum := struct {
		usersCreated, usersSkippedDeleted int
		dirsImported, dirsSkipped         int
		rulesImported, rulesSkipped       int
		watchesImported, watchesSkipped   int
		watchesInvalid                    int
		dedupsImported, dedupsSkipped     int
		tgSavedImported, tgSavedSkipped   int
		pendingTasks                      int
		storageSkips                      int
	}{}

	// --- one transaction over the v1 database ---------------------------
	err = database.Transaction(ctx, func(tx *gorm.DB) error {
		// Users: chat_id is the join key. Existing v1 rows keep their
		// enabled flag untouched (the TOML import owns it); rows created
		// here never auto-enable — Enabled is gated on TOML whitelist
		// membership, and whitelist members were already created (enabled)
		// by the TOML import, so a v0-only user stays disabled.
		type userMapEntry struct {
			id uint
		}
		userByV0ID := make(map[uint]userMapEntry, len(v0Users))
		userByChatID := make(map[int64]userMapEntry, len(v0Users))
		for _, u := range v0Users {
			if u.Deleted {
				sum.usersSkippedDeleted++
				continue
			}
			var existing database.User
			findErr := tx.Where("chat_id = ?", u.ChatID).First(&existing).Error
			if findErr != nil && !database.IsNotFound(findErr) {
				return findErr
			}
			if database.IsNotFound(findErr) {
				created := database.User{
					ChatID:           u.ChatID,
					Silent:           u.Silent,
					ApplyRule:        u.ApplyRule,
					WatchNotify:      u.WatchNotify,
					FilenameStrategy: u.FilenameStrategy,
					FilenameTemplate: u.FilenameTemplate,
					ConflictStrategy: u.ConflictStrategy,
					MediaFilter:      u.MediaFilter,
					Enabled:          false, // never auto-enable a v0-only user
				}
				if err := tx.Create(&created).Error; err != nil {
					return fmt.Errorf("create user %d: %w", u.ChatID, err)
				}
				// The model default is enabled=true; a created v0-only user
				// must NOT inherit it (Enabled is gated on TOML whitelist
				// membership and whitelist members already exist).
				if err := tx.Model(&created).Update("enabled", false).Error; err != nil {
					return err
				}
				sum.usersCreated++
				userByV0ID[u.ID] = userMapEntry{created.ID}
				userByChatID[u.ChatID] = userMapEntry{created.ID}
				continue
			}
			// Map the flags the v0 user configured onto the existing row;
			// an empty/zero mapped value never clobbers a v1 choice.
			updates := map[string]any{}
			if u.Silent {
				updates["silent"] = true
			}
			if u.ApplyRule {
				updates["apply_rule"] = true
			}
			if u.WatchNotify {
				updates["watch_notify"] = true
			}
			if u.FilenameStrategy != "" {
				updates["filename_strategy"] = u.FilenameStrategy
			}
			if u.FilenameTemplate != "" {
				updates["filename_template"] = u.FilenameTemplate
			}
			if u.ConflictStrategy != "" {
				updates["conflict_strategy"] = u.ConflictStrategy
			}
			if userHasMediaFilter && u.MediaFilter != "" {
				updates["media_filter"] = u.MediaFilter
			}
			if len(updates) > 0 {
				if err := tx.Model(&existing).Updates(updates).Error; err != nil {
					return err
				}
			}
			userByV0ID[u.ID] = userMapEntry{existing.ID}
			userByChatID[u.ChatID] = userMapEntry{existing.ID}
		}
		user := func(v0ID uint) (uint, bool) {
			e, ok := userByV0ID[v0ID]
			return e.id, ok
		}

		// Dirs: (user_id, storage_id, path) unique triple. The v0 dir ID →
		// v1 dir ID map feeds the users.default_dir remap below.
		type v0Dir struct {
			ID          uint
			UserID      uint
			StorageName string
			Path        string
		}
		var v0Dirs []v0Dir
		if err := src.Table("dirs").Select("id, user_id, storage_name, path").Scan(&v0Dirs).Error; err != nil {
			return fmt.Errorf("read v0 dirs: %w", err)
		}
		dirIDByV0 := make(map[uint]uint, len(v0Dirs))
		for _, d := range v0Dirs {
			uid, ok := user(d.UserID)
			if !ok {
				sum.dirsSkipped++
				continue
			}
			countName(d.StorageName)
			sid := storageIDs[strings.ToLower(strings.TrimSpace(d.StorageName))]
			if sid == 0 {
				sum.dirsSkipped++
				sum.storageSkips++
				continue
			}
			var v1 database.Dir
			findErr := tx.Where("user_id = ? AND storage_id = ? AND path = ?", uid, sid, d.Path).First(&v1).Error
			if findErr != nil && !database.IsNotFound(findErr) {
				return findErr
			}
			if database.IsNotFound(findErr) {
				if err := tx.Create(&database.Dir{UserID: uid, StorageID: sid, Path: d.Path}).Error; err != nil {
					return err
				}
				if err := tx.Where("user_id = ? AND storage_id = ? AND path = ?", uid, sid, d.Path).First(&v1).Error; err != nil {
					return err
				}
				sum.dirsImported++
			}
			dirIDByV0[d.ID] = v1.ID
		}

		// Users.default_dir remap + default_storage name→ID.
		for _, u := range v0Users {
			if u.Deleted {
				continue
			}
			e, ok := userByV0ID[u.ID]
			if !ok {
				continue
			}
			updates := map[string]any{}
			if u.DefaultStorage != "" {
				countName(u.DefaultStorage)
				if sid := storageIDs[strings.ToLower(strings.TrimSpace(u.DefaultStorage))]; sid != 0 {
					updates["default_storage_id"] = sid
				} else {
					sum.storageSkips++
				}
			}
			if u.DefaultDir != 0 {
				if v1Dir, ok := dirIDByV0[u.DefaultDir]; ok {
					updates["default_dir_id"] = v1Dir
				}
			}
			if len(updates) == 0 {
				continue
			}
			if err := tx.Model(&database.User{}).Where("id = ?", e.id).Updates(updates).Error; err != nil {
				return err
			}
		}

		// Rules: v0 Data → v1 Content; an unresolvable storage imports as a
		// dir-only rule (StorageID 0 is valid v1 semantics: keep the user's
		// storage choice).
		type v0Rule struct {
			ID          uint
			UserID      uint
			Type        string
			Data        string
			StorageName string
			DirPath     string
		}
		var v0Rules []v0Rule
		if err := src.Table("rules").Select("id, user_id, type, data, storage_name, dir_path").Scan(&v0Rules).Error; err != nil {
			return fmt.Errorf("read v0 rules: %w", err)
		}
		for _, r := range v0Rules {
			uid, ok := user(r.UserID)
			if !ok {
				sum.rulesSkipped++
				continue
			}
			countName(r.StorageName)
			sid := storageIDs[strings.ToLower(strings.TrimSpace(r.StorageName))]
			if r.StorageName != "" && sid == 0 {
				sum.storageSkips++
			}
			var count int64
			if err := tx.Model(&database.Rule{}).
				Where("user_id = ? AND type = ? AND content = ? AND storage_id = ? AND dir_path = ?",
					uid, r.Type, r.Data, sid, r.DirPath).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			if err := tx.Create(&database.Rule{
				UserID: uid, Type: r.Type, Content: r.Data, StorageID: sid, DirPath: r.DirPath,
			}).Error; err != nil {
				return err
			}
			sum.rulesImported++
		}

		// Watch chats: validation block + collision policy (higher cursor
		// wins, matching the boot sweep). Invalid stored values are cleared
		// (never fatal): the watch survives, the invalid field does not.
		type v0Watch struct {
			ID            uint
			UserID        uint
			ChatID        int64
			Filter        string
			MediaType     string
			StorageName   string
			DirPath       string
			LastMessageID *int64
			Deleted       bool
		}
		watchSelect := "id, user_id, chat_id, filter"
		if watchHasMediaType {
			watchSelect += ", media_type"
		}
		if watchHasStorageName {
			watchSelect += ", storage_name"
		}
		if watchHasDirPath {
			watchSelect += ", dir_path"
		}
		if watchHasCursor {
			watchSelect += ", last_message_id"
		}
		if has(watchCols, "deleted_at") {
			watchSelect += ", deleted_at IS NOT NULL AS deleted"
		}
		var v0Watches []v0Watch
		if err := src.Table("watch_chats").Select(watchSelect).Scan(&v0Watches).Error; err != nil {
			return fmt.Errorf("read v0 watch_chats: %w", err)
		}
		best := make(map[string]v0Watch)
		order := make([]string, 0, len(v0Watches))
		for _, w := range v0Watches {
			if w.Deleted {
				continue
			}
			key := fmt.Sprintf("%d:%d", w.UserID, w.ChatID)
			prev, seen := best[key]
			cursor := int64(0)
			if w.LastMessageID != nil {
				cursor = *w.LastMessageID
			}
			prevCursor := int64(0)
			if seen && prev.LastMessageID != nil {
				prevCursor = *prev.LastMessageID
			}
			if !seen || cursor > prevCursor {
				if !seen {
					order = append(order, key)
				}
				best[key] = w
			}
		}
		for _, key := range order {
			w := best[key]
			uid, ok := user(w.UserID)
			if !ok {
				sum.watchesSkipped++
				continue
			}
			countName(w.StorageName)
			sid := uint(0)
			if watchHasStorageName && w.StorageName != "" {
				sid = storageIDs[strings.ToLower(strings.TrimSpace(w.StorageName))]
				if sid == 0 {
					// 0 = inherit (rule layer decides) — keep the watch.
					sum.storageSkips++
				}
			}
			filter := w.Filter
			if filter != "" {
				if len(filter) > maxImportWatchRegexLen {
					log.Warnf("Dropping over-long watch filter of chat %d (import)", w.ChatID)
					filter = ""
					sum.watchesInvalid++
				} else if _, err := regexp.Compile(filter); err != nil {
					log.Warnf("Dropping uncompilable watch filter of chat %d (import)", w.ChatID)
					filter = ""
					sum.watchesInvalid++
				}
			}
			mediaType := ""
			if watchHasMediaType {
				mediaType = w.MediaType
			}
			if mediaType != "" {
				scope, err := mediafilter.ParseMediaFilter(mediaType)
				if err != nil || scope == nil {
					log.Warnf("Clearing invalid mediatype filter of chat %d (import)", w.ChatID)
					mediaType = ""
					sum.watchesInvalid++
				}
			}
			dirPath := ""
			if watchHasDirPath {
				dirPath = w.DirPath
			}
			if dirPath != "" {
				if sanitized, err := dirutil.SanitizeRelDirPath(dirPath); err == nil {
					dirPath = sanitized
				} else {
					log.Warnf("Clearing invalid dir path of chat %d (import)", w.ChatID)
					dirPath = ""
					sum.watchesInvalid++
				}
			}
			row := database.WatchChat{
				UserID:        uid,
				ChatID:        w.ChatID,
				MessageFilter: filter,
				MediaType:     mediaType,
				StorageID:     sid,
				DirPath:       dirPath,
				LastMessageID: w.LastMessageID,
			}
			var count int64
			if err := tx.Model(&database.WatchChat{}).
				Where("user_id = ? AND chat_id = ? AND deleted_at IS NULL", uid, w.ChatID).
				Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				// Keep the v1 row (it may carry a newer cursor); the import
				// never lowers a cursor.
				continue
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			sum.watchesImported++
		}

		// Dedup ledger: v0 user_id is the CHAT ID (not the users row ID).
		type v0Dedup struct {
			UserID       int64
			StorageName  string
			FileUniqueID string
			ContentHash  string
			Size         int64
			StoragePath  string
			ChatID       int64
			MessageID    int
			CreatedAt    time.Time
		}
		// A section that cannot be probed (or is missing entirely) must
		// abort the whole import: a silent skip would commit an incomplete
		// migration behind the permanent marker. All three tables are core
		// to every v0 fork release — their absence means the file is not a
		// complete v0 database.
		dedupCols, derr := database.ColumnNames(src, "saved_file_dedups")
		if derr != nil {
			return fmt.Errorf("probe saved_file_dedups: %w", derr)
		}
		if len(dedupCols) == 0 {
			return fmt.Errorf("refusing to import %s: saved_file_dedups table is missing", srcPath)
		}
		{
			dedupSelect := "user_id, storage_name, file_unique_id, content_hash, size, storage_path"
			if has(dedupCols, "chat_id") {
				dedupSelect += ", chat_id"
			}
			if has(dedupCols, "message_id") {
				dedupSelect += ", message_id"
			}
			if has(dedupCols, "created_at") {
				dedupSelect += ", created_at"
			}
			var v0Dedups []v0Dedup
			if err := src.Table("saved_file_dedups").Select(dedupSelect).Scan(&v0Dedups).Error; err != nil {
				return fmt.Errorf("read v0 saved_file_dedups: %w", err)
			}
			for _, d := range v0Dedups {
				e, ok := userByChatID[d.UserID]
				if !ok {
					// A ledger row whose owner is gone: skip, never invent
					// users from ledger rows.
					sum.dedupsSkipped++
					continue
				}
				countName(d.StorageName)
				sid := storageIDs[strings.ToLower(strings.TrimSpace(d.StorageName))]
				if sid == 0 {
					sum.dedupsSkipped++
					sum.storageSkips++
					continue
				}
				// OnConflict DoNothing against the partial unique indexes,
				// same contract as the runtime recorder: a marker-less
				// re-import duplicates nothing.
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&database.SavedFileDedup{
					UserID:       e.id,
					StorageID:    sid,
					FileUniqueID: d.FileUniqueID,
					ContentHash:  d.ContentHash,
					Size:         d.Size,
					StoragePath:  d.StoragePath,
					ChatID:       d.ChatID,
					MessageID:    int64(d.MessageID),
					CreatedAt:    d.CreatedAt,
				}).Error; err != nil {
					return err
				}
				sum.dedupsImported++
			}
		}

		// Telegram saved ledger: name→ID, (storage_id, storage_path) unique.
		type v0TgSaved struct {
			StorageName string
			StoragePath string
			CreatedAt   time.Time
		}
		tgCols, terr := database.ColumnNames(src, "telegram_saved_files")
		if terr != nil {
			return fmt.Errorf("probe telegram_saved_files: %w", terr)
		}
		if len(tgCols) == 0 {
			return fmt.Errorf("refusing to import %s: telegram_saved_files table is missing", srcPath)
		}
		{
			tgSelect := "storage_name, storage_path"
			if has(tgCols, "created_at") {
				tgSelect += ", created_at"
			}
			var v0Tg []v0TgSaved
			if err := src.Table("telegram_saved_files").Select(tgSelect).Scan(&v0Tg).Error; err != nil {
				return fmt.Errorf("read v0 telegram_saved_files: %w", err)
			}
			for _, t := range v0Tg {
				countName(t.StorageName)
				sid := storageIDs[strings.ToLower(strings.TrimSpace(t.StorageName))]
				if sid == 0 {
					sum.tgSavedSkipped++
					sum.storageSkips++
					continue
				}
				var count int64
				if err := tx.Model(&database.TelegramSavedFile{}).
					Where("storage_id = ? AND storage_path = ?", sid, t.StoragePath).
					Count(&count).Error; err != nil {
					return err
				}
				if count > 0 {
					continue
				}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&database.TelegramSavedFile{
					StorageID:   sid,
					StoragePath: t.StoragePath,
					CreatedAt:   t.CreatedAt,
				}).Error; err != nil {
					return err
				}
				sum.tgSavedImported++
			}
		}

		// In-flight v0 journal rows are NOT migrated (out of scope); count
		// them so the boot log can surface the warning.
		ptCols, perr := database.ColumnNames(src, "pending_tasks")
		if perr != nil {
			return fmt.Errorf("probe pending_tasks: %w", perr)
		}
		if len(ptCols) == 0 {
			return fmt.Errorf("refusing to import %s: pending_tasks table is missing", srcPath)
		}
		{
			var pending int64
			if serr := src.Table("pending_tasks").Count(&pending).Error; serr == nil {
				sum.pendingTasks = int(pending)
			} else {
				log.Warnf("Could not count v0 pending_tasks (advisory): %v", serr)
			}
		}

		// Hollow-import refusal: every storage reference failed to resolve,
		// which almost certainly means --config was not passed (the TOML
		// import populates the storages rows this mapping needs). Refuse and
		// DO NOT write the permanent marker.
		if len(referencedNames) > 0 {
			resolved := 0
			for name := range referencedNames {
				if storageIDs[name] != 0 {
					resolved++
				}
			}
			if resolved == 0 {
				return fmt.Errorf("none of the %d storages referenced by the v0 database resolve; pass --config with the old config.toml so storages are imported first, then retry", len(referencedNames))
			}
		}

		return tx.Save(&database.Setting{
			Key: forkDBImportedKey, Value: time.Now().UTC().Format(time.RFC3339),
		}).Error
	})
	if err != nil {
		return err
	}

	log.Info("Fork database imported",
		"users_created", sum.usersCreated,
		"users_skipped_deleted", sum.usersSkippedDeleted,
		"dirs", sum.dirsImported, "dirs_skipped", sum.dirsSkipped,
		"rules", sum.rulesImported, "rules_skipped", sum.rulesSkipped,
		"watches", sum.watchesImported, "watches_skipped", sum.watchesSkipped,
		"watch_fields_cleared", sum.watchesInvalid,
		"dedup_rows", sum.dedupsImported, "dedup_skipped", sum.dedupsSkipped,
		"tg_saved_rows", sum.tgSavedImported, "tg_saved_skipped", sum.tgSavedSkipped,
		"storage_name_skips", sum.storageSkips)
	if sum.pendingTasks > 0 {
		log.Warnf("Fork database import: %d in-flight task(s) were NOT migrated; drain the task queue before the next cutover", sum.pendingTasks)
	}
	return nil
}
