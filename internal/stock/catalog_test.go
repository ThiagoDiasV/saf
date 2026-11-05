package stock

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/db"
	"saf/internal/form"
	"saf/internal/seal"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
)

var dipirona = MedicineInput{Ingredient: "Dipirona", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"}

// newInventory opens a fresh database with one admin (id 1) to sign changes.
func newInventory(t *testing.T) *Inventory {
	t.Helper()
	conn, err := db.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	_, err = conn.Exec(`INSERT INTO users (id, name, login, password_hash, role, created_at)
		VALUES (1, 'Paulo Mendes', 'paulo.mendes', 'x', 'admin', '2026-10-08T12:00:00Z')`)
	require.NoError(t, err)
	box, err := seal.New(make([]byte, 32)) // a fixed key is fine for tests
	require.NoError(t, err)
	return NewInventory(conn, box, t.TempDir())
}

func createMedicine(t *testing.T, inv *Inventory, in MedicineInput) int64 {
	t.Helper()
	id, err := inv.CreateMedicine(ctx, in, t0)
	require.NoError(t, err)
	return id
}

// receive puts quantity units of medicineID in stock through a raw entry (lot A123), as Entrada
// will once it exists.
func receive(t *testing.T, inv *Inventory, medicineID, quantity int64) {
	t.Helper()
	_, err := inv.db.Exec(`INSERT INTO batches (medicine_id, lot, manufacturer, expiry_month, match_key)
		VALUES (?, 'A123', 'EMS', '2027-03', 'a123ems') ON CONFLICT (medicine_id, match_key) DO NOTHING`, medicineID)
	require.NoError(t, err)
	res, err := inv.db.Exec(`INSERT INTO movements (kind, type, occurred_on, recorded_at, user_id)
		VALUES ('in', 'Doação', '2026-10-08', '2026-10-08T12:00:00Z', 1)`)
	require.NoError(t, err)
	movementID, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = inv.db.Exec(`INSERT INTO movement_lines (movement_id, batch_id, quantity)
		VALUES (?, (SELECT id FROM batches WHERE medicine_id = ? AND match_key = 'a123ems'), ?)`,
		movementID, medicineID, quantity)
	require.NoError(t, err)
}

type loggedChange struct{ Field, Old, New string }

func changesOf(t *testing.T, inv *Inventory, medicineID int64) []loggedChange {
	t.Helper()
	rows, err := inv.db.Query(`SELECT field, old_value, new_value FROM changes WHERE medicine_id = ? ORDER BY id`, medicineID)
	require.NoError(t, err)
	defer rows.Close()
	var out []loggedChange
	for rows.Next() {
		var c loggedChange
		require.NoError(t, rows.Scan(&c.Field, &c.Old, &c.New))
		out = append(out, c)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestCreateMedicineCleansInput(t *testing.T) {
	inv := newInventory(t)

	id := createMedicine(t, inv, MedicineInput{Ingredient: "  Dipirona   sódica ", Strength: "500  mg", DosageForm: "Comprimido", Unit: " Comprimido "})

	m, err := inv.Medicine(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, Medicine{ID: id, Ingredient: "Dipirona sódica", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"}, m)
	assert.Equal(t, "Dipirona sódica 500 mg comprimido", m.Label())
}

func TestCreateMedicineRequiresEveryField(t *testing.T) {
	inv := newInventory(t)

	_, err := inv.CreateMedicine(ctx, MedicineInput{Ingredient: " ", Strength: "", DosageForm: "", Unit: ""}, t0)

	assert.Equal(t, form.Errors{
		"ingredient":  "Informe o princípio ativo.",
		"strength":    "Informe a concentração.",
		"dosage_form": "Informe a forma farmacêutica.",
		"unit":        "Informe a unidade de contagem.",
	}, err)
}

func TestCreateMedicineRejectsDuplicates(t *testing.T) {
	inv := newInventory(t)
	createMedicine(t, inv, MedicineInput{Ingredient: "Dipirona sódica", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"})

	_, err := inv.CreateMedicine(ctx, MedicineInput{Ingredient: "DIPIRONA SODICA", Strength: "500mg", DosageForm: "comprimido", Unit: "unidade"}, t0)

	assert.Equal(t, form.Errors{"dosage_form": "Já existe: Dipirona sódica 500 mg comprimido. Maiúsculas, acentos e espaços não contam."}, err)
}

func TestDuplicateOfArchivedMedicineSuggestsRestoring(t *testing.T) {
	inv := newInventory(t)
	id := createMedicine(t, inv, dipirona)
	require.NoError(t, inv.SetMedicineArchived(ctx, id, true, 1, t0))

	_, err := inv.CreateMedicine(ctx, dipirona, t0)

	assert.Equal(t, form.Errors{"dosage_form": "Já existe, arquivado: Dipirona 500 mg comprimido. Restaure em vez de cadastrar de novo."}, err)
}

func TestUpdateMedicineLogsEachChangedField(t *testing.T) {
	inv := newInventory(t)
	id := createMedicine(t, inv, MedicineInput{Ingredient: "Dipirna", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"})

	changed, err := inv.UpdateMedicine(ctx, id, MedicineInput{Ingredient: "Dipirona", Strength: "500 mg", DosageForm: "Comprimido", Unit: "Unidade"}, 1, t0)

	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []loggedChange{{"Princípio ativo", "Dipirna", "Dipirona"}, {"Unidade", "comprimido", "unidade"}}, changesOf(t, inv, id))
	m, _ := inv.Medicine(ctx, id)
	assert.Equal(t, "Dipirona 500 mg comprimido", m.Label())

	changed, err = inv.UpdateMedicine(ctx, id, MedicineInput{Ingredient: "Dipirona", Strength: "500 mg", DosageForm: "Comprimido", Unit: "unidade"}, 1, t0)
	require.NoError(t, err)
	assert.False(t, changed, "same values log nothing")
	assert.Len(t, changesOf(t, inv, id), 2)
}

func TestUpdateMedicineRejectsDuplicateOfAnother(t *testing.T) {
	inv := newInventory(t)
	createMedicine(t, inv, dipirona)
	id := createMedicine(t, inv, MedicineInput{Ingredient: "Paracetamol", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"})

	_, err := inv.UpdateMedicine(ctx, id, dipirona, 1, t0)

	assert.Equal(t, form.Errors{"dosage_form": "Já existe: Dipirona 500 mg comprimido. Maiúsculas, acentos e espaços não contam."}, err)
	_, err = inv.UpdateMedicine(ctx, 999, dipirona, 1, t0)
	assert.ErrorIs(t, err, ErrMedicineNotFound)
}

func TestArchiveNeedsZeroStock(t *testing.T) {
	inv := newInventory(t)
	id := createMedicine(t, inv, dipirona)
	receive(t, inv, id, 30)

	assert.ErrorIs(t, inv.SetMedicineArchived(ctx, id, true, 1, t0), ErrStillInStock)

	receive(t, inv, id, -30)
	require.NoError(t, inv.SetMedicineArchived(ctx, id, true, 1, t0))
	m, _ := inv.Medicine(ctx, id)
	assert.True(t, m.Archived)
}

func TestArchiveAndRestoreAreLogged(t *testing.T) {
	inv := newInventory(t)
	id := createMedicine(t, inv, dipirona)

	require.NoError(t, inv.SetMedicineArchived(ctx, id, true, 1, t0))
	require.NoError(t, inv.SetMedicineArchived(ctx, id, true, 1, t0)) // already archived: nothing to log
	require.NoError(t, inv.SetMedicineArchived(ctx, id, false, 1, t0))

	assert.Equal(t, []loggedChange{{"Situação", "Ativo", "Arquivado"}, {"Situação", "Arquivado", "Ativo"}}, changesOf(t, inv, id))
}

func TestDeleteOnlyNeverMovedMedicines(t *testing.T) {
	inv := newInventory(t)
	edited := createMedicine(t, inv, MedicineInput{Ingredient: "Dipirna", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	_, err := inv.UpdateMedicine(ctx, edited, dipirona, 1, t0)
	require.NoError(t, err)
	moved := createMedicine(t, inv, MedicineInput{Ingredient: "Paracetamol", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	receive(t, inv, moved, 10)
	receive(t, inv, moved, -10)

	require.NoError(t, inv.DeleteMedicine(ctx, edited), "its edit log goes with it")
	_, err = inv.Medicine(ctx, edited)
	assert.ErrorIs(t, err, ErrMedicineNotFound)
	assert.Empty(t, changesOf(t, inv, edited))

	assert.ErrorIs(t, inv.DeleteMedicine(ctx, moved), ErrHasMovements, "even with zero stock")
	assert.ErrorIs(t, inv.DeleteMedicine(ctx, 999), ErrMedicineNotFound)
}

func TestMedicinesListsStockAndFilters(t *testing.T) {
	inv := newInventory(t)
	paracetamol := createMedicine(t, inv, MedicineInput{Ingredient: "Paracetamol", Strength: "750 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	dipironaID := createMedicine(t, inv, MedicineInput{Ingredient: "Dipirona sódica", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	acido := createMedicine(t, inv, MedicineInput{Ingredient: "Ácido fólico", Strength: "5 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	receive(t, inv, dipironaID, 120)
	receive(t, inv, dipironaID, -20)
	require.NoError(t, inv.SetMedicineArchived(ctx, paracetamol, true, 1, t0))

	active, err := inv.Medicines(ctx, MedicineFilter{})
	require.NoError(t, err)
	require.Len(t, active, 2)
	assert.Equal(t, []int64{acido, dipironaID}, []int64{active[0].ID, active[1].ID}, "Á sorts with A")
	assert.Equal(t, int64(100), active[1].OnHand)
	assert.True(t, active[1].Moved)
	assert.False(t, active[0].Moved)

	all, err := inv.Medicines(ctx, MedicineFilter{IncludeArchived: true})
	require.NoError(t, err)
	assert.Len(t, all, 3)

	found, err := inv.Medicines(ctx, MedicineFilter{Query: "DIPIRONA sodica 500"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, dipironaID, found[0].ID)
}

func TestCatalogSuggestions(t *testing.T) {
	inv := newInventory(t)
	createMedicine(t, inv, MedicineInput{Ingredient: "Dipirona", Strength: "500 mg", DosageForm: "comprimido", Unit: "comprimido"})
	createMedicine(t, inv, MedicineInput{Ingredient: "Nistatina", Strength: "100.000 UI/mL", DosageForm: "Suspensão oral", Unit: "Frasco conta-gotas"})

	s, err := inv.CatalogSuggestions(ctx)

	require.NoError(t, err)
	assert.Equal(t, []string{"Dipirona", "Nistatina"}, s.Ingredients)
	assert.Contains(t, s.DosageForms, "Comprimido")
	assert.NotContains(t, s.DosageForms, "comprimido", "same form spelled differently is listed once")
	assert.Contains(t, s.Units, "frasco conta-gotas")
	assert.Equal(t, "ampola", s.Units[0], "sorted")
}
