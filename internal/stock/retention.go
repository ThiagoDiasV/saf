package stock

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"saf/internal/db"
)

// RetentionMonths: photos and patients' first names are deleted this long after they were saved
// (LGPD, docs/PLAN.md §1). Movements and quantities stay forever.
const RetentionMonths = 6

// fileGrace protects files written in the last hour from the sweep: a photo is written to disk
// a moment before its row is committed.
const fileGrace = time.Hour

// PurgeResult counts what one purge deleted.
type PurgeResult struct {
	Photos int // photos past the six months
	Names  int // first names past the six months
	Files  int // photo files deleted from disk, including leftovers of earlier failures
}

// Purge deletes photos and first names saved more than six months before now. A purged photo
// keeps its row, marked removed by nobody, so the movement shows it was deleted by the rule.
// Run it daily.
func (inv *Inventory) Purge(ctx context.Context, now time.Time) (PurgeResult, error) {
	cutoff := db.FormatTime(now.In(TimeZone).AddDate(0, -RetentionMonths, 0))
	var result PurgeResult

	res, err := inv.db.ExecContext(ctx, `UPDATE photos SET removed_at = ? WHERE removed_at IS NULL AND added_at < ?`,
		db.FormatTime(now), cutoff)
	if err != nil {
		return result, fmt.Errorf("purge photos: %w", err)
	}
	if result.Photos, err = rowsAffected(res); err != nil {
		return result, err
	}
	// secure_delete (set on every connection) overwrites the old values in the database file.
	res, err = inv.db.ExecContext(ctx, `UPDATE movements SET patient_name = NULL WHERE patient_name IS NOT NULL AND recorded_at < ?`,
		cutoff)
	if err != nil {
		return result, fmt.Errorf("purge first names: %w", err)
	}
	if result.Names, err = rowsAffected(res); err != nil {
		return result, err
	}
	result.Files, err = inv.sweepPhotoFiles(ctx, now)
	return result, err
}

func rowsAffected(res sql.Result) (int, error) {
	n, err := res.RowsAffected()
	return int(n), err
}

// sweepPhotoFiles deletes the files of removed photos (purged here, or removed by hand when the
// delete failed), files with no row at all (a save that failed between file and commit), and
// temporary files of interrupted writes. Files with no row get an hour of grace: a save in
// progress writes its file a moment before committing the row.
func (inv *Inventory) sweepPhotoFiles(ctx context.Context, now time.Time) (int, error) {
	removed := map[string]bool{} // photo id → its row says removed
	rows, err := inv.db.QueryContext(ctx, `SELECT id, removed_at IS NOT NULL FROM photos`)
	if err != nil {
		return 0, fmt.Errorf("list photos: %w", err)
	}
	for rows.Next() {
		var id int64
		var gone bool
		if err := rows.Scan(&id, &gone); err != nil {
			rows.Close()
			return 0, err
		}
		removed[strconv.FormatInt(id, 10)] = gone
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(inv.photoDir)
	if err != nil {
		return 0, fmt.Errorf("list photo files: %w", err)
	}
	deleted := 0
	for _, e := range entries {
		name := e.Name()
		gone, hasRow := removed[name]
		_, numErr := strconv.ParseInt(name, 10, 64)
		switch {
		case hasRow && !gone:
			continue // a photo in use
		case hasRow && gone:
			// its row says removed: delete now
		case numErr == nil || strings.HasPrefix(name, ".tmp-"):
			info, err := e.Info()
			if err != nil || now.Sub(info.ModTime()) < fileGrace {
				continue
			}
		default:
			continue // not a photo file
		}
		if err := os.Remove(filepath.Join(inv.photoDir, name)); err != nil && !os.IsNotExist(err) {
			return deleted, fmt.Errorf("delete photo file %s: %w", name, err)
		}
		deleted++
	}
	return deleted, nil
}
