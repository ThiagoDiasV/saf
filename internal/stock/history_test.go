package stock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/form"
)

// seedHistory: seedStock's Saldo inicial (dated 2026-10-01, saved at t0), a Dispensação to Maria
// saved an hour later, and a lot correction (K12 → K13, expiry too) two hours later.
func seedHistory(t *testing.T, inv *Inventory) (entryID, exitID int64) {
	t.Helper()
	dipironaID, _, _ := seedStock(t, inv)
	require.NoError(t, inv.db.QueryRow(`SELECT id FROM movements WHERE type = 'Saldo inicial'`).Scan(&entryID))
	lots := lotIDs(t, inv, dipironaID)
	exitID, err := inv.RecordExit(ctx, Exit{Type: Dispensing, Date: "2026-10-08", PatientName: "Maria", Note: "UBS Centro",
		Lines: []ExitLine{{Medicine: "Dipirona 500 mg comprimido", Quantity: "30", Takes: takes(map[int64]int64{lots["B777"]: 30})}}}, 1, t0.Add(time.Hour))
	require.NoError(t, err)
	_, err = inv.UpdateBatch(ctx, lots["K12"], BatchInput{Lot: "K13", Manufacturer: "Prati-Donaduzzi", Expiry: "09/2027"}, 1, t0.Add(2*time.Hour))
	require.NoError(t, err)
	return entryID, exitID
}

func kindsOf(items []HistoryItem) []string {
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.Kind+":"+it.Type)
	}
	return kinds
}

func TestHistoryListsMovementsAndAdjustments(t *testing.T) {
	inv := newInventory(t)
	entryID, exitID := seedHistory(t, inv)

	items, err := inv.History(ctx, HistoryFilter{})

	require.NoError(t, err)
	assert.Equal(t, []string{"adjust:Lote", "out:Dispensação", "adjust:Medicamento", "in:Saldo inicial"}, kindsOf(items),
		"newest first: the lot fix, the exit, archiving Captopril, then the backdated entry")
	lotFix := items[0]
	assert.Equal(t, "Dipirona 500 mg comprimido, lote K13", lotFix.Subject)
	assert.Equal(t, []string{"Lote: K12 → K13", "Validade: 08/2027 → 09/2027"},
		[]string{lotFix.Changes[0].Field + ": " + lotFix.Changes[0].Old + " → " + lotFix.Changes[0].New,
			lotFix.Changes[1].Field + ": " + lotFix.Changes[1].Old + " → " + lotFix.Changes[1].New}, "one save, one Ajuste")
	exit := items[1]
	assert.Equal(t, exitID, exit.MovementID)
	assert.Equal(t, "Maria", exit.PatientName, "decrypted for the list")
	assert.Equal(t, "Paulo Mendes", exit.UserName)
	require.Len(t, exit.Lines, 1)
	assert.Equal(t, HistoryLine{Medicine: "Dipirona 500 mg comprimido", Unit: "comprimido", Lot: "B777", Manufacturer: "Medley",
		Expiry: "12/2026", Quantity: -30, BatchID: exit.Lines[0].BatchID}, exit.Lines[0])
	entry := items[3]
	assert.Equal(t, entryID, entry.MovementID)
	assert.Len(t, entry.Lines, 4)
	assert.Equal(t, "2026-10-01", entry.OccurredOn)
	assert.True(t, entry.RecordedAt.Equal(t0), "backdated: recorded later than its date")
}

func TestHistoryFilters(t *testing.T) {
	inv := newInventory(t)
	seedHistory(t, inv)
	kinds := func(f HistoryFilter) []string {
		items, err := inv.History(ctx, f)
		require.NoError(t, err)
		return kindsOf(items)
	}

	assert.Equal(t, []string{"in:Saldo inicial"}, kinds(HistoryFilter{From: "2026-09-01", To: "2026-10-07"}))
	assert.Equal(t, []string{"adjust:Lote", "out:Dispensação", "adjust:Medicamento"}, kinds(HistoryFilter{From: "2026-10-08", To: "2026-10-08"}))
	assert.Equal(t, []string{"out:Dispensação"}, kinds(HistoryFilter{Kind: "out"}))
	assert.Equal(t, []string{"in:Saldo inicial"}, kinds(HistoryFilter{Kind: "in", Type: "Saldo inicial"}))
	assert.Empty(t, kinds(HistoryFilter{Kind: "in", Type: "Doação"}))
	assert.Equal(t, []string{"adjust:Lote", "adjust:Medicamento"}, kinds(HistoryFilter{Kind: "adjust"}))
	assert.Equal(t, []string{"out:Dispensação"}, kinds(HistoryFilter{PatientName: "MARIA"}))
	assert.Equal(t, []string{"in:Saldo inicial"}, kinds(HistoryFilter{Medicine: "paracetamol"}))
	assert.Equal(t, []string{"adjust:Lote", "out:Dispensação", "in:Saldo inicial"}, kinds(HistoryFilter{Medicine: "dipirona"}))
	assert.Equal(t, []string{"out:Dispensação", "in:Saldo inicial"}, kinds(HistoryFilter{Lot: "b777"}))
	assert.Equal(t, []string{"adjust:Lote", "in:Saldo inicial"}, kinds(HistoryFilter{Lot: "k13"}), "lots go by their current name")
	assert.Equal(t, []string{"adjust:Medicamento"}, kinds(HistoryFilter{Medicine: "captopril"}))
}

