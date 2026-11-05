package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/stock"
)

func TestTrialFirstRunCreatesKeyAndSampleDataOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "saf-dados")
	now := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)

	store, _, err := openTrial(dir, now)
	require.NoError(t, err)
	key, err := os.ReadFile(filepath.Join(dir, "chave.key"))
	require.NoError(t, err)
	var movements int
	require.NoError(t, store.QueryRow(`SELECT COUNT(*) FROM movements`).Scan(&movements))
	assert.Positive(t, movements)
	var withName int64
	require.NoError(t, store.QueryRow(`SELECT id FROM movements WHERE patient_name IS NOT NULL LIMIT 1`).Scan(&withName))
	require.NoError(t, store.Close())

	store, box, err := openTrial(dir, now.Add(time.Hour))
	require.NoError(t, err)
	defer store.Close()

	again, err := os.ReadFile(filepath.Join(dir, "chave.key"))
	require.NoError(t, err)
	assert.Equal(t, key, again, "the key is created once")
	var after int
	require.NoError(t, store.QueryRow(`SELECT COUNT(*) FROM movements`).Scan(&after))
	assert.Equal(t, movements, after, "sample data only fills an empty database")
	name, err := stock.NewInventory(store, box, filepath.Join(dir, "photos")).PatientName(context.Background(), withName)
	require.NoError(t, err)
	assert.NotEmpty(t, name, "names saved on the first run still open")
}
