package store

import (
	"path/filepath"
	"testing"
)

func TestOpenMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "slskdpp.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO downloads (username, filename, size, dest, state, created, updated) VALUES ('u', 'f', 1, 'd', 'queued', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Reopening keeps data and does not re-run migrations.
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n, version int
	db.QueryRow("SELECT count(*) FROM downloads").Scan(&n)
	db.QueryRow("PRAGMA user_version").Scan(&version)
	if n != 1 || version != 3 {
		t.Errorf("rows %d version %d", n, version)
	}
	var mode string
	db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal mode %q", mode)
	}
}
