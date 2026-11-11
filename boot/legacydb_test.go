package boot

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/krau/SaveAny-Bot/database"
)

// TestGuardV0Database: a v0-shaped file at the ACTIVE database path must
// refuse boot (database.Init would AutoMigrate it and destroy the rollback
// artifact); a v1-shaped or missing file passes.
func TestGuardV0Database(t *testing.T) {
	// Missing file: pass.
	if err := guardV0Database(filepath.Join(t.TempDir(), "absent.db")); err != nil {
		t.Fatalf("missing file: %v", err)
	}

	// v0-shaped: users + a fork table, no schema_migrations/storages.
	v0Path := filepath.Join(t.TempDir(), "v0.db")
	g, err := database.Open(v0Path)
	if err != nil {
		// The default ncruces WASM driver cannot open absolute Windows
		// paths (pre-existing upstream defect) — skip, not fail.
		t.Skipf("sqlite driver cannot open a database on this platform; run with -tags sqlite_glebarez: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE saved_file_dedups (id INTEGER PRIMARY KEY)`,
	} {
		if err := g.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	if sqlDB, err := g.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if err := guardV0Database(v0Path); !errors.Is(err, ErrV0Database) {
		t.Fatalf("v0 file: err = %v, want ErrV0Database", err)
	}

	// v1-shaped: schema_migrations present → pass.
	v1Path := filepath.Join(t.TempDir(), "v1.db")
	g2, err := database.Open(v1Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := g2.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := g2.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if err := guardV0Database(v1Path); err != nil {
		t.Fatalf("v1 file: %v", err)
	}
}
