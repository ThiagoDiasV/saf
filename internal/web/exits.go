package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"saf/internal/auth"
	"saf/internal/form"
	"saf/internal/stock"
)

const maxExitLines = 200

type exitView struct {
	Exit      stock.Exit
	Lines     []exitLineView
	Types     []string
	Medicines []stock.Medicine // active ones, for the medicine list
	Today     string           // "AAAA-MM-DD", the date field's default and maximum
	NextIndex int              // first free line number for lines the browser adds
}

// exitLineView is one medicine of the Saída form with its lots.
type exitLineView struct {
	Index    int
	Line     stock.ExitLine
	Medicine stock.Medicine // zero when the typed label matches no active medicine
	Batches  []exitBatchView
	Invalid  map[string]bool // "medicine" or "quantity" → has an error
	Errors   []string
}

type exitBatchView struct {
	stock.Batch
	Take    string
	Blocked bool   // expired, and the exit is not a discard
	Error   string // about this lot's take
}

// exitLineView builds line i. With prefill the takes come from the FEFO proposal for the typed
// quantity; otherwise they are what was typed (after an error).
func (s *server) exitLineView(ctx context.Context, i int, l stock.ExitLine, exitType string, errs form.Errors, prefill bool) (exitLineView, error) {
	v := exitLineView{Index: i, Line: l, Invalid: map[string]bool{}}
	for _, field := range []string{"medicine", "quantity"} {
		if msg, ok := errs[fmt.Sprintf("lines.%d.%s", i, field)]; ok {
			v.Invalid[field] = true
			v.Errors = append(v.Errors, msg)
		}
	}
	m, found, err := s.inventory.ActiveMedicineByLabel(ctx, l.Medicine)
	if err != nil || !found {
		return v, err
	}
	v.Medicine = m
	batches, err := s.inventory.AvailableBatches(ctx, m.ID, s.now())
	if err != nil {
		return v, err
	}
	discard := exitType == stock.Discard
	var proposed map[int64]int64
	if prefill {
		quantity, _ := strconv.ParseInt(strings.TrimSpace(l.Quantity), 10, 64) // not a number: propose nothing
		proposed = stock.PrefillFEFO(batches, quantity, discard)
	}
	for _, b := range batches {
		take := l.Takes[b.ID]
		if prefill {
			take = strconv.FormatInt(proposed[b.ID], 10)
		}
		if take == "" {
			take = "0"
		}
		v.Batches = append(v.Batches, exitBatchView{
			Batch: b, Take: take, Blocked: b.Expired() && !discard,
			Error: errs[fmt.Sprintf("lines.%d.take.%d", i, b.ID)],
		})
	}
	return v, nil
}

func (s *server) exitPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	today := s.now().In(stock.TimeZone).Format("2006-01-02")
	s.renderExit(w, r, user, http.StatusOK, stock.Exit{Date: today}, nil)
}

func (s *server) renderExit(w http.ResponseWriter, r *http.Request, user auth.User, status int, e stock.Exit, errs form.Errors) {
	medicines, err := s.inventory.Medicines(r.Context(), stock.MedicineFilter{})
	if err != nil {
		s.serverError(w, err)
		return
	}
	lines := e.Lines
	if len(lines) == 0 {
		lines = []stock.ExitLine{{}}
	}
	view := exitView{Exit: e, Types: stock.ExitTypes, Medicines: medicines, NextIndex: len(lines),
		Today: s.now().In(stock.TimeZone).Format("2006-01-02")}
	for i, l := range lines {
		v, err := s.exitLineView(r.Context(), i, l, e.Type, errs, false)
		if err != nil {
			s.serverError(w, err)
			return
		}
		view.Lines = append(view.Lines, v)
	}
	data := pageData{Title: "Nova saída", Active: "/saida", User: user, Errors: errs, Page: view}
	if len(errs) > 0 {
		data.Flash = &flash{Kind: "error", Message: "Corrija os campos destacados."}
	}
	s.render(w, r, status, "exit.html", data)
}

// exitBatches answers the browser when a line's medicine, quantity or the exit type changes:
// the line's lots with the FEFO proposal, as HTML to drop into the form.
func (s *server) exitBatches(w http.ResponseWriter, r *http.Request, _ auth.User) {
	q := r.URL.Query()
	i, err := strconv.Atoi(q.Get("linha"))
	if err != nil || i < 0 || i >= maxExitLines {
		http.Error(w, "Linha inválida.", http.StatusBadRequest)
		return
	}
	line := stock.ExitLine{Medicine: q.Get("medicamento"), Quantity: q.Get("quantidade")}
	v, err := s.exitLineView(r.Context(), i, line, q.Get("tipo"), nil, true)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragment(w, "exit-fefo", v)
}

// exitLines reads lines.<n>.medicine, lines.<n>.quantity and lines.<n>.take.<lot id> back into
// lines. The browser numbers lines as they are added, so numbers may have gaps; rows left empty
// are dropped.
func exitLines(r *http.Request) []stock.ExitLine {
	byIndex := map[int]*stock.ExitLine{}
	for key, values := range r.PostForm {
		parts := strings.Split(key, ".")
		if len(parts) < 3 || parts[0] != "lines" {
			continue
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 0 || n >= maxExitLines {
			continue
		}
		l := byIndex[n]
		if l == nil {
			l = &stock.ExitLine{Takes: map[int64]string{}}
			byIndex[n] = l
		}
		switch {
		case len(parts) == 3 && parts[2] == "medicine":
			l.Medicine = values[0]
		case len(parts) == 3 && parts[2] == "quantity":
			l.Quantity = values[0]
		case len(parts) == 4 && parts[2] == "take":
			if id, err := strconv.ParseInt(parts[3], 10, 64); err == nil {
				l.Takes[id] = values[0]
			}
		}
	}
	indices := make([]int, 0, len(byIndex))
	for n := range byIndex {
		indices = append(indices, n)
	}
	slices.Sort(indices)
	var lines []stock.ExitLine
	for _, n := range indices {
		if l := byIndex[n]; strings.TrimSpace(l.Medicine+l.Quantity) != "" {
			lines = append(lines, *l)
		}
	}
	return lines
}

func (s *server) recordExit(w http.ResponseWriter, r *http.Request, user auth.User) {
	e := stock.Exit{
		Type:        r.PostForm.Get("type"),
		Date:        r.PostForm.Get("date"),
		Note:        r.PostForm.Get("note"),
		PatientName: r.PostForm.Get("patient_name"),
		Lines:       exitLines(r),
	}
	// Photos are checked first, so a bad file never leaves an exit saved without them.
	photos, problem, err := photoUploads(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if problem != "" {
		s.renderExit(w, r, user, http.StatusUnprocessableEntity, e, form.Errors{"photos": problem})
		return
	}
	now := s.now()
	id, err := s.inventory.RecordExit(r.Context(), e, user.ID, now)
	var errs form.Errors
	if errors.As(err, &errs) {
		if len(photos) > 0 {
			errs["photos"] = photosAgain
		}
		s.renderExit(w, r, user, http.StatusUnprocessableEntity, e, errs)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !s.attachPhotos(w, r, id, photos, user.ID, now, "Saída") {
		http.Redirect(w, r, "/saida", http.StatusSeeOther)
		return
	}
	setFlash(w, r, "ok", fmt.Sprintf("Saída registrada: %s.", strings.ToLower(e.Type)))
	http.Redirect(w, r, "/saida", http.StatusSeeOther)
}
