// Package stock owns the pharmacy's stock: the medicine catalog, lots and movements.
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
	"saf/internal/seal"
)

var (
	ErrMedicineNotFound = errors.New("medicine not found")
	// ErrStillInStock: a medicine is archived only once its stock is zero.
	ErrStillInStock = errors.New("medicine still has stock")
	// ErrHasMovements: a medicine that ever moved is archived, never deleted.
	ErrHasMovements = errors.New("medicine has movements")
)

const maxFieldRunes = 80

// Inventory reads and changes the stock in the database.
type Inventory struct {
	db       *sql.DB
	box      *seal.Box // seals patients' first names and photos
	photoDir string    // one sealed file per photo, named by its id
}

// NewInventory returns an Inventory backed by conn, sealing personal data with box and keeping
// photo files in photoDir.
//
//	inventory := stock.NewInventory(conn, box, filepath.Join(dataDir, "photos"))
func NewInventory(conn *sql.DB, box *seal.Box, photoDir string) *Inventory {
	return &Inventory{db: conn, box: box, photoDir: photoDir}
}

// Medicine is one catalog item: active ingredient + strength + dosage form, counted in Unit.
type Medicine struct {
	ID         int64
	Ingredient string
	Strength   string
	DosageForm string
	Unit       string
	Archived   bool
	OnHand     int64 // units in stock, all lots together
	Moved      bool  // has had at least one entry or exit
}

// Label is how the medicine reads everywhere: "Dipirona 500 mg comprimido".
func (m Medicine) Label() string {
	return m.Ingredient + " " + m.Strength + " " + strings.ToLower(m.DosageForm)
}

// MedicineInput is what the medicine form sends.
type MedicineInput struct {
	Ingredient string
	Strength   string
	DosageForm string
	Unit       string
}

func (in MedicineInput) cleaned() MedicineInput {
	return MedicineInput{
		Ingredient: clean(in.Ingredient),
		Strength:   clean(in.Strength),
		DosageForm: clean(in.DosageForm),
		Unit:       strings.ToLower(clean(in.Unit)),
	}
}

func (in MedicineInput) matchKey() string {
	return fold(in.Ingredient + in.Strength + in.DosageForm)
}

func (in MedicineInput) validate() form.Errors {
	errs := form.Errors{}
	for _, f := range []struct{ name, value, missing string }{
		{"ingredient", in.Ingredient, "Informe o princípio ativo."},
		{"strength", in.Strength, "Informe a concentração."},
		{"dosage_form", in.DosageForm, "Informe a forma farmacêutica."},
		{"unit", in.Unit, "Informe a unidade de contagem."},
	} {
		switch {
		case f.value == "":
			errs[f.name] = f.missing
		case utf8.RuneCountInString(f.value) > maxFieldRunes:
			errs[f.name] = fmt.Sprintf("Use no máximo %d caracteres.", maxFieldRunes)
		}
	}
	return errs
}

