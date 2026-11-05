// Package db opens the SAF SQLite database and keeps its schema current.
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite" // also registers the "sqlite" driver
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Applied to every connection. secure_delete overwrites deleted values (the 6-month purge);
// _txlock=immediate takes the write lock at BEGIN, so two concurrent saves wait instead of
// failing halfway with "database is locked".
const connParams = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(FULL)&_pragma=secure_delete(1)&_txlock=immediate"

// Timestamps are stored as UTC text with second precision, so text order is time order
// and "expires_at > ?" works as a plain string comparison.
const timeLayout = "2006-01-02T15:04:05Z"

// FormatTime turns t into the stored form, e.g. "2026-10-07T15:04:05Z".
func FormatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// ParseTime reads a timestamp written by FormatTime.
func ParseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// IsUniqueViolation reports whether err comes from a UNIQUE constraint, such as a login or a
// medicine that already exists.
func IsUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// Open opens dataDir/saf.db, creating it if needed, and applies pending migrations.
// Before migrating a database that already has a schema, it copies it to dataDir/backups.
//
//	store, err := db.Open("./data")
func Open(dataDir string) (*sql.DB, error) {
	conn, err := openFile(filepath.Join(dataDir, "saf.db"))
	if err != nil {
		return nil, err
	}
	if err := migrate(conn, migrations, filepath.Join(dataDir, "backups")); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func openFile(file string) (*sql.DB, error) {
	conn, err := sql.Open("sqlite", "file:"+file+"?"+connParams)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", file, err)
	}
	return conn, nil
}

// migrate applies the files migrations/NNN_name.sql that are newer than the database's
// user_version, in order, each in its own transaction. Numbers must run 001, 002, ... without gaps.
func migrate(conn *sql.DB, files fs.FS, backupDir string) error {
	names, err := fs.Glob(files, "migrations/*.sql") // sorted by name
	if err != nil {
		return err
	}
	for i, name := range names {
		if want := fmt.Sprintf("%03d_", i+1); !strings.HasPrefix(path.Base(name), want) {
			return fmt.Errorf("migration %s: name must start with %s", name, want)
		}
	}

	var version int
	if err := conn.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(names) {
		return fmt.Errorf("database schema version %d is newer than this program (%d migrations); refusing to start",
			version, len(names))
	}
	if version == len(names) {
		return nil
	}
	if version > 0 {
		backup := filepath.Join(backupDir, fmt.Sprintf("pre-migration-%03d.db", version+1))
		if err := snapshot(conn, backup); err != nil {
			return err
		}
	}
	for i := version; i < len(names); i++ {
		if err := apply(conn, files, names[i], i+1); err != nil {
			return err
		}
	}
	return nil
}

func apply(conn *sql.DB, files fs.FS, name string, version int) error {
	script, err := fs.ReadFile(files, name)
	if err != nil {
		return err
	}
	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("migration %s: %w", name, err)
	}
	defer tx.Rollback() // no-op once committed

	if _, err := tx.Exec(string(script)); err != nil {
		return fmt.Errorf("migration %s: %w", name, err)
	}
	// user_version lives in the database file, so it commits or rolls back with the script.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("migration %s: set version: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", name, err)
	}
	return nil
}

// snapshot writes a consistent copy of the database to file. An existing file is kept: it comes
// from an earlier, failed attempt at the same migration and was taken before anything changed.
func snapshot(conn *sql.DB, file string) error {
	if _, err := os.Stat(file); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	if _, err := conn.Exec("VACUUM INTO ?", file); err != nil {
		return fmt.Errorf("snapshot to %s: %w", file, err)
	}
	return nil
}
