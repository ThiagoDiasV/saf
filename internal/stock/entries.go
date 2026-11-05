package stock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"saf/internal/db"
	"saf/internal/form"
)

// EntryTypes are the kinds of Entrada, in the order the form lists them.
var EntryTypes = []string{"Doação", "Permuta", "Abastecimento", "Devolução", "Saldo inicial", "Outros"}

const (
	maxNoteRunes = 1000
	maxQuantity  = 1_000_000
)

// EntryLine is one row of the Entrada form, as typed.
type EntryLine struct {
	Medicine     string // the medicine's label, picked from the list: "Dipirona 500 mg comprimido"
	Lot          string
	Manufacturer string
	Expiry       string // "MM/AAAA"
	Quantity     string
}

// Entry is the Entrada form, as typed.
type Entry struct {
	Type  string
	Date  string // "AAAA-MM-DD"; may be in the past, never in the future
	Note  string
	Lines []EntryLine
}

type entryLine struct {
	medicineID   int64
	lot          string
	manufacturer string
	expiry       string // "AAAA-MM"
	quantity     int64
}

// RecordEntry saves an entry and returns its movement id. Each line adds to the lot with the same
// medicine, lot and manufacturer, or creates it; a lot keeps one expiry, so a different one is
// refused. Invalid input comes back as form.Errors keyed "type", "date", "note" and
// "lines.<i>.<field>" (medicine, lot, manufacturer, expiry, quantity).
func (inv *Inventory) RecordEntry(ctx context.Context, e Entry, userID int64, now time.Time) (int64, error) {
	byLabel, err := inv.activeByLabel(ctx)
	if err != nil {
		return 0, err
	}

	var movementID int64
	err = inv.inTx(ctx, func(tx *sql.Tx) error {
		date, note, lines, errs, err := checkEntry(ctx, tx, e, byLabel, now)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return errs
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO movements (kind, type, occurred_on, recorded_at, user_id, note) VALUES ('in', ?, ?, ?, ?, ?)`,
			e.Type, date, db.FormatTime(now), userID, note)
		if err != nil {
			return fmt.Errorf("insert entry: %w", err)
		}
		if movementID, err = res.LastInsertId(); err != nil {
			return err
		}
		for _, l := range lines {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO batches (medicine_id, lot, manufacturer, expiry_month, match_key) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (medicine_id, match_key) DO NOTHING`,
				l.medicineID, l.lot, l.manufacturer, l.expiry, batchKey(l.lot, l.manufacturer))
			if err != nil {
				return fmt.Errorf("insert batch %s: %w", l.lot, err)
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO movement_lines (movement_id, batch_id, quantity)
				VALUES (?, (SELECT id FROM batches WHERE medicine_id = ? AND match_key = ?), ?)`,
				movementID, l.medicineID, batchKey(l.lot, l.manufacturer), l.quantity)
			if err != nil {
				return fmt.Errorf("insert entry line %s: %w", l.lot, err)
			}
		}
		return nil
	})
	return movementID, err
}

// checkEntry validates the form inside the saving transaction, so a lot's expiry cannot change
// between the check and the save.
func checkEntry(ctx context.Context, tx *sql.Tx, e Entry, byLabel map[string]Medicine, now time.Time) (string, string, []entryLine, form.Errors, error) {
	errs := form.Errors{}
	if !slices.Contains(EntryTypes, e.Type) {
		errs["type"] = "Escolha o tipo de entrada."
	}
	date := checkDate(errs, e.Date, now)
	note := strings.TrimSpace(e.Note)
	switch {
	case e.Type == "Outros" && note == "":
		errs["note"] = "Descreva a entrada quando o tipo é Outros."
	case utf8.RuneCountInString(note) > maxNoteRunes:
		errs["note"] = fmt.Sprintf("Use no máximo %d caracteres.", maxNoteRunes)
	}
	if len(e.Lines) == 0 {
		errs["lines.0.medicine"] = "Adicione pelo menos um medicamento."
	}

	var lines []entryLine
	for i, l := range e.Lines {
		key := func(field string) string { return fmt.Sprintf("lines.%d.%s", i, field) }
		before := len(errs)
		m, known := byLabel[fold(l.Medicine)]
		if !known {
			errs[key("medicine")] = "Escolha um medicamento da lista. Se não existir, cadastre em Medicamentos."
		}
		lot, manufacturer := clean(l.Lot), clean(l.Manufacturer)
		checkText(errs, key("lot"), lot, "Informe o lote.")
		checkText(errs, key("manufacturer"), manufacturer, "Informe o fabricante.")
		expiry, expiryOK := parseExpiry(l.Expiry)
		if !expiryOK {
			errs[key("expiry")] = "Validade no formato MM/AAAA."
		}
		quantity, problem := parseQuantity(l.Quantity)
		if problem != "" {
			errs[key("quantity")] = problem
		}
		if len(errs) > before {
			continue
		}

		var stored, storedLot, storedManufacturer string
		err := tx.QueryRowContext(ctx, `SELECT expiry_month, lot, manufacturer FROM batches WHERE medicine_id = ? AND match_key = ?`,
			m.ID, batchKey(lot, manufacturer)).Scan(&stored, &storedLot, &storedManufacturer)
		switch {
		case err == nil && stored != expiry:
			errs[key("expiry")] = fmt.Sprintf("O lote %s (%s) já existe com validade %s. Corrija aqui ou edite o lote no Estoque.",
				storedLot, storedManufacturer, FormatExpiry(stored))
			continue
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return "", "", nil, nil, fmt.Errorf("look up lot %s: %w", lot, err)
		}
		twin := slices.IndexFunc(lines, func(o entryLine) bool {
			return o.medicineID == m.ID && batchKey(o.lot, o.manufacturer) == batchKey(lot, manufacturer)
		})
		if twin >= 0 && lines[twin].expiry != expiry {
			errs[key("expiry")] = "Este lote aparece em outra linha com outra validade."
			continue
		}
		lines = append(lines, entryLine{medicineID: m.ID, lot: lot, manufacturer: manufacturer, expiry: expiry, quantity: quantity})
	}
	return date, note, lines, errs, nil
}

// checkDate reads the movement date ("AAAA-MM-DD"): required, and never in the future.
func checkDate(errs form.Errors, text string, now time.Time) string {
	text = strings.TrimSpace(text)
	d, err := time.Parse("2006-01-02", text)
	switch {
	case text == "":
		errs["date"] = "Informe a data."
	case err != nil:
		errs["date"] = "Data inválida."
	case d.Format("2006-01-02") > now.In(TimeZone).Format("2006-01-02"):
		errs["date"] = "A data não pode estar no futuro."
	}
	return text
}

func checkText(errs form.Errors, key, value, missing string) {
	switch {
	case value == "":
		errs[key] = missing
	case utf8.RuneCountInString(value) > maxFieldRunes:
		errs[key] = fmt.Sprintf("Use no máximo %d caracteres.", maxFieldRunes)
	}
}

// parseQuantity reads a whole number of units; it returns a message when the text isn't one.
func parseQuantity(text string) (int64, string) {
	n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	switch {
	case err != nil || n < 1:
		return 0, "Quantidade: número inteiro maior que zero."
	case n > maxQuantity:
		return 0, "Quantidade grande demais. Confira o número."
	}
	return n, ""
}
