package stock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // the container image has no time zone files

	"saf/internal/db"
	"saf/internal/form"
)

// TimeZone is the pharmacy's: "today", expiry and movement dates follow it.
var TimeZone = mustLoadLocation("America/Fortaleza")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err) // time/tzdata is compiled in
	}
	return loc
}

// ExpiringDays: a lot within this many days of its expiry is flagged "vencendo".
const ExpiringDays = 90

var ErrBatchNotFound = errors.New("batch not found")

var expiryPattern = regexp.MustCompile(`^(0[1-9]|1[0-2])/(20\d\d)$`)

// parseExpiry reads "MM/AAAA" as typed and returns the stored "AAAA-MM".
func parseExpiry(text string) (string, bool) {
	m := expiryPattern.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", false
	}
	return m[2] + "-" + m[1], true
}

// FormatExpiry turns the stored "AAAA-MM" into "MM/AAAA".
func FormatExpiry(month string) string {
	year, mm, _ := strings.Cut(month, "-")
	return mm + "/" + year
}

// daysToExpiry counts days from today until the last day of the expiry month, when the lot is
// still good. Negative means expired.
func daysToExpiry(month string, now time.Time) int {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		panic(fmt.Sprintf("stored expiry_month %q is not AAAA-MM", month))
	}
	lastDay := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC)
	local := now.In(TimeZone)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	return int(lastDay.Sub(today).Hours() / 24)
}

// Batch is a lot of one medicine in stock: medicine + lot + manufacturer, with one expiry.
type Batch struct {
	ID           int64
	MedicineID   int64
	Lot          string
	Manufacturer string
	ExpiryMonth  string // "2027-03", valid until the last day of that month
	OnHand       int64
	DaysLeft     int // days until expiry, from the moment it was read; negative = expired
}

func (b Batch) Expiry() string { return FormatExpiry(b.ExpiryMonth) }
func (b Batch) Expired() bool  { return b.DaysLeft < 0 }
func (b Batch) Expiring() bool { return b.DaysLeft >= 0 && b.DaysLeft <= ExpiringDays }

func batchKey(lot, manufacturer string) string {
	return fold(lot) + "|" + fold(manufacturer)
}

// Batch returns one lot with its stock, or ErrBatchNotFound.
func (inv *Inventory) Batch(ctx context.Context, id int64, now time.Time) (Batch, error) {
	var b Batch
	err := inv.db.QueryRowContext(ctx, `
		SELECT b.id, b.medicine_id, b.lot, b.manufacturer, b.expiry_month,
			COALESCE((SELECT SUM(quantity) FROM movement_lines WHERE batch_id = b.id), 0)
		FROM batches b WHERE b.id = ?`, id).
		Scan(&b.ID, &b.MedicineID, &b.Lot, &b.Manufacturer, &b.ExpiryMonth, &b.OnHand)
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{}, ErrBatchNotFound
	}
	if err != nil {
		return Batch{}, fmt.Errorf("load batch %d: %w", id, err)
	}
	b.DaysLeft = daysToExpiry(b.ExpiryMonth, now)
	return b, nil
}

// BatchInput is what the lot form sends, as typed.
type BatchInput struct {
	Lot          string
	Manufacturer string
	Expiry       string // "MM/AAAA"
}

