package stock

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPurgeDeletesWhatIsOlderThanSixMonths(t *testing.T) {
	inv := newInventory(t)
	dipironaID, _, _ := seedStock(t, inv)
	lots := lotIDs(t, inv, dipironaID)
	sevenMonthsAgo, lastMonth := t0.AddDate(0, -7, 0), t0.AddDate(0, -1, 0)
	dispense := func(name string, at time.Time) int64 {
		id, err := inv.RecordExit(ctx, Exit{Type: Dispensing, Date: at.Format("2006-01-02"), PatientName: name,
			Lines: []ExitLine{{Medicine: "Dipirona 500 mg comprimido", Quantity: "1", Takes: takes(map[int64]int64{lots["K12"]: 1})}}}, 1, at)
		require.NoError(t, err)
		require.NoError(t, inv.AddPhotos(ctx, id, [][]byte{testJPEG(t, 8, 8)}, 1, at))
		return id
	}
	old, recent := dispense("Maria", sevenMonthsAgo), dispense("Ana", lastMonth)
	oldPhoto, recentPhoto := photoIDs(t, inv, old)[0], photoIDs(t, inv, recent)[0]
	stockBefore := stockOf(t, inv, dipironaID)

	result, err := inv.Purge(ctx, t0)

	require.NoError(t, err)
	assert.Equal(t, PurgeResult{Photos: 1, Names: 1, Files: 1}, result)
	assert.NoFileExists(t, inv.photoPath(oldPhoto))
	assert.FileExists(t, inv.photoPath(recentPhoto))
	_, err = inv.PhotoJPEG(ctx, oldPhoto)
	assert.ErrorIs(t, err, ErrPhotoNotFound)
	oldName, err := inv.PatientName(ctx, old)
	require.NoError(t, err)
	assert.Empty(t, oldName)
	recentName, err := inv.PatientName(ctx, recent)
	require.NoError(t, err)
	assert.Equal(t, "Ana", recentName)
	assert.Equal(t, stockBefore, stockOf(t, inv, dipironaID), "movements and quantities stay")

	detail, err := inv.Movement(ctx, old)
	require.NoError(t, err)
	require.Len(t, detail.Photos, 1)
	assert.True(t, detail.Photos[0].Removed())
	assert.Empty(t, detail.Photos[0].RemovedBy, "removed by the rule, not by a person")

	again, err := inv.Purge(ctx, t0)
	require.NoError(t, err)
	assert.Equal(t, PurgeResult{}, again, "nothing left to do")
}

func TestPurgeSweepsLeftoverFiles(t *testing.T) {
	inv := newInventory(t)
	_, exitID := seedHistory(t, inv)
	require.NoError(t, inv.AddPhotos(ctx, exitID, [][]byte{testJPEG(t, 8, 8)}, 1, t0))
	kept := photoIDs(t, inv, exitID)[0]
	hoursAgo := t0.Add(-3 * time.Hour)
	write := func(name string, modified time.Time) string {
		path := filepath.Join(inv.photoDir, name)
		require.NoError(t, os.WriteFile(path, []byte("sealed"), 0o600))
		require.NoError(t, os.Chtimes(path, modified, modified))
		return path
	}
	orphan := write("9999", hoursAgo)         // a row that never committed, or a removal whose delete failed
	unfinished := write(".tmp-123", hoursAgo) // a write interrupted by a crash
	fresh := write("10000", t0)               // being saved right now: its row is not committed yet
	notOurs := write("LEIAME.txt", hoursAgo)
	require.NoError(t, os.Chtimes(inv.photoPath(kept), hoursAgo, hoursAgo))
	require.NoError(t, inv.AddPhotos(ctx, exitID, [][]byte{testJPEG(t, 8, 8)}, 1, t0))
	stuck := photoIDs(t, inv, exitID)[1] // removed by hand, but deleting its file failed
	_, err := inv.db.Exec(`UPDATE photos SET removed_at = ?, removed_by = 1 WHERE id = ?`, "2026-10-08T11:00:00Z", stuck)
	require.NoError(t, err)

	result, err := inv.Purge(ctx, t0)

	require.NoError(t, err)
	assert.Equal(t, 3, result.Files)
	assert.NoFileExists(t, inv.photoPath(stuck), "a removed photo's file goes at once, however recent")
	assert.NoFileExists(t, orphan)
	assert.NoFileExists(t, unfinished)
	assert.FileExists(t, fresh, "an hour of grace for saves in progress")
	assert.FileExists(t, notOurs)
	assert.FileExists(t, inv.photoPath(kept))
}

func TestSixMonthsCountInThePharmacyCalendar(t *testing.T) {
	inv := newInventory(t)
	_, exitID := seedHistory(t, inv)
	savedAt := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	require.NoError(t, inv.AddPhotos(ctx, exitID, [][]byte{testJPEG(t, 8, 8)}, 1, savedAt))

	result, err := inv.Purge(ctx, savedAt.AddDate(0, 6, 0).Add(-time.Minute))
	require.NoError(t, err)
	assert.Zero(t, result.Photos, "one minute short of six months")

	result, err = inv.Purge(ctx, savedAt.AddDate(0, 6, 0).Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 1, result.Photos)
}

func photoIDs(t *testing.T, inv *Inventory, movementID int64) []int64 {
	t.Helper()
	photos, err := inv.Photos(ctx, movementID)
	require.NoError(t, err)
	var ids []int64
	for _, p := range photos {
		ids = append(ids, p.ID)
	}
	return ids
}
