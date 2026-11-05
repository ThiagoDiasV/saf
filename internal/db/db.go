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

// backupLayout names the hourly copies; the date and time are those of the PC.
const backupLayout = "saf-2006-01-02-1504.db"

// backupRetention is how long hourly copies are kept: backups expire too (docs/PLAN.md §1).
const backupRetention = 30 * 24 * time.Hour

// Backup copies the database to dataDir/backups/saf-<date>-<time>.db when it changed since the
// newest copy there, and deletes copies older than 30 days. A sync app (Google Drive) carries the
// folder off the PC. It returns the copy written, or "" when nothing changed.
//
//	written, err := db.Backup(conn, "dados", time.Now())
func Backup(conn *sql.DB, dataDir string, now time.Time) (string, error) {
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	changed, err := changedSinceBackup(dataDir, dir)
	if err != nil {
		return "", err
	}
	written := ""
	if changed {
		// Written next to the database first, outside backups/, so the sync app never uploads
		// half a copy.
		tmp := filepath.Join(dataDir, ".backup.db")
		if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if _, err := conn.Exec("VACUUM INTO ?", tmp); err != nil {
			return "", fmt.Errorf("back up to %s: %w", tmp, err)
		}
		written = filepath.Join(dir, now.Format(backupLayout))
		if err := os.Rename(tmp, written); err != nil {
			return "", fmt.Errorf("move backup to %s: %w", written, err)
		}
	}
	return written, pruneBackups(dir, now)
}

// changedSinceBackup compares the database files' last write with the newest hourly copy. Every
// save touches saf.db-wal, and a checkpoint touches saf.db.
func changedSinceBackup(dataDir, dir string) (bool, error) {
	var lastWrite time.Time
	for _, name := range []string{"saf.db", "saf.db-wal"} {
		info, err := os.Stat(filepath.Join(dataDir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.ModTime().After(lastWrite) {
			lastWrite = info.ModTime()
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if _, err := time.Parse(backupLayout, e.Name()); err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return false, err
		}
		if !info.ModTime().Before(lastWrite) {
			return false, nil // a copy at least as new as the last save
		}
	}
	return true, nil
}

// pruneBackups deletes hourly copies older than backupRetention. Pre-migration copies stay.
func pruneBackups(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		taken, err := time.ParseInLocation(backupLayout, e.Name(), now.Location())
		if err != nil || now.Sub(taken) <= backupRetention {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete old backup %s: %w", e.Name(), err)
		}
	}
	return nil
}
