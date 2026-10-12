//go:build !sqlite_glebarez

package database

import (
	"strings"

	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

// GetDialect opens the ncruces sqlite dialect. The driver only honors
// pragmas passed per-connection inside the DSN, so WAL mode and a
// busy_timeout must ride along the file path itself — later db.Exec pragma
// calls never take effect.
func GetDialect(dsn string) gorm.Dialector {
	if !strings.Contains(dsn, "?") {
		if !strings.HasPrefix(dsn, "file:") {
			dsn = "file:" + strings.ReplaceAll(dsn, "\\", "/")
		}
		dsn += "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	return gormlite.Open(dsn)
}
