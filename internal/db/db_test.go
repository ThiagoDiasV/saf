package db

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCreatesSchema(t *testing.T) {
	conn := openTestDB(t, t.TempDir())

	names, err := fs.Glob(migrations, "migrations/*.sql")
	require.NoError(t, err)
	assert.Equal(t, len(names), userVersion(t, conn))

	var tables []string
	rows, err := conn.Query("SELECT name FROM sqlite_schema WHERE type = 'table'")
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	assert.Subset(t, tables, []string{
		"users", "sessions", "medicines", "batches", "movements", "movement_lines", "photos", "changes",
	})
}

func TestOpenSetsConnectionPragmas(t *testing.T) {
	conn := openTestDB(t, t.TempDir())

	for pragma, want := range map[string]string{
		"foreign_keys":  "1",
		"journal_mode":  "wal",
		"synchronous":   "2", // FULL
		"secure_delete": "1",
		"busy_timeout":  "5000",
	} {
		var got string
		require.NoError(t, conn.QueryRow("PRAGMA "+pragma).Scan(&got))
		assert.Equal(t, want, got, pragma)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	conn := openTestDB(t, t.TempDir())

	_, err := conn.Exec("INSERT INTO movement_lines (movement_id, batch_id, quantity) VALUES (999, 999, 1)")

	assert.ErrorContains(t, err, "FOREIGN KEY constraint failed")
}

func TestStrictTablesRejectWrongTypes(t *testing.T) {
	conn := openTestDB(t, t.TempDir())

	_, err := conn.Exec(`INSERT INTO users (name, login, password_hash, role, active, created_at)
		VALUES ('Paulo', 'paulo', 'x', 'admin', 'sim', '2026-10-07T12:00:00Z')`)

	assert.ErrorContains(t, err, "cannot store TEXT value in INTEGER column")
}

func TestChangeNeedsExactlyOneSubject(t *testing.T) {
	conn := openTestDB(t, t.TempDir())
	_, err := conn.Exec(`
		INSERT INTO users (id, name, login, password_hash, role, created_at)
			VALUES (1, 'Paulo Mendes', 'paulo.mendes', 'x', 'admin', '2026-10-07T12:00:00Z');
		INSERT INTO medicines (id, active_ingredient, strength, dosage_form, unit, match_key, created_at)
			VALUES (1, 'Dipirona', '500 mg', 'comprimido', 'comprimido', 'dipirona500mgcomprimido', '2026-10-07T12:00:00Z');
		INSERT INTO batches (id, medicine_id, lot, manufacturer, expiry_month, match_key)
			VALUES (1, 1, 'A123', 'EMS', '2027-03', 'a123ems');`)
	require.NoError(t, err)
	insert := func(medicineID, batchID any) error {
		_, err := conn.Exec(`INSERT INTO changes (medicine_id, batch_id, field, old_value, new_value, user_id, changed_at)
			VALUES (?, ?, 'Nome', 'a', 'b', 1, '2026-10-07T12:00:00Z')`, medicineID, batchID)
		return err
	}

	assert.ErrorContains(t, insert(nil, nil), "CHECK constraint failed")
	assert.ErrorContains(t, insert(1, 1), "CHECK constraint failed")
	assert.NoError(t, insert(1, nil))
}

func TestMedicineWithChangesCannotBeDeletedAlone(t *testing.T) {
	conn := openTestDB(t, t.TempDir())
	_, err := conn.Exec(`
		INSERT INTO users (id, name, login, password_hash, role, created_at)
			VALUES (1, 'Paulo Mendes', 'paulo.mendes', 'x', 'admin', '2026-10-07T12:00:00Z');
		INSERT INTO medicines (id, active_ingredient, strength, dosage_form, unit, match_key, created_at)
			VALUES (1, 'Dipirona', '500 mg', 'comprimido', 'comprimido', 'dipirona500mgcomprimido', '2026-10-07T12:00:00Z');
		INSERT INTO changes (medicine_id, field, old_value, new_value, user_id, changed_at)
			VALUES (1, 'Concentração', '50 mg', '500 mg', 1, '2026-10-07T12:00:00Z');`)
	require.NoError(t, err)

	_, err = conn.Exec("DELETE FROM medicines WHERE id = 1")

	assert.ErrorContains(t, err, "FOREIGN KEY constraint failed")
}

func TestIsUniqueViolation(t *testing.T) {
	conn := openTestDB(t, t.TempDir())
	insertUser := `INSERT INTO users (name, login, password_hash, role, created_at)
		VALUES ('Paulo Mendes', 'paulo.mendes', 'x', 'admin', '2026-10-07T12:00:00Z')`
	_, err := conn.Exec(insertUser)
	require.NoError(t, err)

	_, err = conn.Exec(insertUser)
	assert.True(t, IsUniqueViolation(err))

	_, err = conn.Exec("INSERT INTO movement_lines (movement_id, batch_id, quantity) VALUES (999, 999, 1)")
	assert.False(t, IsUniqueViolation(err), "a foreign key error is not a unique violation")
}

func TestReopenKeepsDataAndTakesNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	conn, err := Open(dir)
	require.NoError(t, err)
	_, err = conn.Exec(`INSERT INTO users (name, login, password_hash, role, created_at)
		VALUES ('Paulo Mendes', 'paulo.mendes', 'x', 'admin', '2026-10-07T12:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	conn = openTestDB(t, dir)

	var name string
	require.NoError(t, conn.QueryRow("SELECT name FROM users WHERE login = 'paulo.mendes'").Scan(&name))
	assert.Equal(t, "Paulo Mendes", name)
	assert.NoDirExists(t, filepath.Join(dir, "backups"))
}

func TestMigrateSnapshotsBeforeUpgrading(t *testing.T) {
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")
	conn := openRawDB(t, filepath.Join(dir, "saf.db"))
	v1 := fstest.MapFS{"migrations/001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")}}
	v2 := fstest.MapFS{
		"migrations/001_a.sql": v1["migrations/001_a.sql"],
		"migrations/002_b.sql": {Data: []byte("CREATE TABLE b (y INTEGER); INSERT INTO b VALUES (7);")},
	}
	require.NoError(t, migrate(conn, v1, backups))
	_, err := conn.Exec("INSERT INTO a VALUES (1)")
	require.NoError(t, err)

	require.NoError(t, migrate(conn, v2, backups))

	assert.Equal(t, 2, userVersion(t, conn))
	var y int
	require.NoError(t, conn.QueryRow("SELECT y FROM b").Scan(&y))
	assert.Equal(t, 7, y)

	before := openRawDB(t, filepath.Join(backups, "pre-migration-002.db"))
	assert.Equal(t, 1, userVersion(t, before))
	var x int
	require.NoError(t, before.QueryRow("SELECT x FROM a").Scan(&x))
	assert.Equal(t, 1, x)
	_, err = before.Exec("SELECT y FROM b")
	assert.ErrorContains(t, err, "no such table: b")
}

func TestBackupCopiesTheDatabaseOnlyWhenItChanged(t *testing.T) {
	dir := t.TempDir()
	conn := openTestDB(t, dir)
	addUser := func(login string) {
		_, err := conn.Exec(`INSERT INTO users (name, login, password_hash, role, created_at)
			VALUES ('Pessoa', ?, 'x', 'admin', '2026-11-05T12:00:00Z')`, login)
		require.NoError(t, err)
	}
	addUser("paulo.mendes")
	at := time.Date(2026, 11, 5, 14, 0, 0, 0, time.Local)

	written, err := Backup(conn, dir, at)
	require.NoError(t, err)
	first := filepath.Join(dir, "backups", "saf-2026-11-05-1400.db")
	assert.Equal(t, first, written)
	var users int
	require.NoError(t, openRawDB(t, written).QueryRow("SELECT count(*) FROM users").Scan(&users))
	assert.Equal(t, 1, users, "the copy holds the data")
	assert.NoFileExists(t, filepath.Join(dir, ".backup.db"))

	written, err = Backup(conn, dir, at.Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, written, "nothing changed since the last copy")

	hourAgo := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(first, hourAgo, hourAgo))
	addUser("thiago.dias")
	written, err = Backup(conn, dir, at.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "backups", "saf-2026-11-05-1600.db"), written, "a save since the last copy")
}

func TestBackupDeletesCopiesOlderThan30Days(t *testing.T) {
	dir := t.TempDir()
	conn := openTestDB(t, dir)
	backups := filepath.Join(dir, "backups")
	require.NoError(t, os.MkdirAll(backups, 0o700))
	for _, name := range []string{"saf-2026-10-01-0900.db", "saf-2026-10-20-0900.db", "pre-migration-002.db", "notas.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(backups, name), []byte("x"), 0o600))
	}

	_, err := Backup(conn, dir, time.Date(2026, 11, 5, 14, 0, 0, 0, time.Local))
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(backups, "saf-2026-10-01-0900.db"), "35 days old")
	assert.FileExists(t, filepath.Join(backups, "saf-2026-10-20-0900.db"))
	assert.FileExists(t, filepath.Join(backups, "pre-migration-002.db"), "pre-migration copies stay")
	assert.FileExists(t, filepath.Join(backups, "notas.txt"))
}

func TestFailedMigrationRollsBack(t *testing.T) {
	dir := t.TempDir()
	conn := openRawDB(t, filepath.Join(dir, "saf.db"))
	v1 := fstest.MapFS{"migrations/001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")}}
	broken := fstest.MapFS{
		"migrations/001_a.sql": v1["migrations/001_a.sql"],
		"migrations/002_b.sql": {Data: []byte("CREATE TABLE b (y INTEGER); INSERT INTO missing VALUES (1);")},
	}
	require.NoError(t, migrate(conn, v1, filepath.Join(dir, "backups")))

	err := migrate(conn, broken, filepath.Join(dir, "backups"))

	assert.ErrorContains(t, err, "migration migrations/002_b.sql")
	assert.Equal(t, 1, userVersion(t, conn))
	_, err = conn.Exec("SELECT y FROM b")
	assert.ErrorContains(t, err, "no such table: b")
}

func TestMigrateRefusesNewerDatabase(t *testing.T) {
	conn := openRawDB(t, filepath.Join(t.TempDir(), "saf.db"))
	_, err := conn.Exec("PRAGMA user_version = 5")
	require.NoError(t, err)

	err = migrate(conn, fstest.MapFS{"migrations/001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")}}, t.TempDir())

	assert.ErrorContains(t, err, "database schema version 5 is newer than this program (1 migrations)")
}

func TestMigrateRejectsGapsInNumbering(t *testing.T) {
	conn := openRawDB(t, filepath.Join(t.TempDir(), "saf.db"))
	files := fstest.MapFS{
		"migrations/001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")},
		"migrations/003_c.sql": {Data: []byte("CREATE TABLE c (z INTEGER);")},
	}

	err := migrate(conn, files, t.TempDir())

	assert.ErrorContains(t, err, "migration migrations/003_c.sql: name must start with 002_")
	assert.Equal(t, 0, userVersion(t, conn))
}

func TestTimesRoundTripInUTCAndSortAsText(t *testing.T) {
	fortaleza := time.FixedZone("UTC-3", -3*60*60)
	early := time.Date(2026, 10, 7, 21, 30, 0, 0, fortaleza) // 2026-10-08 00:30 UTC
	late := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

	assert.Equal(t, "2026-10-08T00:30:00Z", FormatTime(early))
	assert.Less(t, FormatTime(early), FormatTime(late))
	parsed, err := ParseTime(FormatTime(early))
	require.NoError(t, err)
	assert.True(t, parsed.Equal(early))
}

func openTestDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	conn, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func openRawDB(t *testing.T, file string) *sql.DB {
	t.Helper()
	conn, err := openFile(file)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func userVersion(t *testing.T, conn *sql.DB) int {
	t.Helper()
	var version int
	require.NoError(t, conn.QueryRow("PRAGMA user_version").Scan(&version))
	return version
}