func TestMovementsSavedInTheSameSecondListNewestFirst(t *testing.T) {
	inv := newInventory(t)
	createMedicine(t, inv, dipirona)
	var ids []int64
	for _, lot := range []string{"A1", "A2", "A3"} {
		id, err := inv.RecordEntry(ctx, entryOf(dipironaLine(lot, "EMS", "03/2027", "1")), 1, t0)
		require.NoError(t, err)
		ids = append(ids, id)
	}

	items, err := inv.History(ctx, HistoryFilter{})

	require.NoError(t, err)
	assert.Equal(t, []int64{ids[2], ids[1], ids[0]}, []int64{items[0].MovementID, items[1].MovementID, items[2].MovementID})
}

func TestHistoryDayFollowsThePharmacyClock(t *testing.T) {
	inv := newInventory(t)
	id := createMedicine(t, inv, dipirona)
	lateEvening := time.Date(2026, 10, 9, 1, 30, 0, 0, time.UTC) // still Oct 8 in Fortaleza
	_, err := inv.UpdateMedicine(ctx, id, MedicineInput{Ingredient: "Dipirona sódica", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"}, 1, lateEvening)
	require.NoError(t, err)

	items, err := inv.History(ctx, HistoryFilter{From: "2026-10-08", To: "2026-10-08"})

	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "2026-10-08", items[0].OccurredOn)
}

func TestReverseAnExitPutsUnitsBack(t *testing.T) {
	inv := newInventory(t)
	_, exitID := seedHistory(t, inv)
	later := t0.Add(48 * time.Hour)

	reversalID, err := inv.Reverse(ctx, exitID, 1, later)

	require.NoError(t, err)
	var dipirona int64
	require.NoError(t, inv.db.QueryRow(`SELECT id FROM medicines WHERE active_ingredient = 'Dipirona'`).Scan(&dipirona))
	assert.Equal(t, int64(270), stockOf(t, inv, dipirona))
	reversal, err := inv.Movement(ctx, reversalID)
	require.NoError(t, err)
	assert.Equal(t, "reversal", reversal.Kind)
	assert.Equal(t, "Estorno de saída", reversal.Type)
	assert.Equal(t, "2026-10-10", reversal.OccurredOn, "dated the day it was done")
	assert.Equal(t, exitID, reversal.ReversesID)
	assert.Equal(t, "out", reversal.ReversesKind)
	assert.Equal(t, int64(30), reversal.Lines[0].Quantity)
	assert.False(t, reversal.Reversible(), "an estorno is never reversed")

	original, err := inv.Movement(ctx, exitID)
	require.NoError(t, err)
	assert.Equal(t, reversalID, original.ReversedByID)
	assert.False(t, original.Reversible())

	_, err = inv.Reverse(ctx, exitID, 1, later)
	assert.ErrorIs(t, err, ErrAlreadyReversed)
	_, err = inv.Reverse(ctx, reversalID, 1, later)
	assert.ErrorIs(t, err, ErrNotReversible)
	_, err = inv.Reverse(ctx, 999, 1, later)
	assert.ErrorIs(t, err, ErrMovementNotFound)
}

func TestReverseAnEntryWhoseUnitsLeftIsBlocked(t *testing.T) {
	inv := newInventory(t)
	entryID, _ := seedHistory(t, inv) // 30 of B777's 150 already dispensed

	detail, err := inv.Movement(ctx, entryID)
	require.NoError(t, err)
	assert.False(t, detail.Reversible())
	assert.Equal(t, []ReverseBlocker{{Medicine: "Dipirona 500 mg comprimido", Unit: "comprimido", Lot: "B777", OnHand: 120, Needed: 150}},
		detail.ReverseBlockers)

	_, err = inv.Reverse(ctx, entryID, 1, t0)
	var blocked ReverseBlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Equal(t, detail.ReverseBlockers, blocked.Blockers)
	assert.Equal(t, 0, countRows(t, inv, "movements WHERE kind = 'reversal'"))
}

func TestUpdateNote(t *testing.T) {
	inv := newInventory(t)
	_, exitID := seedHistory(t, inv)

	changed, err := inv.UpdateNote(ctx, exitID, "  UBS Centro, receita 123 ", 1, t0)
	require.NoError(t, err)
	assert.True(t, changed)
	changed, err = inv.UpdateNote(ctx, exitID, "UBS Centro, receita 123", 1, t0)
	require.NoError(t, err)
	assert.False(t, changed)

	detail, err := inv.Movement(ctx, exitID)
	require.NoError(t, err)
	assert.Equal(t, "UBS Centro, receita 123", detail.Note)
	require.Len(t, detail.Log, 1)
	assert.Equal(t, Change{Field: "Observação", Old: "UBS Centro", New: "UBS Centro, receita 123", ChangedAt: t0, UserName: "Paulo Mendes"},
		detail.Log[0])

	items, err := inv.History(ctx, HistoryFilter{Kind: "adjust"})
	require.NoError(t, err)
	assert.Len(t, items, 2, "note edits live in the movement's own log, not as Ajustes")

	other, err := inv.RecordEntry(ctx, Entry{Type: "Outros", Date: "2026-10-08", Note: "Achado no depósito",
		Lines: []EntryLine{dipironaLine("Z1", "EMS", "01/2028", "1")}}, 1, t0)
	require.NoError(t, err)
	_, err = inv.UpdateNote(ctx, other, " ", 1, t0)
	assert.Equal(t, form.Errors{"note": "Descreva a entrada quando o tipo é Outros."}, err)
	_, err = inv.UpdateNote(ctx, 999, "x", 1, t0)
	assert.ErrorIs(t, err, ErrMovementNotFound)
}
