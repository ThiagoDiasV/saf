package stock

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"saf/internal/db"
	"saf/internal/form"
)

var (
	ErrMovementNotFound = errors.New("movement not found")
	// ErrNotReversible: only entries and exits are reversed, never a reversal.
	ErrNotReversible   = errors.New("movement cannot be reversed")
	ErrAlreadyReversed = errors.New("movement already reversed")
)

// HistoryFilter narrows Histórico. Dates are "AAAA-MM-DD" in the pharmacy's time zone, inclusive.
type HistoryFilter struct {
	From, To    string
	Kind        string // "", "in", "out", "reversal" or "adjust"
	Type        string // with Kind "in" or "out": one entry or exit type
	Medicine    string // matched against the label; case, accents and spaces don't count
	Lot         string
	PatientName string
}

// HistoryItem is one row of Histórico: a movement, or an "Ajuste" (catalog or lot correction).
type HistoryItem struct {
	Kind       string    // "in", "out", "reversal" or "adjust"
	OccurredOn string    // "AAAA-MM-DD": the movement date, or the day of the correction
	RecordedAt time.Time // when it was saved
	UserName   string

	// Movements
	MovementID   int64
	Type         string
	Note         string
	PatientName  string // decrypted; "" when none or deleted after 6 months
	Lines        []HistoryLine
	PhotoCount   int    // photos still attached
	ReversedByID int64  // the estorno of this movement, 0 if none
	ReversesID   int64  // for an estorno: the movement it undoes
	ReversesKind string // "in" or "out"
	ReversesOn   string // its date

	// Ajustes
	Subject string // "Dipirona 500 mg comprimido" or "Dipirona 500 mg comprimido, lote K12"
	Changes []Change

	subjectMedicine, subjectLot string // what an Ajuste is about, for the filters
}

// HistoryLine is one lot moved; Quantity is signed: + into stock, − out of it.
type HistoryLine struct {
	Medicine     string
	Unit         string
	Lot          string
	Manufacturer string
	Expiry       string // "MM/AAAA"
	Quantity     int64
	BatchID      int64
}

// Change is one field corrected: old → new.
type Change struct {
	Field, Old, New string
	ChangedAt       time.Time
	UserName        string
}

// History lists movements and corrections, most recent first.
func (inv *Inventory) History(ctx context.Context, f HistoryFilter) ([]HistoryItem, error) {
	var items []HistoryItem
	if f.Kind != "adjust" {
		movements, err := inv.movements(ctx, f, 0)
		if err != nil {
			return nil, err
		}
		items = append(items, movements...)
	}
	if f.Kind == "" || f.Kind == "adjust" {
		adjustments, err := inv.adjustments(ctx, f)
		if err != nil {
			return nil, err
		}
		items = append(items, adjustments...)
	}

	medicine, lot, name := fold(f.Medicine), fold(f.Lot), fold(f.PatientName)
	items = slices.DeleteFunc(items, func(it HistoryItem) bool {
		if name != "" && !strings.Contains(fold(it.PatientName), name) {
			return true
		}
		if medicine == "" && lot == "" {
			return false
		}
		if it.Kind == "adjust" {
			return !strings.Contains(fold(it.subjectMedicine), medicine) || (lot != "" && !strings.Contains(fold(it.subjectLot), lot))
		}
		return !slices.ContainsFunc(it.Lines, func(l HistoryLine) bool {
			return strings.Contains(fold(l.Medicine), medicine) && strings.Contains(fold(l.Lot), lot)
		})
	})
	// Times have one-second precision; two saves in the same second fall back to save order (ids).
	slices.SortStableFunc(items, func(a, b HistoryItem) int {
		return cmp.Or(cmp.Compare(b.OccurredOn, a.OccurredOn), b.RecordedAt.Compare(a.RecordedAt), cmp.Compare(b.MovementID, a.MovementID))
	})
	return items, nil
}

