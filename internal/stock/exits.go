package stock

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"saf/internal/db"
	"saf/internal/form"
)

// ExitTypes are the kinds of Saída, in the order the form lists them.
var ExitTypes = []string{Dispensing, "Perda", "Extravio", Discard, "Permuta", "Devolução ao fornecedor"}

const (
	Dispensing = "Dispensação"
	// Discard is the only exit that may take units from expired lots.
	Discard = "Vencimento/Descarte"

	maxFirstNameRunes = 20
)

// ExitLine is one medicine of the Saída form, as typed: the total wanted and how many units come
// out of each lot.
type ExitLine struct {
	Medicine string           // the medicine's label, picked from the list
	Quantity string           // total units
	Takes    map[int64]string // lot id → units taken from it
}

// Exit is the Saída form, as typed.
type Exit struct {
	Type        string
	Date        string // "AAAA-MM-DD"; may be in the past, never in the future
	Note        string
	PatientName string // Dispensação only: first name, one word (LGPD: no surname, no CPF)
	Lines       []ExitLine
}

// PrefillFEFO proposes how many units to take from each lot so the closest expiry leaves first.
// Expired lots are skipped unless allowExpired (a discard). batches must be in FEFO order, as
// AvailableBatches returns them.
func PrefillFEFO(batches []Batch, quantity int64, allowExpired bool) map[int64]int64 {
	takes := make(map[int64]int64, len(batches))
	left := quantity
	for _, b := range batches {
		if left <= 0 || (b.Expired() && !allowExpired) {
			takes[b.ID] = 0
			continue
		}
		take := min(left, b.OnHand)
		takes[b.ID] = take
		left -= take
	}
	return takes
}

type exitTake struct {
	batchID  int64
	quantity int64
}

