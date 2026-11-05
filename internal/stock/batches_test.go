package stock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/form"
)

func TestParseAndFormatExpiry(t *testing.T) {
	for text, want := range map[string]string{"03/2027": "2027-03", " 12/2030 ": "2030-12"} {
		got, ok := parseExpiry(text)
		assert.True(t, ok, text)
		assert.Equal(t, want, got, text)
	}
	for _, bad := range []string{"", "3/2027", "13/2027", "00/2027", "03/27", "03-2027", "03/1999"} {
		_, ok := parseExpiry(bad)
		assert.False(t, ok, bad)
	}
	assert.Equal(t, "03/2027", FormatExpiry("2027-03"))
}

func TestExpiryLastsToTheEndOfTheMonthInFortaleza(t *testing.T) {
	sept30Evening := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC) // 23:00 on Sep 30 in Fortaleza
	oct1Morning := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	assert.Equal(t, 0, daysToExpiry("2026-09", sept30Evening), "still good on its last day")
	assert.Equal(t, -1, daysToExpiry("2026-09", oct1Morning))
	assert.Equal(t, 30, daysToExpiry("2026-10", oct1Morning))

	b := Batch{DaysLeft: 90}
	assert.True(t, b.Expiring())
	b.DaysLeft = 91
	assert.False(t, b.Expiring())
	b.DaysLeft = -1
	assert.True(t, b.Expired())
	assert.False(t, b.Expiring())
}

