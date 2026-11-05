package stock

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.RGBA{R: 160, G: 45, B: 52, A: 255})
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

// withEXIF puts an APP1 "Exif" segment (where phones keep GPS) right after the JPEG start marker.
func withEXIF(jpg []byte) []byte {
	payload := append([]byte("Exif\x00\x00"), []byte("GPS -3.7319,-38.5267")...)
	segment := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, segment...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

func TestNormalizePhotoDropsMetadata(t *testing.T) {
	raw := withEXIF(testJPEG(t, 40, 30))
	require.True(t, bytes.Contains(raw, []byte("GPS")))

	out, err := NormalizePhoto(raw)

	require.NoError(t, err)
	assert.False(t, bytes.Contains(out, []byte("Exif")), "EXIF gone")
	assert.False(t, bytes.Contains(out, []byte("GPS")))
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err)
	assert.Equal(t, "jpeg", format)
	assert.Equal(t, [2]int{40, 30}, [2]int{cfg.Width, cfg.Height})
}

func TestNormalizePhotoAcceptsPNGAndRefusesTheRest(t *testing.T) {
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 8, 8))))
	out, err := NormalizePhoto(pngBytes.Bytes())
	require.NoError(t, err)
	_, format, _ := image.DecodeConfig(bytes.NewReader(out))
	assert.Equal(t, "jpeg", format, "a PNG comes out as JPEG")

	_, err = NormalizePhoto([]byte("%PDF-1.7 receita"))
	assert.ErrorIs(t, err, ErrNotAPhoto)
	_, err = NormalizePhoto(testJPEG(t, maxPhotoSide+1, 1))
	assert.ErrorIs(t, err, ErrPhotoTooLarge)
}

func TestAddedPhotosAreSealedAndBoundToTheirID(t *testing.T) {
	inv := newInventory(t)
	_, exitID := seedHistory(t, inv)
	first, second := testJPEG(t, 10, 10), testJPEG(t, 12, 12)

	require.NoError(t, inv.AddPhotos(ctx, exitID, [][]byte{first, second}, 1, t0))

	photos, err := inv.Photos(ctx, exitID)
	require.NoError(t, err)
	require.Len(t, photos, 2)
	assert.Equal(t, "Paulo Mendes", photos[0].AddedBy)
	onDisk, err := os.ReadFile(inv.photoPath(photos[0].ID))
	require.NoError(t, err)
	assert.False(t, bytes.HasPrefix(onDisk, []byte{0xFF, 0xD8}), "not a readable JPEG on disk")
	got, err := inv.PhotoJPEG(ctx, photos[0].ID)
	require.NoError(t, err)
	assert.Equal(t, first, got)

	require.NoError(t, os.WriteFile(inv.photoPath(photos[1].ID), onDisk, 0o600))
	_, err = inv.PhotoJPEG(ctx, photos[1].ID)
	assert.ErrorContains(t, err, "does not open", "a sealed photo copied over another is refused")

	items, err := inv.History(ctx, HistoryFilter{Kind: "out"})
	require.NoError(t, err)
	assert.Equal(t, 2, items[0].PhotoCount)
}

func TestRemovePhoto(t *testing.T) {
	inv := newInventory(t)
	_, exitID := seedHistory(t, inv)
	require.NoError(t, inv.AddPhotos(ctx, exitID, [][]byte{testJPEG(t, 10, 10)}, 1, t0))
	photos, _ := inv.Photos(ctx, exitID)
	id := photos[0].ID

	movementID, err := inv.RemovePhoto(ctx, id, 1, t0)

	require.NoError(t, err)
	assert.Equal(t, exitID, movementID)
	assert.NoFileExists(t, inv.photoPath(id))
	_, err = inv.PhotoJPEG(ctx, id)
	assert.ErrorIs(t, err, ErrPhotoNotFound)
	photos, _ = inv.Photos(ctx, exitID)
	assert.True(t, photos[0].Removed())
	assert.Equal(t, "Paulo Mendes", photos[0].RemovedBy)
	_, err = inv.RemovePhoto(ctx, id, 1, t0)
	assert.ErrorIs(t, err, ErrPhotoNotFound)
	items, _ := inv.History(ctx, HistoryFilter{Kind: "out"})
	assert.Equal(t, 0, items[0].PhotoCount)
}

func TestAddPhotosLeavesNothingBehindOnFailure(t *testing.T) {
	inv := newInventory(t)
	_, exitID := seedHistory(t, inv)

	assert.ErrorIs(t, inv.AddPhotos(ctx, 999, [][]byte{testJPEG(t, 4, 4)}, 1, t0), ErrMovementNotFound)

	inv.photoDir = filepath.Join(t.TempDir(), "missing")
	err := inv.AddPhotos(ctx, exitID, [][]byte{testJPEG(t, 4, 4)}, 1, t0)
	assert.ErrorContains(t, err, "create photo file")
	assert.Equal(t, 0, countRows(t, inv, "photos"), "rows rolled back with the failed file")
}