// RecordExit saves an exit and returns its movement id. Stock is checked inside the saving
// transaction, so two exits can never take the same units. Invalid input comes back as
// form.Errors keyed "type", "date", "note", "patient_name", "lines.<i>.medicine",
// "lines.<i>.quantity" and "lines.<i>.take.<lot id>".
func (inv *Inventory) RecordExit(ctx context.Context, e Exit, userID int64, now time.Time) (int64, error) {
	byLabel, err := inv.activeByLabel(ctx)
	if err != nil {
		return 0, err
	}
	var movementID int64
	err = inv.inTx(ctx, func(tx *sql.Tx) error {
		date, note, name, takes, errs, err := checkExit(ctx, tx, e, byLabel, now)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return errs
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO movements (kind, type, occurred_on, recorded_at, user_id, note) VALUES ('out', ?, ?, ?, ?, ?)`,
			e.Type, date, db.FormatTime(now), userID, note)
		if err != nil {
			return fmt.Errorf("insert exit: %w", err)
		}
		if movementID, err = res.LastInsertId(); err != nil {
			return err
		}
		if name != "" {
			sealed := inv.box.Seal([]byte(name), patientNameContext(movementID))
			if _, err := tx.ExecContext(ctx, `UPDATE movements SET patient_name = ? WHERE id = ?`, sealed, movementID); err != nil {
				return fmt.Errorf("store patient name of movement %d: %w", movementID, err)
			}
		}
		for _, t := range takes {
			_, err := tx.ExecContext(ctx, `INSERT INTO movement_lines (movement_id, batch_id, quantity) VALUES (?, ?, ?)`,
				movementID, t.batchID, -t.quantity)
			if err != nil {
				return fmt.Errorf("insert exit line for batch %d: %w", t.batchID, err)
			}
		}
		return nil
	})
	return movementID, err
}

// patientNameContext binds a sealed first name to its movement: copied onto another row it won't open.
func patientNameContext(movementID int64) string {
	return fmt.Sprintf("movement:%d:patient_name", movementID)
}

// PatientName returns the first name stored on a movement, or "" when there is none.
func (inv *Inventory) PatientName(ctx context.Context, movementID int64) (string, error) {
	var sealed []byte
	err := inv.db.QueryRowContext(ctx, `SELECT patient_name FROM movements WHERE id = ?`, movementID).Scan(&sealed)
	if err != nil {
		return "", fmt.Errorf("load patient name of movement %d: %w", movementID, err)
	}
	if sealed == nil {
		return "", nil
	}
	name, err := inv.box.Open(sealed, patientNameContext(movementID))
	if err != nil {
		return "", fmt.Errorf("open patient name of movement %d: %w", movementID, err)
	}
	return string(name), nil
}

func checkExit(ctx context.Context, tx *sql.Tx, e Exit, byLabel map[string]Medicine, now time.Time) (
	date, note, name string, takes []exitTake, errs form.Errors, err error) {
	errs = form.Errors{}
	if !slices.Contains(ExitTypes, e.Type) {
		errs["type"] = "Escolha o tipo de saída."
	}
	date = checkDate(errs, e.Date, now)
	note = strings.TrimSpace(e.Note)
	if utf8.RuneCountInString(note) > maxNoteRunes {
		errs["note"] = fmt.Sprintf("Use no máximo %d caracteres.", maxNoteRunes)
	}
	if e.Type == Dispensing {
		name = strings.TrimSpace(e.PatientName)
		if problem := firstNameProblem(name); problem != "" {
			errs["patient_name"] = problem
		}
	}
	if len(e.Lines) == 0 {
		errs["lines.0.medicine"] = "Adicione pelo menos um medicamento."
	}

	seen := map[int64]bool{}
	for i, l := range e.Lines {
		key := func(field string) string { return fmt.Sprintf("lines.%d.%s", i, field) }
		m, known := byLabel[fold(l.Medicine)]
		switch {
		case !known:
			errs[key("medicine")] = "Escolha um medicamento da lista."
			continue
		case seen[m.ID]:
			errs[key("medicine")] = "Este medicamento já está em outra linha."
			continue
		}
		seen[m.ID] = true
		wanted, problem := parseQuantity(l.Quantity)
		if problem != "" {
			errs[key("quantity")] = problem
			continue
		}
		byMedicine, err := batchesInStock(ctx, tx, now, m.ID)
		if err != nil {
			return "", "", "", nil, nil, err
		}
		batches := byMedicine[m.ID]
		if len(batches) == 0 {
			errs[key("quantity")] = "Sem estoque deste medicamento."
			continue
		}
		lineTakes, ok := checkTakes(errs, key, l.Takes, batches, e.Type)
		if !ok {
			continue
		}
		var taken int64
		for _, t := range lineTakes {
			taken += t.quantity
		}
		if taken != wanted {
			errs[key("quantity")] = fmt.Sprintf("Os lotes somam %d, mas a quantidade é %d.", taken, wanted)
			continue
		}
		takes = append(takes, lineTakes...)
	}
	return date, note, name, takes, errs, nil
}

// checkTakes reads how many units come out of each lot. It reports false when a take is wrong.
func checkTakes(errs form.Errors, key func(string) string, typed map[int64]string, batches []Batch, exitType string) ([]exitTake, bool) {
	ok := true
	available := map[int64]Batch{}
	for _, b := range batches {
		available[b.ID] = b
	}
	for id, text := range typed {
		if _, inStock := available[id]; !inStock && strings.TrimSpace(text) != "" && strings.TrimSpace(text) != "0" {
			errs[key("quantity")] = "Um dos lotes não tem mais estoque. Escolha o medicamento de novo."
			ok = false
		}
	}
	var takes []exitTake
	for _, b := range batches {
		text := strings.TrimSpace(typed[b.ID])
		if text == "" {
			continue
		}
		takeKey := key(fmt.Sprintf("take.%d", b.ID))
		n, err := strconv.ParseInt(text, 10, 64)
		switch {
		case err != nil || n < 0:
			errs[takeKey] = "Número inteiro."
			ok = false
		case n > b.OnHand:
			errs[takeKey] = fmt.Sprintf("Máximo %d.", b.OnHand)
			ok = false
		case n > 0 && b.Expired() && exitType != Discard:
			errs[takeKey] = "Lote vencido só sai por Vencimento/Descarte."
			ok = false
		case n > 0:
			takes = append(takes, exitTake{batchID: b.ID, quantity: n})
		}
	}
	return takes, ok
}

// firstNameProblem enforces the LGPD rule: only a first name, one word of letters, at most 20.
// Empty is fine, the field is optional.
func firstNameProblem(name string) string {
	switch {
	case name == "":
		return ""
	case strings.ContainsFunc(name, unicode.IsSpace):
		return "Use só o primeiro nome, sem sobrenome."
	case strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) }):
		return "Use só letras."
	case utf8.RuneCountInString(name) > maxFirstNameRunes:
		return fmt.Sprintf("Use no máximo %d letras.", maxFirstNameRunes)
	}
	return ""
}
