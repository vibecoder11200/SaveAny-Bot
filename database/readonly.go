package database

import (
	"net/url"
	"path/filepath"
	"strings"

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
	// A Windows drive path must be represented as file:///D:/...; without
	// the leading slash net/url emits file://D:/..., which SQLite treats as
	// a UNC path on host "D:".
	slash := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	dsn := (&url.URL{Scheme: "file", Path: slash, RawQuery: url.Values{"mode": {"ro"}}.Encode()}).String()
	return gorm.Open(openReadOnlyDialector(dsn), &gorm.Config{})
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

// Open opens a writable database handle through the configured driver —
// used by tools and tests that must BUILD a database file (the importer
// itself uses OpenReadOnly).
func Open(path string) (*gorm.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	slash := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	dsn := (&url.URL{Scheme: "file", Path: slash}).String()
	return gorm.Open(openDialector(dsn), &gorm.Config{})
}
