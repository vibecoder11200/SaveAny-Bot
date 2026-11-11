package boot

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
	"gorm.io/gorm"

	"github.com/krau/SaveAny-Bot/database"
)

// guardV0Database refuses to boot when the RESOLVED active database path
// holds a v0 database: database.Init would AutoMigrate it (adding v1
// tables/columns to the v0 production file) before any importer-side guard
// could run, permanently defeating the import and corrupting the only
// rollback artifact. Runs BEFORE database.Init on every boot.
//
// The file is probed read-only; a missing file or unreadable file simply
// means "not a v0 file" and boot continues.
func guardV0Database(dbPath string) error {
	if dbPath == "" {
		return nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil // no file yet: nothing to guard
	}
	g, err := database.OpenReadOnly(dbPath)
	if err != nil {
		// Unreadable (locked WAL etc.) is not proof of a v0 file; let the
		// normal open path surface the real error later.
		log.Warnf("Could not probe database %s read-only (continuing): %v", dbPath, err)
		return nil
	}
	defer func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	tables, err := database.TableNames(g)
	if err != nil {
		log.Warnf("Could not list tables of %s (continuing): %v", dbPath, err)
		return nil
	}
	looksV0 := false
	for _, t := range tables {
		switch t {
		case "schema_migrations", "storages":
			return nil // v1 schema: fine
		case "pending_tasks", "saved_file_dedups", "telegram_saved_files", "dirs", "rules", "watch_chats", "users":
			looksV0 = true
		}
	}
	if !looksV0 {
		return nil
	}
	return ErrV0Database
}

// discoverForkDB resolves the v0 source database for the one-shot import:
// an explicit --fork-db (or SAVEANY_FORK_DB) wins; otherwise fall back to
// auto-detecting <data-dir>/saveany.db when it smells like v0 and is NOT
// the active database path (a v0 file at the active path already made boot
// refuse). The bool reports whether an import should run.
func discoverForkDB(opts Options) (string, bool) {
	if opts.ForkDBPath != "" {
		return opts.ForkDBPath, true
	}
	candidate := filepath.Join(opts.DataDir, "saveany.db")
	if samePath(candidate, opts.DBPath) {
		return "", false
	}
	if _, err := os.Stat(candidate); err != nil {
		return "", false
	}
	g, err := database.OpenReadOnly(candidate)
	if err != nil {
		return "", false
	}
	defer func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	if smellsLikeV0(g) {
		log.Warnf("Found a v0 database at %s; importing it once (pass --fork-db to control this explicitly)", candidate)
		return candidate, true
	}
	return "", false
}

// samePath compares two paths without requiring them to exist.
func samePath(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(aa, bb)
}

// smellsLikeV0 mirrors settings.smellsLikeV0DB at the boot layer (the boot
// package cannot import settings: settings imports database only, while
// boot imports settings — a settings→boot import would cycle).
func smellsLikeV0(g *gorm.DB) bool {
	tables, err := database.TableNames(g)
	if err != nil {
		return false
	}
	hasUsers := false
	for _, t := range tables {
		switch t {
		case "users":
			hasUsers = true
		case "schema_migrations", "storages":
			return false
		}
	}
	return hasUsers
}

// ErrV0Database is returned by boot.Init when the active database path
// holds a v0 database file.
var ErrV0Database = errV0{}

type errV0 struct{}

func (errV0) Error() string {
	return "the database at the active path is a v0 database; v1 would migrate it in place and destroy the rollback artifact. " +
		"Use a FRESH data dir and import instead: saveany --data-dir <new-dir> --config <old-config.toml> --fork-db <copy-of-v0.db>"
}
