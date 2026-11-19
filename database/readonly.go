package database

import (
	"net/url"
	"path/filepath"

	"gorm.io/gorm"
)

// OpenReadOnly opens a SQLite database with a mode=ro URI DSN — no WAL
// pragma writes, no journal mutations: the file is never modified, not even
// its header. Used by the boot-time v0-refusal guard and the v0→v1 data
// importer.
func OpenReadOnly(path string) (*gorm.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Same DSN form as dsnWithPragmas: file:C:/path preserves the native
	// Windows drive path (file:///C:/ leaves SQLite seeing a POSIX /C: and
	// file://C:/ a UNC host).
	dsn := sqliteFileDSN(filepath.ToSlash(absolute)) + "?" + url.Values{"mode": {"ro"}}.Encode()
	return gorm.Open(openDialector(dsn), &gorm.Config{})
}

// TableNames lists the user tables of an opened database.
func TableNames(g *gorm.DB) ([]string, error) {
	var names []string
	if err := g.Raw("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&names).Error; err != nil {
		return nil, err
	}
	return names, nil
}

// ColumnNames lists the columns of one table; the name is interpolated from
// the caller's constant, never user input.
func ColumnNames(g *gorm.DB, table string) ([]string, error) {
	var cols []struct {
		Name string
	}
	if err := g.Raw("PRAGMA table_info(" + table + ")").Scan(&cols).Error; err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.Name)
	}
	return names, nil
}

// SmellsLikeV0 reports whether an opened database is a v0 database: a users
// table without v1's schema_migrations/storages tables. Covers both the
// fork v0 schema and the upstream-v0 base (same base tables). Shared by the
// boot guard, the fork-DB discovery and the legacy importer: one detector,
// so the callers can never disagree about what counts as v0.
func SmellsLikeV0(g *gorm.DB) bool {
	tables, err := TableNames(g)
	if err != nil {
		return false
	}
	hasUsers := false
	for _, t := range tables {
		switch t {
		case "users":
			hasUsers = true
		case "schema_migrations", "storages":
			// v1 markers: this file already carries the new schema.
			return false
		}
	}
	return hasUsers
}

// Open opens a writable database handle through the configured driver —
// used by tools and tests that must BUILD a database file (the importer
// itself uses OpenReadOnly).
func Open(path string) (*gorm.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := sqliteFileDSN(filepath.ToSlash(absolute))
	return gorm.Open(openDialector(dsn), &gorm.Config{})
}