// movements reads movements (one, when id is set) with their lines: two queries, however many rows.
func (inv *Inventory) movements(ctx context.Context, f HistoryFilter, id int64) ([]HistoryItem, error) {
	var where []string
	var args []any
	switch {
	case id != 0:
		where, args = append(where, "m.id = ?"), append(args, id)
	default:
		if f.From != "" {
			where, args = append(where, "m.occurred_on >= ?"), append(args, f.From)
		}
		if f.To != "" {
			where, args = append(where, "m.occurred_on <= ?"), append(args, f.To)
		}
		if f.Kind != "" {
			where, args = append(where, "m.kind = ?"), append(args, f.Kind)
		}
		if f.Type != "" {
			where, args = append(where, "m.type = ?"), append(args, f.Type)
		}
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}

	rows, err := inv.db.QueryContext(ctx, `
		SELECT m.id, m.kind, m.type, m.occurred_on, m.recorded_at, m.note, m.patient_name, u.name,
			(SELECT COUNT(*) FROM photos p WHERE p.movement_id = m.id AND p.removed_at IS NULL),
			COALESCE(r.id, 0), COALESCE(o.id, 0), COALESCE(o.kind, ''), COALESCE(o.occurred_on, '')
		FROM movements m
		JOIN users u ON u.id = m.user_id
		LEFT JOIN movements r ON r.reverses_id = m.id
		LEFT JOIN movements o ON o.id = m.reverses_id `+clause, args...)
	if err != nil {
		return nil, fmt.Errorf("list movements: %w", err)
	}
	defer rows.Close()
	var items []HistoryItem
	index := map[int64]int{}
	for rows.Next() {
		var it HistoryItem
		var recorded string
		var sealedName []byte
		if err := rows.Scan(&it.MovementID, &it.Kind, &it.Type, &it.OccurredOn, &recorded, &it.Note, &sealedName, &it.UserName,
			&it.PhotoCount, &it.ReversedByID, &it.ReversesID, &it.ReversesKind, &it.ReversesOn); err != nil {
			return nil, err
		}
		if it.RecordedAt, err = db.ParseTime(recorded); err != nil {
			return nil, fmt.Errorf("movement %d recorded_at: %w", it.MovementID, err)
		}
		if sealedName != nil {
			name, err := inv.box.Open(sealedName, patientNameContext(it.MovementID))
			if err != nil {
				return nil, fmt.Errorf("open patient name of movement %d: %w", it.MovementID, err)
			}
			it.PatientName = string(name)
		}
		index[it.MovementID] = len(items)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	lineRows, err := inv.db.QueryContext(ctx, `
		SELECT l.movement_id, l.quantity, b.id, b.lot, b.manufacturer, b.expiry_month,
			med.active_ingredient, med.strength, med.dosage_form, med.unit
		FROM movement_lines l
		JOIN movements m ON m.id = l.movement_id
		JOIN batches b ON b.id = l.batch_id
		JOIN medicines med ON med.id = b.medicine_id `+clause+`
		ORDER BY l.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list movement lines: %w", err)
	}
	defer lineRows.Close()
	for lineRows.Next() {
		var movementID int64
		var l HistoryLine
		var expiry string
		var m Medicine
		if err := lineRows.Scan(&movementID, &l.Quantity, &l.BatchID, &l.Lot, &l.Manufacturer, &expiry,
			&m.Ingredient, &m.Strength, &m.DosageForm, &m.Unit); err != nil {
			return nil, err
		}
		l.Expiry, l.Medicine, l.Unit = FormatExpiry(expiry), m.Label(), m.Unit
		if i, ok := index[movementID]; ok {
			items[i].Lines = append(items[i].Lines, l)
		}
	}
	return items, lineRows.Err()
}

// adjustments reads catalog and lot corrections. Rows saved together (same subject, user and
// second) are one "Ajuste".
func (inv *Inventory) adjustments(ctx context.Context, f HistoryFilter) ([]HistoryItem, error) {
	var where []string
	var args []any
	if f.From != "" {
		where, args = append(where, "c.changed_at >= ?"), append(args, startOfDayUTC(f.From, 0))
	}
	if f.To != "" {
		where, args = append(where, "c.changed_at < ?"), append(args, startOfDayUTC(f.To, 1))
	}
	clause := "WHERE c.movement_id IS NULL"
	if len(where) > 0 {
		clause += " AND " + strings.Join(where, " AND ")
	}
	rows, err := inv.db.QueryContext(ctx, `
		SELECT COALESCE(c.medicine_id, b.medicine_id), COALESCE(c.batch_id, 0), COALESCE(b.lot, ''), c.user_id, u.name,
			c.field, c.old_value, c.new_value, c.changed_at,
			med.active_ingredient, med.strength, med.dosage_form
		FROM changes c
		JOIN users u ON u.id = c.user_id
		LEFT JOIN batches b ON b.id = c.batch_id
		JOIN medicines med ON med.id = COALESCE(c.medicine_id, b.medicine_id) `+clause+`
		ORDER BY c.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list adjustments: %w", err)
	}
	defer rows.Close()
	type groupKey struct {
		medicineID, batchID, userID int64
		at                          string
	}
	var items []HistoryItem
	index := map[groupKey]int{}
	for rows.Next() {
		var k groupKey
		var lot string
		var c Change
		var m Medicine
		if err := rows.Scan(&k.medicineID, &k.batchID, &lot, &k.userID, &c.UserName, &c.Field, &c.Old, &c.New, &k.at,
			&m.Ingredient, &m.Strength, &m.DosageForm); err != nil {
			return nil, err
		}
		if c.ChangedAt, err = db.ParseTime(k.at); err != nil {
			return nil, fmt.Errorf("change changed_at: %w", err)
		}
		i, seen := index[k]
		if !seen {
			it := HistoryItem{Kind: "adjust", Type: "Medicamento", Subject: m.Label(), subjectMedicine: m.Label(),
				UserName: c.UserName, RecordedAt: c.ChangedAt, OccurredOn: c.ChangedAt.In(TimeZone).Format("2006-01-02")}
			if k.batchID != 0 {
				it.Type, it.Subject, it.subjectLot = "Lote", m.Label()+", lote "+lot, lot
			}
			i = len(items)
			index[k] = i
			items = append(items, it)
		}
		items[i].Changes = append(items[i].Changes, c)
	}
	return items, rows.Err()
}

// startOfDayUTC is midnight in the pharmacy, days after date ("AAAA-MM-DD"), as a stored timestamp.
func startOfDayUTC(date string, days int) string {
	d, err := time.ParseInLocation("2006-01-02", date, TimeZone)
	if err != nil {
		return ""
	}
	return db.FormatTime(d.AddDate(0, 0, days))
}

// MovementDetail is one movement with its edit log, for the detail page.
type MovementDetail struct {
	HistoryItem
	Log    []Change // corrections to the movement itself (observação), oldest first
	Photos []Photo  // removed ones included, for the log
	// ReverseBlockers lists lots that no longer hold the units an estorno of this entry would take
	// back out; empty when it can be reversed.
	ReverseBlockers []ReverseBlocker
}

// ReverseBlocker is a lot whose stock is too low for the estorno to take its units back.
type ReverseBlocker struct {
	Medicine string
	Unit     string
	Lot      string
	OnHand   int64
	Needed   int64
}

// Reversible says whether the movement can still get an estorno.
func (d MovementDetail) Reversible() bool {
	return d.Kind != "reversal" && d.ReversedByID == 0 && len(d.ReverseBlockers) == 0
}

// Movement returns one movement with its log, or ErrMovementNotFound.
func (inv *Inventory) Movement(ctx context.Context, id int64) (MovementDetail, error) {
	items, err := inv.movements(ctx, HistoryFilter{}, id)
	if err != nil {
		return MovementDetail{}, err
	}
	if len(items) == 0 {
		return MovementDetail{}, ErrMovementNotFound
	}
	d := MovementDetail{HistoryItem: items[0]}
	rows, err := inv.db.QueryContext(ctx, `
		SELECT c.field, c.old_value, c.new_value, c.changed_at, u.name
		FROM changes c JOIN users u ON u.id = c.user_id WHERE c.movement_id = ? ORDER BY c.id`, id)
	if err != nil {
		return MovementDetail{}, fmt.Errorf("load log of movement %d: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var c Change
		var at string
		if err := rows.Scan(&c.Field, &c.Old, &c.New, &at, &c.UserName); err != nil {
			return MovementDetail{}, err
		}
		if c.ChangedAt, err = db.ParseTime(at); err != nil {
			return MovementDetail{}, err
		}
		d.Log = append(d.Log, c)
	}
	if err := rows.Err(); err != nil {
		return MovementDetail{}, err
	}
	if d.Photos, err = inv.Photos(ctx, id); err != nil {
		return MovementDetail{}, err
	}
	if d.Kind != "reversal" && d.ReversedByID == 0 {
		if d.ReverseBlockers, err = reverseBlockers(ctx, inv.db, d.Lines); err != nil {
			return MovementDetail{}, err
		}
	}
	return d, nil
}

// reverseBlockers finds lots that would go below zero if lines were undone: an entry whose units
// already left. Undoing an exit only puts units back, so it never blocks.
func reverseBlockers(ctx context.Context, q querier, lines []HistoryLine) ([]ReverseBlocker, error) {
	var blockers []ReverseBlocker
	for _, l := range lines {
		if l.Quantity <= 0 {
			continue
		}
		var onHand int64
		err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(quantity), 0) FROM movement_lines WHERE batch_id = ?`, l.BatchID).Scan(&onHand)
		if err != nil {
			return nil, fmt.Errorf("stock of batch %d: %w", l.BatchID, err)
		}
		if onHand < l.Quantity {
			blockers = append(blockers, ReverseBlocker{Medicine: l.Medicine, Unit: l.Unit, Lot: l.Lot, OnHand: onHand, Needed: l.Quantity})
		}
	}
	return blockers, nil
}

// ReverseBlockedError carries the lots that stop an estorno.
type ReverseBlockedError struct{ Blockers []ReverseBlocker }

func (e ReverseBlockedError) Error() string {
	return fmt.Sprintf("estorno blocked by %d lots without enough stock", len(e.Blockers))
}

// Reverse records the estorno of an entry or exit: a new movement, dated today, with every line
// negated. The original stays in the history, marked as reversed. It returns the estorno's id.
func (inv *Inventory) Reverse(ctx context.Context, id, userID int64, now time.Time) (int64, error) {
	var reversalID int64
	err := inv.inTx(ctx, func(tx *sql.Tx) error {
		var kind string
		var reversedBy int64
		err := tx.QueryRowContext(ctx, `
			SELECT m.kind, COALESCE((SELECT r.id FROM movements r WHERE r.reverses_id = m.id), 0)
			FROM movements m WHERE m.id = ?`, id).Scan(&kind, &reversedBy)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrMovementNotFound
		case err != nil:
			return fmt.Errorf("load movement %d: %w", id, err)
		case kind == "reversal":
			return ErrNotReversible
		case reversedBy != 0:
			return ErrAlreadyReversed
		}
		lines, err := movementLines(ctx, tx, id)
		if err != nil {
			return err
		}
		blockers, err := reverseBlockers(ctx, tx, lines)
		if err != nil {
			return err
		}
		if len(blockers) > 0 {
			return ReverseBlockedError{Blockers: blockers}
		}
		what := map[string]string{"in": "Estorno de entrada", "out": "Estorno de saída"}[kind]
		res, err := tx.ExecContext(ctx, `
			INSERT INTO movements (kind, type, occurred_on, recorded_at, user_id, reverses_id) VALUES ('reversal', ?, ?, ?, ?, ?)`,
			what, now.In(TimeZone).Format("2006-01-02"), db.FormatTime(now), userID, id)
		if err != nil {
			return fmt.Errorf("insert estorno of %d: %w", id, err)
		}
		if reversalID, err = res.LastInsertId(); err != nil {
			return err
		}
		for _, l := range lines {
			_, err := tx.ExecContext(ctx, `INSERT INTO movement_lines (movement_id, batch_id, quantity) VALUES (?, ?, ?)`,
				reversalID, l.BatchID, -l.Quantity)
			if err != nil {
				return fmt.Errorf("insert estorno line: %w", err)
			}
		}
		return nil
	})
	return reversalID, err
}

func movementLines(ctx context.Context, q querier, movementID int64) ([]HistoryLine, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT l.batch_id, l.quantity, b.lot, med.active_ingredient, med.strength, med.dosage_form, med.unit
		FROM movement_lines l JOIN batches b ON b.id = l.batch_id JOIN medicines med ON med.id = b.medicine_id
		WHERE l.movement_id = ? ORDER BY l.id`, movementID)
	if err != nil {
		return nil, fmt.Errorf("lines of movement %d: %w", movementID, err)
	}
	defer rows.Close()
	var lines []HistoryLine
	for rows.Next() {
		var l HistoryLine
		var m Medicine
		if err := rows.Scan(&l.BatchID, &l.Quantity, &l.Lot, &m.Ingredient, &m.Strength, &m.DosageForm, &m.Unit); err != nil {
			return nil, err
		}
		l.Medicine, l.Unit = m.Label(), m.Unit
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// UpdateNote corrects a movement's observação, logging old → new in the movement's own log.
// It reports whether anything changed.
func (inv *Inventory) UpdateNote(ctx context.Context, id int64, note string, userID int64, now time.Time) (bool, error) {
	var current, typ string
	err := inv.db.QueryRowContext(ctx, `SELECT note, type FROM movements WHERE id = ?`, id).Scan(&current, &typ)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrMovementNotFound
	}
	if err != nil {
		return false, fmt.Errorf("load note of movement %d: %w", id, err)
	}
	note = strings.TrimSpace(note)
	switch {
	case typ == "Outros" && note == "":
		return false, form.Errors{"note": "Descreva a entrada quando o tipo é Outros."}
	case utf8.RuneCountInString(note) > maxNoteRunes:
		return false, form.Errors{"note": fmt.Sprintf("Use no máximo %d caracteres.", maxNoteRunes)}
	case note == current:
		return false, nil
	}
	err = inv.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE movements SET note = ? WHERE id = ?`, note, id); err != nil {
			return fmt.Errorf("update note of movement %d: %w", id, err)
		}
		return logChanges(ctx, tx, "movement_id", id, []fieldChange{{"Observação", current, note}}, userID, now)
	})
	return err == nil, err
}
