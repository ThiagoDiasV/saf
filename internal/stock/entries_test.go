package stock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/form"
)

// entryOf is a one-line Doação dated today.
func entryOf(lines ...EntryLine) Entry {
	return Entry{Type: "Doação", Date: "2026-10-08", Lines: lines}
}

func dipironaLine(lot, manufacturer, expiry, quantity string) EntryLine {
	return EntryLine{Medicine: "Dipirona 500 mg comprimido", Lot: lot, Manufacturer: manufacturer, Expiry: expiry, Quantity: quantity}
}

func stockOf(t *testing.T, inv *Inventory, medicineID int64) int64 {
	t.Helper()
	m, err := inv.Medicine(ctx, medicineID)
	require.NoError(t, err)
	return m.OnHand
}

func countRows(t *testing.T, inv *Inventory, table string) int {
	t.Helper()
	var n int
	require.NoError(t, inv.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
	return n
}

func TestRecordEntrySavesMovementAndLots(t *testing.T) {
	inv := newInventory(t)
	dipironaID := createMedicine(t, inv, dipirona)
	paracetamolID := createMedicine(t, inv, MedicineInput{Ingredient: "Paracetamol", Strength: "750 mg", DosageForm: "Comprimido", Unit: "comprimido"})

	id, err := inv.RecordEntry(ctx, Entry{Type: "Saldo inicial", Date: "2026-10-01", Note: "Planilha antiga", Lines: []EntryLine{
		dipironaLine("A123", "EMS", "03/2027", "200"),
		{Medicine: "paracetamol 750MG comprimido", Lot: "P750", Manufacturer: "Medley", Expiry: "11/2026", Quantity: " 40 "},
	}}, 1, t0)

	require.NoError(t, err)
	var kind, typ, occurred, recorded, note string
	var userID int64
	require.NoError(t, inv.db.QueryRow(`SELECT kind, type, occurred_on, recorded_at, user_id, note FROM movements WHERE id = ?`, id).
		Scan(&kind, &typ, &occurred, &recorded, &userID, &note))
	assert.Equal(t, []any{"in", "Saldo inicial", "2026-10-01", "2026-10-08T12:00:00Z", int64(1), "Planilha antiga"},
		[]any{kind, typ, occurred, recorded, userID, note}, "backdated movement keeps when it was recorded")
	assert.Equal(t, int64(200), stockOf(t, inv, dipironaID))
	assert.Equal(t, int64(40), stockOf(t, inv, paracetamolID))
}

func TestSameLotMergesWhateverTheSpelling(t *testing.T) {
	inv := newInventory(t)
	id := createMedicine(t, inv, dipirona)
	_, err := inv.RecordEntry(ctx, entryOf(dipironaLine("A123", "EMS Sigma Pharma", "03/2027", "200")), 1, t0)
	require.NoError(t, err)

	_, err = inv.RecordEntry(ctx, entryOf(dipironaLine(" a 123", "ems sigma  pharma", "03/2027", "50")), 1, t0)

	require.NoError(t, err)
	assert.Equal(t, 1, countRows(t, inv, "batches"), "one lot, not two")
	assert.Equal(t, int64(250), stockOf(t, inv, id))
}

func TestLotWithAnotherExpiryIsRefused(t *testing.T) {
	inv := newInventory(t)
	id := createMedicine(t, inv, dipirona)
	_, err := inv.RecordEntry(ctx, entryOf(dipironaLine("A123", "EMS", "03/2027", "200")), 1, t0)
	require.NoError(t, err)

	_, err = inv.RecordEntry(ctx, entryOf(dipironaLine("a123", "ems", "04/2027", "50")), 1, t0)

	assert.Equal(t, form.Errors{"lines.0.expiry": "O lote A123 (EMS) já existe com validade 03/2027. Corrija aqui ou edite o lote no Estoque."}, err)
	assert.Equal(t, int64(200), stockOf(t, inv, id))
	assert.Equal(t, 1, countRows(t, inv, "movements"))
}

func TestTwinLinesMustAgreeOnExpiry(t *testing.T) {
	inv := newInventory(t)
	createMedicine(t, inv, dipirona)

	_, err := inv.RecordEntry(ctx, entryOf(
		dipironaLine("NEW1", "EMS", "03/2027", "10"),
		dipironaLine("new1", "EMS", "05/2027", "10"),
	), 1, t0)
	assert.Equal(t, form.Errors{"lines.1.expiry": "Este lote aparece em outra linha com outra validade."}, err)

	_, err = inv.RecordEntry(ctx, entryOf(
		dipironaLine("NEW1", "EMS", "03/2027", "10"),
		dipironaLine("new1", "EMS", "03/2027", "5"),
	), 1, t0)
	require.NoError(t, err, "same lot twice with the same expiry is fine")
	assert.Equal(t, 1, countRows(t, inv, "batches"))
}

func TestRecordEntryValidation(t *testing.T) {
	inv := newInventory(t)
	createMedicine(t, inv, dipirona)
	archived := createMedicine(t, inv, MedicineInput{Ingredient: "Captopril", Strength: "25 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	require.NoError(t, inv.SetMedicineArchived(ctx, archived, true, 1, t0))

	_, err := inv.RecordEntry(ctx, Entry{}, 1, t0)
	assert.Equal(t, form.Errors{
		"type":             "Escolha o tipo de entrada.",
		"date":             "Informe a data.",
		"lines.0.medicine": "Adicione pelo menos um medicamento.",
	}, err)

	_, err = inv.RecordEntry(ctx, Entry{Type: "Outros", Date: "2026-10-09", Lines: []EntryLine{
		{Medicine: "Ibuprofeno 600 mg comprimido", Lot: "", Manufacturer: "", Expiry: "3/27", Quantity: "1,5"},
		{Medicine: "Captopril 25 mg comprimido", Lot: "C25", Manufacturer: "EMS", Expiry: "13/2027", Quantity: "0"},
		dipironaLine("A123", "EMS", "03/2027", "2000000"),
	}}, 1, t0)
	assert.Equal(t, form.Errors{
		"date":                 "A data não pode estar no futuro.",
		"note":                 "Descreva a entrada quando o tipo é Outros.",
		"lines.0.medicine":     "Escolha um medicamento da lista. Se não existir, cadastre em Medicamentos.",
		"lines.0.lot":          "Informe o lote.",
		"lines.0.manufacturer": "Informe o fabricante.",
		"lines.0.expiry":       "Validade no formato MM/AAAA.",
		"lines.0.quantity":     "Quantidade: número inteiro maior que zero.",
		"lines.1.medicine":     "Escolha um medicamento da lista. Se não existir, cadastre em Medicamentos.",
		"lines.1.expiry":       "Validade no formato MM/AAAA.",
		"lines.1.quantity":     "Quantidade: número inteiro maior que zero.",
		"lines.2.quantity":     "Quantidade grande demais. Confira o número.",
	}, err)
	assert.Equal(t, 0, countRows(t, inv, "movements"), "nothing is saved when any line is wrong")
}

func TestDateUsesPharmacyTimeZone(t *testing.T) {
	inv := newInventory(t)
	createMedicine(t, inv, dipirona)
	lateEvening := time.Date(2026, 10, 9, 1, 30, 0, 0, time.UTC) // 22:30 on Oct 8 in Fortaleza

	_, err := inv.RecordEntry(ctx, Entry{Type: "Doação", Date: "2026-10-09", Lines: []EntryLine{dipironaLine("A1", "EMS", "03/2027", "1")}}, 1, lateEvening)
	assert.Equal(t, form.Errors{"date": "A data não pode estar no futuro."}, err, "Oct 9 is still tomorrow in Fortaleza")

	_, err = inv.RecordEntry(ctx, Entry{Type: "Doação", Date: "2026-10-08", Lines: []EntryLine{dipironaLine("A1", "EMS", "03/2027", "1")}}, 1, lateEvening)
	assert.NoError(t, err)
}
