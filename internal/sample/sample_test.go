package sample

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"saf/internal/auth"
	"saf/internal/db"
	"saf/internal/seal"
	"saf/internal/stock"
)

func TestFill(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	box, err := seal.New(make([]byte, 32))
	require.NoError(t, err)
	accounts, inventory := auth.NewAccounts(conn, bcrypt.MinCost), stock.NewInventory(conn, box, t.TempDir())
	now := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)

	require.NoError(t, Fill(ctx, accounts, inventory, now))

	admin, err := accounts.Authenticate(ctx, AdminLogin, Password, now)
	require.NoError(t, err)
	assert.True(t, admin.IsAdmin())
	viewer, err := accounts.Authenticate(ctx, ViewerLogin, Password, now)
	require.NoError(t, err)
	assert.False(t, viewer.IsAdmin())

	groups, counts, err := inventory.StockGroups(ctx, stock.StockFilter{}, now)
	require.NoError(t, err)
	assert.Equal(t, 6, counts.Medicines, "Captopril is archived")
	assert.Equal(t, 1, counts.Expired, "Dipirona X999")
	assert.Equal(t, 2, counts.Expiring, "Dipirona B777 and Amoxicilina K12")
	totals := map[string]int64{}
	for _, g := range groups {
		totals[g.Medicine.Ingredient] = g.Total
	}
	assert.Equal(t, map[string]int64{
		"Amoxicilina": 184, "Cetoconazol": 0, "Dipirona": 258, "Ibuprofeno": 12, "Losartana potássica": 300, "Paracetamol": 0,
	}, totals)

	history, err := inventory.History(ctx, stock.HistoryFilter{})
	require.NoError(t, err)
	kinds := map[string]int{}
	var withPhoto, withName int
	for _, it := range history {
		kinds[it.Kind]++
		withPhoto += it.PhotoCount
		if it.PatientName != "" {
			withName++
		}
	}
	assert.Equal(t, map[string]int{"in": 2, "out": 5, "reversal": 1, "adjust": 2}, kinds)
	assert.Equal(t, 1, withPhoto)
	assert.Equal(t, 3, withName, "Pedro, Carla and João")
	assert.Equal(t, "out", history[0].Kind, "today's dispensação to João comes first")
	assert.Equal(t, "João", history[0].PatientName)
}