// CreateMedicine adds a medicine to the catalog. Invalid input and duplicates come back as form.Errors.
func (inv *Inventory) CreateMedicine(ctx context.Context, in MedicineInput, now time.Time) (int64, error) {
	in = in.cleaned()
	if errs := in.validate(); len(errs) > 0 {
		return 0, errs
	}
	dup, err := inv.duplicateOf(ctx, in, 0)
	if err != nil {
		return 0, err
	}
	if dup != nil {
		return 0, dup
	}
	res, err := inv.db.ExecContext(ctx, `
		INSERT INTO medicines (active_ingredient, strength, dosage_form, unit, match_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		in.Ingredient, in.Strength, in.DosageForm, in.Unit, in.matchKey(), db.FormatTime(now))
	if db.IsUniqueViolation(err) { // created by someone else in the meantime
		return 0, form.Errors{"dosage_form": "Este medicamento já existe."}
	}
	if err != nil {
		return 0, fmt.Errorf("insert medicine %q: %w", in.matchKey(), err)
	}
	return res.LastInsertId()
}

// duplicateOf finds another medicine (not selfID) with the same match key and says so in the form.
func (inv *Inventory) duplicateOf(ctx context.Context, in MedicineInput, selfID int64) (form.Errors, error) {
	row := inv.db.QueryRowContext(ctx, medicineSelect+` WHERE m.match_key = ? AND m.id <> ?`, in.matchKey(), selfID)
	other, err := scanMedicine(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if other.Archived {
		return form.Errors{"dosage_form": fmt.Sprintf("Já existe, arquivado: %s. Restaure em vez de cadastrar de novo.", other.Label())}, nil
	}
	return form.Errors{"dosage_form": fmt.Sprintf("Já existe: %s. Maiúsculas, acentos e espaços não contam.", other.Label())}, nil
}

// UpdateMedicine corrects a medicine's fields (typos). Each changed field is logged old → new as
// one "Ajuste" in Histórico. It reports whether anything changed.
func (inv *Inventory) UpdateMedicine(ctx context.Context, id int64, in MedicineInput, userID int64, now time.Time) (bool, error) {
	current, err := inv.Medicine(ctx, id)
	if err != nil {
		return false, err
	}
	in = in.cleaned()
	if errs := in.validate(); len(errs) > 0 {
		return false, errs
	}
	dup, err := inv.duplicateOf(ctx, in, id)
	if err != nil {
		return false, err
	}
	if dup != nil {
		return false, dup
	}

	var changed []fieldChange
	for _, f := range []fieldChange{
		{"Princípio ativo", current.Ingredient, in.Ingredient},
		{"Concentração", current.Strength, in.Strength},
		{"Forma farmacêutica", current.DosageForm, in.DosageForm},
		{"Unidade", current.Unit, in.Unit},
	} {
		if f.old != f.new {
			changed = append(changed, f)
		}
	}
	if len(changed) == 0 {
		return false, nil
	}
	err = inv.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE medicines SET active_ingredient = ?, strength = ?, dosage_form = ?, unit = ?, match_key = ?
			WHERE id = ?`,
			in.Ingredient, in.Strength, in.DosageForm, in.Unit, in.matchKey(), id)
		if err != nil {
			return fmt.Errorf("update medicine %d: %w", id, err)
		}
		return logChanges(ctx, tx, "medicine_id", id, changed, userID, now)
	})
	if db.IsUniqueViolation(err) {
		return false, form.Errors{"dosage_form": "Este medicamento já existe."}
	}
	return err == nil, err
}

// SetMedicineArchived archives a medicine (hidden from Estoque, Entrada and Saída; history keeps it)
// or restores it. Archiving needs the stock at zero.
func (inv *Inventory) SetMedicineArchived(ctx context.Context, id int64, archived bool, userID int64, now time.Time) error {
	m, err := inv.Medicine(ctx, id)
	if err != nil {
		return err
	}
	if m.Archived == archived {
		return nil
	}
	if archived && m.OnHand != 0 {
		return ErrStillInStock
	}
	change := fieldChange{"Situação", "Ativo", "Arquivado"}
	archivedAt := sql.NullString{String: db.FormatTime(now), Valid: true}
	if !archived {
		change = fieldChange{"Situação", "Arquivado", "Ativo"}
		archivedAt = sql.NullString{}
	}
	return inv.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE medicines SET archived_at = ? WHERE id = ?`, archivedAt, id); err != nil {
			return fmt.Errorf("archive medicine %d: %w", id, err)
		}
		return logChanges(ctx, tx, "medicine_id", id, []fieldChange{change}, userID, now)
	})
}

// DeleteMedicine removes a medicine that never moved, with the log of its edits.
// One that moved must be archived instead: its movements keep pointing at it.
func (inv *Inventory) DeleteMedicine(ctx context.Context, id int64) error {
	m, err := inv.Medicine(ctx, id)
	if err != nil {
		return err
	}
	if m.Moved {
		return ErrHasMovements
	}
	return inv.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM changes WHERE medicine_id = ?`, id); err != nil {
			return fmt.Errorf("delete changes of medicine %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM medicines WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete medicine %d: %w", id, err)
		}
		return nil
	})
}

type fieldChange struct {
	field, old, new string
}

// medicineSelect reads a medicine with its stock and whether it ever moved.
const medicineSelect = `
	SELECT m.id, m.active_ingredient, m.strength, m.dosage_form, m.unit, m.archived_at IS NOT NULL,
		COALESCE((SELECT SUM(l.quantity) FROM movement_lines l JOIN batches b ON b.id = l.batch_id
			WHERE b.medicine_id = m.id), 0),
		EXISTS (SELECT 1 FROM movement_lines l JOIN batches b ON b.id = l.batch_id WHERE b.medicine_id = m.id)
	FROM medicines m`

