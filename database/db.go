package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/log"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

var db *gorm.DB

// dbPath records the database file path handed to Init; DBPath reports it
// for ops readouts (/status). Empty until Init ran.
var dbPath string

// DBPath returns the on-disk path of the SQLite database.
func DBPath() string { return dbPath }

// DB exposes the handle for tests and one-off administrative tooling; call
// Init first. Nil until then.
func DB() *gorm.DB {
	return db
}

func Init(ctx context.Context, path string) {
	dbPath = path
	logger := log.FromContext(ctx)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.Fatalf("Failed to create data directory: %v", err)
	}
	var err error
	db, err = gorm.Open(GetDialect(path), &gorm.Config{
		Logger: glogger.New(logger, glogger.Config{
			Colorful:                  true,
			SlowThreshold:             time.Second * 5,
			LogLevel:                  glogger.Error,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
		}),
		PrepareStmt: true,
	})
	if err != nil {
		logger.Fatalf("Failed to open database: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		// SQLite allows exactly one writer. With GORM's default unlimited
		// pool, concurrent read transactions upgrading to writes (cursor
		// touches, dedup inserts during a watch catch-up burst) surface
		// SQLITE_BUSY mid-transaction, which busy_timeout does not retry.
		// One pooled connection serializes access deterministically.
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		logger.Warnf("Failed to restrict database permissions: %v", err)
	}
	logger.Debug("Database connected")
	if err := migrate(ctx); err != nil {
		logger.Fatalf("Database migration failed: %v", err)
	}
	if err := EnsureWatchChatUniqueIndex(ctx); err != nil {
		logger.Fatalf("Failed to ensure the watch unique index: %v", err)
	}
	logger.Info("Database initialized")
}

func Close() error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get sql db: %w", err)
	}
	return sqlDB.Close()
}

func Transaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(fn)
}