// seedStock: Dipirona with an expired, an expiring and a good lot; Paracetamol with one good lot;
// Cetoconazol never received; Captopril archived. t0 is 2026-10-08.
func seedStock(t *testing.T, inv *Inventory) (dipironaID, paracetamolID, cetoconazolID int64) {
	t.Helper()
	dipironaID = createMedicine(t, inv, dipirona)
	paracetamolID = createMedicine(t, inv, MedicineInput{Ingredient: "Paracetamol", Strength: "750 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	cetoconazolID = createMedicine(t, inv, MedicineInput{Ingredient: "Cetoconazol", Strength: "20 mg/g", DosageForm: "Creme", Unit: "bisnaga"})
	captopril := createMedicine(t, inv, MedicineInput{Ingredient: "Captopril", Strength: "25 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	_, err := inv.RecordEntry(ctx, Entry{Type: "Saldo inicial", Date: "2026-10-01", Lines: []EntryLine{
		dipironaLine("K12", "Prati-Donaduzzi", "08/2027", "100"),
		dipironaLine("X999", "EMS", "09/2026", "20"),
		dipironaLine("B777", "Medley", "12/2026", "150"),
		{Medicine: "Paracetamol 750 mg comprimido", Lot: "P750", Manufacturer: "EMS", Expiry: "06/2027", Quantity: "40"},
	}}, 1, t0)
	require.NoError(t, err)
	require.NoError(t, inv.SetMedicineArchived(ctx, captopril, true, 1, t0))
	return dipironaID, paracetamolID, cetoconazolID
}

func lotsOf(g StockGroup) []string {
	var lots []string
	for _, b := range g.Batches {
		lots = append(lots, b.Lot)
	}
	return lots
}

func TestStockGroups(t *testing.T) {
	inv := newInventory(t)
	dipironaID, _, cetoconazolID := seedStock(t, inv)

	groups, counts, err := inv.StockGroups(ctx, StockFilter{}, t0)

	require.NoError(t, err)
	assert.Equal(t, StockCounts{Medicines: 3, Batches: 4, Expiring: 1, Expired: 1}, counts, "archived Captopril not counted")
	require.Len(t, groups, 3)
	assert.Equal(t, []string{"Cetoconazol", "Dipirona", "Paracetamol"},
		[]string{groups[0].Medicine.Ingredient, groups[1].Medicine.Ingredient, groups[2].Medicine.Ingredient})
	assert.Equal(t, cetoconazolID, groups[0].Medicine.ID)
	assert.Empty(t, groups[0].Batches, "never received: shown as Sem estoque")

	d := groups[1]
	assert.Equal(t, dipironaID, d.Medicine.ID)
	assert.Equal(t, []string{"X999", "B777", "K12"}, lotsOf(d), "closest expiry first")
	assert.Equal(t, int64(270), d.Total)
	assert.Equal(t, int64(20), d.ExpiredUnits)
	assert.Equal(t, 1, d.ExpiredBatches)
	assert.Equal(t, 1, d.ExpiringBatches)
	assert.Equal(t, 3, d.BatchCount)
}

func TestStockGroupsFilters(t *testing.T) {
	inv := newInventory(t)
	seedStock(t, inv)
	groupsFor := func(f StockFilter) map[string][]string {
		groups, _, err := inv.StockGroups(ctx, f, t0)
		require.NoError(t, err)
		out := map[string][]string{}
		for _, g := range groups {
			out[g.Medicine.Ingredient] = lotsOf(g)
		}
		return out
	}

	assert.Equal(t, map[string][]string{"Dipirona": {"X999", "B777", "K12"}}, groupsFor(StockFilter{Query: "DIPIRONA"}))
	assert.Equal(t, map[string][]string{"Dipirona": {"X999"}, "Paracetamol": {"P750"}}, groupsFor(StockFilter{Query: "ems"}), "by manufacturer")
	assert.Equal(t, map[string][]string{"Dipirona": {"K12"}}, groupsFor(StockFilter{Query: "k 12"}), "by lot")
	assert.Equal(t, map[string][]string{"Dipirona": {"X999"}}, groupsFor(StockFilter{Validity: "vencidos"}))
	assert.Equal(t, map[string][]string{"Dipirona": {"B777"}}, groupsFor(StockFilter{Validity: "vencendo"}))
	assert.Equal(t, map[string][]string{}, groupsFor(StockFilter{Query: "paracetamol", Validity: "vencidos"}))
	assert.True(t, StockFilter{Query: " "}.Filtering() == false && StockFilter{Validity: "vencidos"}.Filtering())
}

func TestUpdateBatch(t *testing.T) {
	inv := newInventory(t)
	seedStock(t, inv)
	groups, _, err := inv.StockGroups(ctx, StockFilter{Query: "K12"}, t0)
	require.NoError(t, err)
	k12 := groups[0].Batches[0]

	_, err = inv.UpdateBatch(ctx, k12.ID, BatchInput{Lot: "", Manufacturer: "", Expiry: "8/27"}, 1, t0)
	assert.Equal(t, form.Errors{"lot": "Informe o lote.", "manufacturer": "Informe o fabricante.", "expiry": "Use o formato MM/AAAA."}, err)

	_, err = inv.UpdateBatch(ctx, k12.ID, BatchInput{Lot: "b 777", Manufacturer: "MEDLEY", Expiry: "08/2027"}, 1, t0)
	assert.Equal(t, form.Errors{"lot": "Já existe o lote B777 (Medley) deste medicamento."}, err)

	changed, err := inv.UpdateBatch(ctx, k12.ID, BatchInput{Lot: "K13", Manufacturer: "Prati-Donaduzzi", Expiry: "09/2027"}, 1, t0)
	require.NoError(t, err)
	assert.True(t, changed)
	b, err := inv.Batch(ctx, k12.ID, t0)
	require.NoError(t, err)
	assert.Equal(t, "K13", b.Lot)
	assert.Equal(t, "09/2027", b.Expiry())
	assert.Equal(t, int64(100), b.OnHand)

	rows, err := inv.db.Query(`SELECT field, old_value, new_value FROM changes WHERE batch_id = ? ORDER BY id`, k12.ID)
	require.NoError(t, err)
	defer rows.Close()
	var logged []loggedChange
	for rows.Next() {
		var c loggedChange
		require.NoError(t, rows.Scan(&c.Field, &c.Old, &c.New))
		logged = append(logged, c)
	}
	assert.Equal(t, []loggedChange{{"Lote", "K12", "K13"}, {"Validade", "08/2027", "09/2027"}}, logged)

	changed, err = inv.UpdateBatch(ctx, k12.ID, BatchInput{Lot: "K13", Manufacturer: "Prati-Donaduzzi", Expiry: "09/2027"}, 1, t0)
	require.NoError(t, err)
	assert.False(t, changed)
	_, err = inv.UpdateBatch(ctx, 999, BatchInput{}, 1, t0)
	assert.ErrorIs(t, err, ErrBatchNotFound)
}

func TestManufacturers(t *testing.T) {
	inv := newInventory(t)
	seedStock(t, inv)

	names, err := inv.Manufacturers(ctx)

	require.NoError(t, err)
	assert.Equal(t, []string{"EMS", "Medley", "Prati-Donaduzzi"}, names)
}
