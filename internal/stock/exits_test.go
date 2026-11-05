package stock

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/form"
)

// lotIDs maps each Dipirona lot of seedStock to its id: X999 (expired, 20), B777 (12/2026, 150),
// K12 (08/2027, 100).
func lotIDs(t *testing.T, inv *Inventory, medicineID int64) map[string]int64 {
	t.Helper()
	batches, err := inv.AvailableBatches(ctx, medicineID, t0)
	require.NoError(t, err)
	ids := map[string]int64{}
	for _, b := range batches {
		ids[b.Lot] = b.ID
	}
	return ids
}

func takes(pairs map[int64]int64) map[int64]string {
	out := map[int64]string{}
	for id, n := range pairs {
		out[id] = strconv.FormatInt(n, 10)
	}
	return out
}

func dispense(quantity string, lineTakes map[int64]string) Exit {
	return Exit{Type: Dispensing, Date: "2026-10-08", Lines: []ExitLine{
		{Medicine: "Dipirona 500 mg comprimido", Quantity: quantity, Takes: lineTakes},
	}}
}

func TestPrefillFEFO(t *testing.T) {
	batches := []Batch{
		{ID: 1, OnHand: 20, DaysLeft: -8}, // expired
		{ID: 2, OnHand: 150, DaysLeft: 84},
		{ID: 3, OnHand: 100, DaysLeft: 327},
	}

	assert.Equal(t, map[int64]int64{1: 0, 2: 150, 3: 30}, PrefillFEFO(batches, 180, false), "expired lot skipped")
	assert.Equal(t, map[int64]int64{1: 20, 2: 150, 3: 10}, PrefillFEFO(batches, 180, true), "a discard takes the expired lot first")
	assert.Equal(t, map[int64]int64{1: 0, 2: 150, 3: 100}, PrefillFEFO(batches, 999, false), "never more than there is")
	assert.Equal(t, map[int64]int64{1: 0, 2: 0, 3: 0}, PrefillFEFO(batches, 0, false))
}

