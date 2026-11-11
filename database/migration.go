package database

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// CurrentSchemaVersion is the database schema expected by this build.
// Version 2 adds the fork tables (dedup ledger, telegram saved-files ledger)
// and the fork columns on users/watch_chats.
const CurrentSchemaVersion = 2

type schemaMigration struct {
	version int
	up      func(tx *gorm.DB) error
}

var schemaMigrations = []schemaMigration{
	{version: 1, up: baselineSchema},
	{version: 2, up: forkTables},
}

func baselineSchema(tx *gorm.DB) error {
	return tx.AutoMigrate(
		&User{},
		&Dir{},
		&Rule{},
		&WatchChat{},
		&Setting{},
		&StorageRecord{},
		&TaskRecord{},
		&TaskItemRecord{},
		&WebSession{},
		&BotWorker{},
		&TaskItemMetadata{},
	)
}

// forkIndexes holds the statements migration 2 must run beyond AutoMigrate.
// The dedup uniqueness lives in PARTIAL unique indexes because gorm tags
// cannot express the non-empty WHERE clauses: they keep empty-string keys
// (non-TG files have no file id, resends have neither id nor hash) from
// colliding, and OnConflict DoNothing only deduplicates against unique
// indexes — a plain index never deduplicates.
var forkIndexes = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_dedup_fileid ON saved_file_dedups(user_id, storage_id, file_unique_id) WHERE file_unique_id != ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_dedup_hash ON saved_file_dedups(user_id, storage_id, content_hash) WHERE content_hash != ''`,
	`CREATE INDEX IF NOT EXISTS idx_dedup_chat ON saved_file_dedups(chat_id)`,
}

func forkTables(tx *gorm.DB) error {
	if err := tx.AutoMigrate(
		&User{},           // + media_filter
		&WatchChat{},      // + media_type, storage_id, dir_path, last_message_id
		&SavedFileDedup{}, // new fork table
		&TelegramSavedFile{}, // new fork ledger table
	); err != nil {
		return err
	}
	for _, stmt := range forkIndexes {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create fork index: %w", err)
		}
	}
	return nil
}

func migrate(ctx context.Context) error {
	if err := db.WithContext(ctx).Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at DATETIME NOT NULL
)`).Error; err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var applied int
	if err := db.WithContext(ctx).Raw("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&applied).Error; err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if applied > CurrentSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", applied, CurrentSchemaVersion)
	}
	for _, migration := range schemaMigrations {
		if migration.version <= applied {
			continue
		}
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := migration.up(tx); err != nil {
				return err
			}
			return tx.Exec(
				"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
				migration.version, time.Now().UTC(),
			).Error
		}); err != nil {
			return fmt.Errorf("apply schema migration %d: %w", migration.version, err)
		}
	}
	return nil
}
