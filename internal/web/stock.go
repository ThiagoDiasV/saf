package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"saf/internal/auth"
	"saf/internal/form"
	"saf/internal/stock"
)

type stockView struct {
	Groups []stock.StockGroup
	Counts stock.StockCounts
	Filter stock.StockFilter
	Today  string // "08/10/2026"
	CSVURL string // same filter, as a download
}

func stockFilter(r *http.Request) stock.StockFilter {
	f := stock.StockFilter{Query: r.URL.Query().Get("q"), Validity: r.URL.Query().Get("validade")}
	if f.Validity != "vencendo" && f.Validity != "vencidos" {
		f.Validity = ""
	}
	return f
}

func (s *server) stockPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	filter := stockFilter(r)
	groups, counts, err := s.inventory.StockGroups(r.Context(), filter, s.now())
	if err != nil {
		s.serverError(w, err)
		return
	}
	csvURL := "/estoque.csv"
	if r.URL.RawQuery != "" {
		csvURL += "?" + url.Values{"q": {filter.Query}, "validade": {filter.Validity}}.Encode()
	}
	s.render(w, r, http.StatusOK, "stock.html", pageData{
		Title: "Estoque", Active: "/estoque", User: user,
		Page: stockView{Groups: groups, Counts: counts, Filter: filter, CSVURL: csvURL,
			Today: s.now().In(stock.TimeZone).Format("02/01/2006")},
	})
}

func (s *server) stockCSV(w http.ResponseWriter, r *http.Request, _ auth.User) {
	groups, _, err := s.inventory.StockGroups(r.Context(), stockFilter(r), s.now())
	if err != nil {
		s.serverError(w, err)
		return
	}
	rows := [][]string{{"Medicamento", "Unidade", "Lote", "Fabricante", "Validade", "Quantidade", "Situação"}}
	for _, g := range groups {
		if len(g.Batches) == 0 {
			rows = append(rows, []string{g.Medicine.Label(), g.Medicine.Unit, "", "", "", "0", "Sem estoque"})
		}
		for _, b := range g.Batches {
			rows = append(rows, []string{g.Medicine.Label(), g.Medicine.Unit, b.Lot, b.Manufacturer, b.Expiry(),
				strconv.FormatInt(b.OnHand, 10), expiryText(b)})
		}
	}
	filename := "estoque-" + s.now().In(stock.TimeZone).Format("2006-01-02") + ".csv"
	if err := writeCSV(w, filename, rows); err != nil {
		s.serverError(w, err)
	}
}

func expiryText(b stock.Batch) string {
	switch {
	case b.Expired():
		return "Vencido"
	case b.Expiring():
		return fmt.Sprintf("Vence em %d dias", b.DaysLeft)
	}
	return "Dentro da validade"
}

type batchEditView struct {
	Batch         stock.Batch
	Medicine      stock.Medicine
	Values        stock.BatchInput
	Manufacturers []string
}

// targetBatch loads the lot named in the URL with its medicine, answering 404 when there is none.
func (s *server) targetBatch(w http.ResponseWriter, r *http.Request) (stock.Batch, stock.Medicine, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return stock.Batch{}, stock.Medicine{}, false
	}
	b, err := s.inventory.Batch(r.Context(), id, s.now())
	if errors.Is(err, stock.ErrBatchNotFound) {
		http.NotFound(w, r)
		return stock.Batch{}, stock.Medicine{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return stock.Batch{}, stock.Medicine{}, false
	}
	m, err := s.inventory.Medicine(r.Context(), b.MedicineID)
	if err != nil {
		s.serverError(w, err)
		return stock.Batch{}, stock.Medicine{}, false
	}
	return b, m, true
}

func (s *server) editBatchPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	b, m, ok := s.targetBatch(w, r)
	if !ok {
		return
	}
	s.renderBatchEdit(w, r, user, http.StatusOK, b, m, stock.BatchInput{Lot: b.Lot, Manufacturer: b.Manufacturer, Expiry: b.Expiry()}, nil)
}

func (s *server) renderBatchEdit(w http.ResponseWriter, r *http.Request, user auth.User, status int,
	b stock.Batch, m stock.Medicine, values stock.BatchInput, errs form.Errors) {
	manufacturers, err := s.inventory.Manufacturers(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, status, "batch_edit.html", pageData{
		Title: "Editar lote", Active: "/estoque", User: user, Errors: errs,
		Page: batchEditView{Batch: b, Medicine: m, Values: values, Manufacturers: manufacturers},
	})
}

func (s *server) updateBatch(w http.ResponseWriter, r *http.Request, user auth.User) {
	b, m, ok := s.targetBatch(w, r)
	if !ok {
		return
	}
	in := stock.BatchInput{Lot: r.PostForm.Get("lot"), Manufacturer: r.PostForm.Get("manufacturer"), Expiry: r.PostForm.Get("expiry")}
	changed, err := s.inventory.UpdateBatch(r.Context(), b.ID, in, user.ID, s.now())
	var errs form.Errors
	if errors.As(err, &errs) {
		s.renderBatchEdit(w, r, user, http.StatusUnprocessableEntity, b, m, in, errs)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if changed {
		setFlash(w, r, "ok", "Lote atualizado. A alteração ficou registrada no histórico.")
	}
	// The anchor reopens this medicine's group on the Estoque screen.
	http.Redirect(w, r, fmt.Sprintf("/estoque#med-%d", m.ID), http.StatusSeeOther)
}
