//go:build sqlite_glebarez

package database

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openDialector(dsn string) gorm.Dialector {
	return sqlite.Open(dsn)
}

func openReadOnlyDialector(dsn string) gorm.Dialector {
	return sqlite.Open(dsn)
}