// UpdateBatch corrects a lot's details (typos). Each changed field is logged old → new.
// It reports whether anything changed.
func (inv *Inventory) UpdateBatch(ctx context.Context, id int64, in BatchInput, userID int64, now time.Time) (bool, error) {
	current, err := inv.Batch(ctx, id, now)
	if err != nil {
		return false, err
	}
	lot, manufacturer := clean(in.Lot), clean(in.Manufacturer)
	expiry, expiryOK := parseExpiry(in.Expiry)
	errs := form.Errors{}
	if lot == "" {
		errs["lot"] = "Informe o lote."
	}
	if manufacturer == "" {
		errs["manufacturer"] = "Informe o fabricante."
	}
	if !expiryOK {
		errs["expiry"] = "Use o formato MM/AAAA."
	}
	if len(errs) == 0 {
		var clashLot, clashManufacturer string
		err := inv.db.QueryRowContext(ctx, `SELECT lot, manufacturer FROM batches WHERE medicine_id = ? AND match_key = ? AND id <> ?`,
			current.MedicineID, batchKey(lot, manufacturer), id).Scan(&clashLot, &clashManufacturer)
		if err == nil {
			errs["lot"] = fmt.Sprintf("Já existe o lote %s (%s) deste medicamento.", clashLot, clashManufacturer)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("look for lot clash: %w", err)
		}
	}
	if len(errs) > 0 {
		return false, errs
	}

	var changed []fieldChange
	for _, f := range []fieldChange{
		{"Lote", current.Lot, lot},
		{"Fabricante", current.Manufacturer, manufacturer},
		{"Validade", current.Expiry(), FormatExpiry(expiry)},
	} {
		if f.old != f.new {
			changed = append(changed, f)
		}
	}
	if len(changed) == 0 {
		return false, nil
	}
	err = inv.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE batches SET lot = ?, manufacturer = ?, expiry_month = ?, match_key = ? WHERE id = ?`,
			lot, manufacturer, expiry, batchKey(lot, manufacturer), id)
		if err != nil {
			return fmt.Errorf("update batch %d: %w", id, err)
		}
		return logChanges(ctx, tx, "batch_id", id, changed, userID, now)
	})
	return err == nil, err
}

// StockGroup is one medicine on the Estoque screen with the lots that are shown.
type StockGroup struct {
	Medicine        Medicine
	Batches         []Batch // lots with stock that match the filter, closest expiry first
	Total           int64   // all its lots, filter or not
	ExpiredUnits    int64
	ExpiredBatches  int
	ExpiringBatches int
	BatchCount      int
}

// StockCounts summarizes all active medicines, whatever the filter.
type StockCounts struct {
	Medicines int
	Batches   int
	Expiring  int
	Expired   int
}

// StockFilter narrows the Estoque screen.
type StockFilter struct {
	Query    string // medicine, lot or manufacturer; case, accents and spaces don't count
	Validity string // "", "vencendo" or "vencidos"
}

// Filtering reports whether the filter hides anything, in which case every group shows opened.
func (f StockFilter) Filtering() bool { return fold(f.Query) != "" || f.Validity != "" }

// StockGroups returns the active medicines with their lots in stock, for the Estoque screen.
func (inv *Inventory) StockGroups(ctx context.Context, filter StockFilter, now time.Time) ([]StockGroup, StockCounts, error) {
	medicines, err := inv.Medicines(ctx, MedicineFilter{})
	if err != nil {
		return nil, StockCounts{}, err
	}
	byMedicine, err := batchesInStock(ctx, inv.db, now, 0)
	if err != nil {
		return nil, StockCounts{}, err
	}

	q := fold(filter.Query)
	counts := StockCounts{Medicines: len(medicines)}
	var groups []StockGroup
	for _, m := range medicines {
		batches := byMedicine[m.ID]
		g := StockGroup{Medicine: m, BatchCount: len(batches)}
		for _, b := range batches {
			g.Total += b.OnHand
			switch {
			case b.Expired():
				g.ExpiredBatches++
				g.ExpiredUnits += b.OnHand
			case b.Expiring():
				g.ExpiringBatches++
			}
		}
		counts.Batches += len(batches)
		counts.Expired += g.ExpiredBatches
		counts.Expiring += g.ExpiringBatches

		medicineMatches := strings.Contains(fold(m.Label()), q)
		for _, b := range batches {
			textMatches := medicineMatches || strings.Contains(fold(b.Lot), q) || strings.Contains(fold(b.Manufacturer), q)
			validityMatches := filter.Validity == "" ||
				(filter.Validity == "vencidos" && b.Expired()) || (filter.Validity == "vencendo" && b.Expiring())
			if textMatches && validityMatches {
				g.Batches = append(g.Batches, b)
			}
		}
		if len(g.Batches) > 0 || (filter.Validity == "" && medicineMatches) {
			groups = append(groups, g)
		}
	}
	return groups, counts, nil
}

// querier is *sql.DB or *sql.Tx, so the same read works inside a saving transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// batchesInStock returns lots with stock by medicine, closest expiry first (FEFO order).
// medicineID 0 means every medicine.
func batchesInStock(ctx context.Context, q querier, now time.Time, medicineID int64) (map[int64][]Batch, error) {
	where, args := "", []any{}
	if medicineID != 0 {
		where, args = "WHERE b.medicine_id = ?", []any{medicineID}
	}
	rows, err := q.QueryContext(ctx, `
		SELECT b.id, b.medicine_id, b.lot, b.manufacturer, b.expiry_month, SUM(l.quantity)
		FROM batches b JOIN movement_lines l ON l.batch_id = b.id `+where+`
		GROUP BY b.id HAVING SUM(l.quantity) > 0
		ORDER BY b.expiry_month, b.lot`, args...)
	if err != nil {
		return nil, fmt.Errorf("list batches in stock: %w", err)
	}
	defer rows.Close()
	byMedicine := map[int64][]Batch{}
	for rows.Next() {
		var b Batch
		if err := rows.Scan(&b.ID, &b.MedicineID, &b.Lot, &b.Manufacturer, &b.ExpiryMonth, &b.OnHand); err != nil {
			return nil, err
		}
		b.DaysLeft = daysToExpiry(b.ExpiryMonth, now)
		byMedicine[b.MedicineID] = append(byMedicine[b.MedicineID], b)
	}
	return byMedicine, rows.Err()
}

// AvailableBatches returns a medicine's lots with stock, closest expiry first (FEFO order).
func (inv *Inventory) AvailableBatches(ctx context.Context, medicineID int64, now time.Time) ([]Batch, error) {
	byMedicine, err := batchesInStock(ctx, inv.db, now, medicineID)
	return byMedicine[medicineID], err
}

// Manufacturers lists the manufacturers already used, for the autocomplete.
func (inv *Inventory) Manufacturers(ctx context.Context) ([]string, error) {
	rows, err := inv.db.QueryContext(ctx, `SELECT DISTINCT manufacturer FROM batches`)
	if err != nil {
		return nil, fmt.Errorf("list manufacturers: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return distinctSorted(names), rows.Err()
}

// logChanges writes one "Ajuste" entry: a row per changed field, all with the same time.
// column says what changed: "medicine_id", "batch_id" or "movement_id".
func logChanges(ctx context.Context, tx *sql.Tx, column string, id int64, changes []fieldChange, userID int64, now time.Time) error {
	for _, c := range changes {
		_, err := tx.ExecContext(ctx, `INSERT INTO changes (`+column+`, field, old_value, new_value, user_id, changed_at)
			VALUES (?, ?, ?, ?, ?, ?)`, id, c.field, c.old, c.new, userID, db.FormatTime(now))
		if err != nil {
			return fmt.Errorf("log change of %s %d: %w", column, id, err)
		}
	}
	return nil
}