func scanMedicine(row interface{ Scan(...any) error }) (Medicine, error) {
	var m Medicine
	err := row.Scan(&m.ID, &m.Ingredient, &m.Strength, &m.DosageForm, &m.Unit, &m.Archived, &m.OnHand, &m.Moved)
	return m, err
}

// Medicine returns one medicine, or ErrMedicineNotFound.
func (inv *Inventory) Medicine(ctx context.Context, id int64) (Medicine, error) {
	m, err := scanMedicine(inv.db.QueryRowContext(ctx, medicineSelect+` WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Medicine{}, ErrMedicineNotFound
	}
	if err != nil {
		return Medicine{}, fmt.Errorf("load medicine %d: %w", id, err)
	}
	return m, nil
}

// MedicineFilter narrows the catalog list.
type MedicineFilter struct {
	Query           string // matched against the label; case, accents and spaces don't count
	IncludeArchived bool
}

// Medicines lists the catalog in alphabetical order.
func (inv *Inventory) Medicines(ctx context.Context, filter MedicineFilter) ([]Medicine, error) {
	query := medicineSelect
	if !filter.IncludeArchived {
		query += ` WHERE m.archived_at IS NULL`
	}
	rows, err := inv.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list medicines: %w", err)
	}
	defer rows.Close()
	want := fold(filter.Query)
	var medicines []Medicine
	for rows.Next() {
		m, err := scanMedicine(rows)
		if err != nil {
			return nil, err
		}
		if strings.Contains(fold(m.Label()), want) {
			medicines = append(medicines, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(medicines, func(a, b Medicine) int { return cmp.Compare(fold(a.Label()), fold(b.Label())) })
	return medicines, nil
}

// activeByLabel maps each active medicine's folded label to it. Entrada and Saída pick medicines
// by label from a list; folding makes "dipirona 500MG comprimido" find "Dipirona 500 mg comprimido".
func (inv *Inventory) activeByLabel(ctx context.Context) (map[string]Medicine, error) {
	medicines, err := inv.Medicines(ctx, MedicineFilter{})
	if err != nil {
		return nil, err
	}
	byLabel := make(map[string]Medicine, len(medicines))
	for _, m := range medicines {
		byLabel[fold(m.Label())] = m
	}
	return byLabel, nil
}

// ActiveMedicineByLabel finds an active medicine by its label as picked from the list.
func (inv *Inventory) ActiveMedicineByLabel(ctx context.Context, label string) (Medicine, bool, error) {
	byLabel, err := inv.activeByLabel(ctx)
	if err != nil {
		return Medicine{}, false, err
	}
	m, ok := byLabel[fold(label)]
	return m, ok, nil
}

// Suggestions feed the form's autocomplete lists: what the catalog already uses, plus common values.
type Suggestions struct {
	Ingredients []string
	DosageForms []string
	Units       []string
}

var (
	commonDosageForms = []string{"Comprimido", "Comprimido revestido", "Cápsula", "Drágea", "Solução oral",
		"Suspensão oral", "Xarope", "Gotas", "Creme", "Pomada", "Gel", "Solução injetável"}
	commonUnits = []string{"comprimido", "cápsula", "drágea", "frasco", "bisnaga", "ampola", "sachê", "tubo"}
)

// CatalogSuggestions returns the autocomplete lists for the medicine form.
func (inv *Inventory) CatalogSuggestions(ctx context.Context) (Suggestions, error) {
	all, err := inv.Medicines(ctx, MedicineFilter{IncludeArchived: true})
	if err != nil {
		return Suggestions{}, err
	}
	s := Suggestions{DosageForms: slices.Clone(commonDosageForms), Units: slices.Clone(commonUnits)}
	for _, m := range all {
		s.Ingredients = append(s.Ingredients, m.Ingredient)
		s.DosageForms = append(s.DosageForms, m.DosageForm)
		s.Units = append(s.Units, m.Unit)
	}
	return Suggestions{Ingredients: distinctSorted(s.Ingredients), DosageForms: distinctSorted(s.DosageForms), Units: distinctSorted(s.Units)}, nil
}

// distinctSorted drops repeats (by folded spelling, first one wins) and sorts alphabetically.
func distinctSorted(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if key := fold(v); !seen[key] {
			seen[key] = true
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return cmp.Compare(fold(a), fold(b)) })
	return out
}

func (inv *Inventory) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := inv.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once committed
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
