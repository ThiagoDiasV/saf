package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"saf/internal/auth"
	"saf/internal/form"
	"saf/internal/stock"
)

type entryView struct {
	Entry         stock.Entry // what was typed, to refill the form after an error
	Types         []string
	Medicines     []stock.Medicine // active ones, for the medicine list
	Manufacturers []string
	Today         string // "AAAA-MM-DD", the date field's default and maximum
}

func (s *server) entryPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	today := s.now().In(stock.TimeZone).Format("2006-01-02")
	s.renderEntry(w, r, user, http.StatusOK, stock.Entry{Date: today}, nil)
}

func (s *server) renderEntry(w http.ResponseWriter, r *http.Request, user auth.User, status int, e stock.Entry, errs form.Errors) {
	medicines, err := s.inventory.Medicines(r.Context(), stock.MedicineFilter{})
	if err != nil {
		s.serverError(w, err)
		return
	}
	manufacturers, err := s.inventory.Manufacturers(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	if len(e.Lines) == 0 {
		e.Lines = []stock.EntryLine{{}}
	}
	data := pageData{
		Title: "Nova entrada", Active: "/entrada", User: user, Errors: errs,
		Page: entryView{Entry: e, Types: stock.EntryTypes, Medicines: medicines, Manufacturers: manufacturers,
			Today: s.now().In(stock.TimeZone).Format("2006-01-02")},
	}
	if len(errs) > 0 {
		data.Flash = &flash{Kind: "error", Message: "Corrija os campos destacados."}
	}
	s.render(w, r, status, "entry.html", data)
}

// entryLines zips the repeated line fields back into rows, skipping rows left completely empty.
func entryLines(r *http.Request) []stock.EntryLine {
	field := func(name string, i int) string {
		if values := r.PostForm[name]; i < len(values) {
			return values[i]
		}
		return ""
	}
	var lines []stock.EntryLine
	for i := range len(r.PostForm["medicine"]) {
		l := stock.EntryLine{
			Medicine: field("medicine", i), Lot: field("lot", i), Manufacturer: field("manufacturer", i),
			Expiry: field("expiry", i), Quantity: field("quantity", i),
		}
		if strings.TrimSpace(l.Medicine+l.Lot+l.Manufacturer+l.Expiry+l.Quantity) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func (s *server) recordEntry(w http.ResponseWriter, r *http.Request, user auth.User) {
	e := stock.Entry{
		Type:  r.PostForm.Get("type"),
		Date:  r.PostForm.Get("date"),
		Note:  r.PostForm.Get("note"),
		Lines: entryLines(r),
	}
	// Photos are checked first, so a bad file never leaves an entry saved without them.
	photos, problem, err := photoUploads(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if problem != "" {
		s.renderEntry(w, r, user, http.StatusUnprocessableEntity, e, form.Errors{"photos": problem})
		return
	}
	now := s.now()
	id, err := s.inventory.RecordEntry(r.Context(), e, user.ID, now)
	var errs form.Errors
	if errors.As(err, &errs) {
		if len(photos) > 0 {
			errs["photos"] = photosAgain
		}
		s.renderEntry(w, r, user, http.StatusUnprocessableEntity, e, errs)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !s.attachPhotos(w, r, id, photos, user.ID, now, "Entrada") {
		http.Redirect(w, r, "/entrada", http.StatusSeeOther)
		return
	}
	noun := "medicamento"
	if len(e.Lines) > 1 {
		noun = "medicamentos"
	}
	setFlash(w, r, "ok", fmt.Sprintf("Entrada registrada com %d %s.", len(e.Lines), noun))
	http.Redirect(w, r, "/entrada", http.StatusSeeOther)
}

// entryLineView is what the "entry-line" template draws for one row.
type entryLineView struct {
	Line    stock.EntryLine
	Invalid map[string]bool // field name → has an error
	Errors  []string        // messages for the row, in field order
}

// entryLine picks row i's errors out of the form errors ("lines.<i>.<field>").
func entryLine(errs form.Errors, i int, l stock.EntryLine) entryLineView {
	v := entryLineView{Line: l, Invalid: map[string]bool{}}
	for _, field := range []string{"medicine", "lot", "manufacturer", "expiry", "quantity"} {
		if msg, ok := errs[fmt.Sprintf("lines.%d.%s", i, field)]; ok {
			v.Invalid[field] = true
			v.Errors = append(v.Errors, msg)
		}
	}
	return v
}
