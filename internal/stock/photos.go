package stock

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // phones sometimes send screenshots as PNG
	"os"
	"path/filepath"
	"strconv"
	"time"

	"saf/internal/db"
)

const (
	// MaxPhotosPerUpload caps one form's photos.
	MaxPhotosPerUpload = 10
	// maxPhotoSide bounds what the server decodes (the browser already shrinks to 1600 px), so a
	// crafted file cannot make it allocate gigabytes.
	maxPhotoSide = 4096
)

var (
	ErrPhotoNotFound = errors.New("photo not found")
	ErrNotAPhoto     = errors.New("file is not a JPEG or PNG image")
	ErrPhotoTooLarge = errors.New("photo is larger than 4096 px on a side")
)

// NormalizePhoto checks that raw is a JPEG or PNG image of at most 4096 px a side and re-encodes
// it as JPEG. Re-encoding drops every bit of metadata (EXIF, GPS), whatever the browser did.
func NormalizePhoto(raw []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, ErrNotAPhoto
	}
	if cfg.Width > maxPhotoSide || cfg.Height > maxPhotoSide {
		return nil, ErrPhotoTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrNotAPhoto
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 82}); err != nil {
		return nil, fmt.Errorf("encode photo: %w", err)
	}
	return out.Bytes(), nil
}

// Photo is one picture attached to a movement. A removed photo keeps its row (who, when) but its
// file is gone.
type Photo struct {
	ID         int64
	MovementID int64
	AddedAt    time.Time
	AddedBy    string
	RemovedAt  time.Time // zero while the photo exists
	RemovedBy  string    // "" with RemovedAt set: deleted by the 6-month rule
}

func (p Photo) Removed() bool { return !p.RemovedAt.IsZero() }

func photoContext(id int64) string { return "photo:" + strconv.FormatInt(id, 10) }

func (inv *Inventory) photoPath(id int64) string {
	return filepath.Join(inv.photoDir, strconv.FormatInt(id, 10))
}

// AddPhotos attaches photos (already through NormalizePhoto) to a movement. Each is sealed with
// the key and written to its own file; a failure leaves neither rows nor files behind.
func (inv *Inventory) AddPhotos(ctx context.Context, movementID int64, photos [][]byte, userID int64, now time.Time) error {
	var written []string
	err := inv.inTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM movements WHERE id = ?)`, movementID).Scan(&exists); err != nil {
			return fmt.Errorf("look up movement %d: %w", movementID, err)
		}
		if !exists {
			return ErrMovementNotFound
		}
		for _, photo := range photos {
			res, err := tx.ExecContext(ctx, `INSERT INTO photos (movement_id, added_at, added_by) VALUES (?, ?, ?)`,
				movementID, db.FormatTime(now), userID)
			if err != nil {
				return fmt.Errorf("insert photo of movement %d: %w", movementID, err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			path := inv.photoPath(id)
			if err := writeFileAtomic(path, inv.box.Seal(photo, photoContext(id))); err != nil {
				return err
			}
			written = append(written, path)
		}
		return nil
	})
	if err != nil {
		for _, path := range written {
			os.Remove(path) // the rows rolled back; the files must go too
		}
	}
	return err
}

// writeFileAtomic writes to a temporary file and renames it, so a crash never leaves half a photo.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create photo file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write photo file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync photo file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close photo file: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}

// PhotoJPEG returns a photo's image, decrypted. Removed photos are not found.
func (inv *Inventory) PhotoJPEG(ctx context.Context, id int64) ([]byte, error) {
	var removed bool
	err := inv.db.QueryRowContext(ctx, `SELECT removed_at IS NOT NULL FROM photos WHERE id = ?`, id).Scan(&removed)
	if errors.Is(err, sql.ErrNoRows) || removed {
		return nil, ErrPhotoNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("look up photo %d: %w", id, err)
	}
	sealed, err := os.ReadFile(inv.photoPath(id))
	if err != nil {
		return nil, fmt.Errorf("read photo %d: %w", id, err)
	}
	photo, err := inv.box.Open(sealed, photoContext(id))
	if err != nil {
		return nil, fmt.Errorf("open photo %d: %w", id, err)
	}
	return photo, nil
}

// RemovePhoto takes a photo off its movement: the row records who and when, the file is deleted.
// It returns the movement's id.
func (inv *Inventory) RemovePhoto(ctx context.Context, id, userID int64, now time.Time) (int64, error) {
	var movementID int64
	err := inv.db.QueryRowContext(ctx, `SELECT movement_id FROM photos WHERE id = ? AND removed_at IS NULL`, id).Scan(&movementID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrPhotoNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("look up photo %d: %w", id, err)
	}
	_, err = inv.db.ExecContext(ctx, `UPDATE photos SET removed_at = ?, removed_by = ? WHERE id = ? AND removed_at IS NULL`,
		db.FormatTime(now), userID, id)
	if err != nil {
		return 0, fmt.Errorf("remove photo %d: %w", id, err)
	}
	// The row already says removed; if deleting the file fails, the error surfaces and the file
	// stays sealed until removed by hand.
	if err := os.Remove(inv.photoPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return movementID, fmt.Errorf("delete photo file %d: %w", id, err)
	}
	return movementID, nil
}

// Photos lists a movement's photos, removed ones included, in the order they were added.
func (inv *Inventory) Photos(ctx context.Context, movementID int64) ([]Photo, error) {
	rows, err := inv.db.QueryContext(ctx, `
		SELECT p.id, p.added_at, a.name, COALESCE(p.removed_at, ''), COALESCE(r.name, '')
		FROM photos p JOIN users a ON a.id = p.added_by LEFT JOIN users r ON r.id = p.removed_by
		WHERE p.movement_id = ? ORDER BY p.id`, movementID)
	if err != nil {
		return nil, fmt.Errorf("list photos of movement %d: %w", movementID, err)
	}
	defer rows.Close()
	var photos []Photo
	for rows.Next() {
		p := Photo{MovementID: movementID}
		var added, removed string
		if err := rows.Scan(&p.ID, &added, &p.AddedBy, &removed, &p.RemovedBy); err != nil {
			return nil, err
		}
		if p.AddedAt, err = db.ParseTime(added); err != nil {
			return nil, err
		}
		if removed != "" {
			if p.RemovedAt, err = db.ParseTime(removed); err != nil {
				return nil, err
			}
		}
		photos = append(photos, p)
	}
	return photos, rows.Err()
}