func TestAvailableBatchesAreInFEFOOrder(t *testing.T) {
	inv := newInventory(t)
	dipironaID, _, cetoconazolID := seedStock(t, inv)

	batches, err := inv.AvailableBatches(ctx, dipironaID, t0)
	require.NoError(t, err)
	var lots []string
	for _, b := range batches {
		lots = append(lots, b.Lot)
	}
	assert.Equal(t, []string{"X999", "B777", "K12"}, lots)

	none, err := inv.AvailableBatches(ctx, cetoconazolID, t0)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestRecordExitTakesFromTheChosenLots(t *testing.T) {
	inv := newInventory(t)
	dipironaID, _, _ := seedStock(t, inv)
	lots := lotIDs(t, inv, dipironaID)

	id, err := inv.RecordExit(ctx, Exit{Type: Dispensing, Date: "2026-10-08", PatientName: " José ", Note: "UBS Centro",
		Lines: []ExitLine{{Medicine: "dipirona 500MG comprimido", Quantity: "160",
			Takes: map[int64]string{lots["X999"]: "0", lots["B777"]: "150", lots["K12"]: "10"}}}}, 1, t0)

	require.NoError(t, err)
	assert.Equal(t, int64(270-160), stockOf(t, inv, dipironaID))
	rows, err := inv.db.Query(`SELECT batch_id, quantity FROM movement_lines WHERE movement_id = ? ORDER BY quantity`, id)
	require.NoError(t, err)
	defer rows.Close()
	got := map[int64]int64{}
	for rows.Next() {
		var batch, qty int64
		require.NoError(t, rows.Scan(&batch, &qty))
		got[batch] = qty
	}
	assert.Equal(t, map[int64]int64{lots["B777"]: -150, lots["K12"]: -10}, got, "lots with 0 get no line")

	name, err := inv.PatientName(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "José", name)
}

func TestPatientNameIsSealedAndBoundToItsMovement(t *testing.T) {
	inv := newInventory(t)
	dipironaID, _, _ := seedStock(t, inv)
	lots := lotIDs(t, inv, dipironaID)
	first, err := inv.RecordExit(ctx, Exit{Type: Dispensing, Date: "2026-10-08", PatientName: "Maria",
		Lines: []ExitLine{{Medicine: "Dipirona 500 mg comprimido", Quantity: "1", Takes: takes(map[int64]int64{lots["K12"]: 1})}}}, 1, t0)
	require.NoError(t, err)
	second, err := inv.RecordExit(ctx, dispense("1", takes(map[int64]int64{lots["K12"]: 1})), 1, t0)
	require.NoError(t, err)

	var stored []byte
	require.NoError(t, inv.db.QueryRow(`SELECT patient_name FROM movements WHERE id = ?`, first).Scan(&stored))
	assert.False(t, bytes.Contains(stored, []byte("Maria")), "not readable in the database file")

	name, err := inv.PatientName(ctx, second)
	require.NoError(t, err)
	assert.Empty(t, name, "no name given, none stored")

	_, err = inv.db.Exec(`UPDATE movements SET patient_name = ? WHERE id = ?`, stored, second)
	require.NoError(t, err)
	_, err = inv.PatientName(ctx, second)
	assert.ErrorContains(t, err, "does not open", "a sealed name copied onto another movement is refused")
}

func TestFirstNameRule(t *testing.T) {
	for name, problem := range map[string]string{
		"":                      "",
		"Maria":                 "",
		"Conceição":             "",
		"Maria Silva":           "Use só o primeiro nome, sem sobrenome.",
		"Maria2":                "Use só letras.",
		"Ana-Paula":             "Use só letras.",
		strings.Repeat("a", 21): "Use no máximo 20 letras.",
	} {
		assert.Equal(t, problem, firstNameProblem(name), name)
	}
}

func TestNameIsOnlyKeptForDispensing(t *testing.T) {
	inv := newInventory(t)
	dipironaID, _, _ := seedStock(t, inv)
	lots := lotIDs(t, inv, dipironaID)

	id, err := inv.RecordExit(ctx, Exit{Type: "Perda", Date: "2026-10-08", PatientName: "Maria Silva 123",
		Lines: []ExitLine{{Medicine: "Dipirona 500 mg comprimido", Quantity: "1", Takes: takes(map[int64]int64{lots["K12"]: 1})}}}, 1, t0)

	require.NoError(t, err, "the hidden field is ignored, not validated")
	name, err := inv.PatientName(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, name)
}

func TestExpiredLotsOnlyLeaveByDiscard(t *testing.T) {
	inv := newInventory(t)
	dipironaID, _, _ := seedStock(t, inv)
	lots := lotIDs(t, inv, dipironaID)
	expired := takes(map[int64]int64{lots["X999"]: 20})

	_, err := inv.RecordExit(ctx, dispense("20", expired), 1, t0)
	assert.Equal(t, form.Errors{"lines.0.take." + strconv.FormatInt(lots["X999"], 10): "Lote vencido só sai por Vencimento/Descarte."}, err)

	discard := dispense("20", expired)
	discard.Type = Discard
	_, err = inv.RecordExit(ctx, discard, 1, t0)
	require.NoError(t, err)
	assert.Equal(t, int64(250), stockOf(t, inv, dipironaID))
}

func TestRecordExitChecks(t *testing.T) {
	inv := newInventory(t)
	dipironaID, _, _ := seedStock(t, inv)
	lots := lotIDs(t, inv, dipironaID)
	b777, k12 := strconv.FormatInt(lots["B777"], 10), strconv.FormatInt(lots["K12"], 10)

	_, err := inv.RecordExit(ctx, Exit{}, 1, t0)
	assert.Equal(t, form.Errors{"type": "Escolha o tipo de saída.", "date": "Informe a data.", "lines.0.medicine": "Adicione pelo menos um medicamento."}, err)

	_, err = inv.RecordExit(ctx, Exit{Type: Dispensing, Date: "2026-10-08", Lines: []ExitLine{
		{Medicine: "Ibuprofeno 600 mg comprimido", Quantity: "1"},
		{Medicine: "Dipirona 500 mg comprimido", Quantity: "10", Takes: map[int64]string{lots["B777"]: "151", lots["K12"]: "-1"}},
		{Medicine: "Dipirona 500 mg comprimido", Quantity: "1"},
		{Medicine: "Cetoconazol 20 mg/g creme", Quantity: "1"},
		{Medicine: "Paracetamol 750 mg comprimido", Quantity: "0"},
	}}, 1, t0)
	assert.Equal(t, form.Errors{
		"lines.0.medicine":     "Escolha um medicamento da lista.",
		"lines.1.take." + b777: "Máximo 150.",
		"lines.1.take." + k12:  "Número inteiro.",
		"lines.2.medicine":     "Este medicamento já está em outra linha.",
		"lines.3.quantity":     "Sem estoque deste medicamento.",
		"lines.4.quantity":     "Quantidade: número inteiro maior que zero.",
	}, err)

	_, err = inv.RecordExit(ctx, dispense("12", takes(map[int64]int64{lots["B777"]: 10})), 1, t0)
	assert.Equal(t, form.Errors{"lines.0.quantity": "Os lotes somam 10, mas a quantidade é 12."}, err)
	assert.Equal(t, int64(270), stockOf(t, inv, dipironaID), "nothing saved")
}

func TestStockNeverGoesBelowZero(t *testing.T) {
	inv := newInventory(t)
	dipironaID, _, _ := seedStock(t, inv)
	lots := lotIDs(t, inv, dipironaID)
	all := dispense("100", takes(map[int64]int64{lots["K12"]: 100}))

	_, err := inv.RecordExit(ctx, all, 1, t0)
	require.NoError(t, err)
	_, err = inv.RecordExit(ctx, all, 1, t0)

	assert.Equal(t, form.Errors{"lines.0.quantity": "Um dos lotes não tem mais estoque. Escolha o medicamento de novo."}, err,
		"the same form sent twice finds K12 empty")
	assert.Equal(t, int64(170), stockOf(t, inv, dipironaID))
}
