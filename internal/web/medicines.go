package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"saf/internal/auth"
	"saf/internal/form"
	"saf/internal/stock"
)

type medicinesView struct {
	Medicines       []stock.Medicine
	Query           string
	IncludeArchived bool
	Values          stock.MedicineInput // refills the form after an error
	Suggestions     stock.Suggestions
}

func (s *server) medicinesPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	s.renderMedicines(w, r, user, http.StatusOK, stock.MedicineInput{}, nil)
}

func (s *server) renderMedicines(w http.ResponseWriter, r *http.Request, user auth.User, status int, values stock.MedicineInput, errs form.Errors) {
	filter := stock.MedicineFilter{Query: r.URL.Query().Get("q"), IncludeArchived: r.URL.Query().Get("arquivados") == "1"}
	medicines, err := s.inventory.Medicines(r.Context(), filter)
	if err != nil {
		s.serverError(w, err)
		return
	}
	suggestions, err := s.inventory.CatalogSuggestions(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, status, "medicines.html", pageData{
		Title: "Medicamentos", Active: "/medicamentos", User: user, Errors: errs,
		Page: medicinesView{Medicines: medicines, Query: filter.Query, IncludeArchived: filter.IncludeArchived,
			Values: values, Suggestions: suggestions},
	})
}

func medicineInput(r *http.Request) stock.MedicineInput {
	return stock.MedicineInput{
		Ingredient: r.PostForm.Get("ingredient"),
		Strength:   r.PostForm.Get("strength"),
		DosageForm: r.PostForm.Get("dosage_form"),
		Unit:       r.PostForm.Get("unit"),
	}
}

func (s *server) createMedicine(w http.ResponseWriter, r *http.Request, user auth.User) {
	in := medicineInput(r)
	id, err := s.inventory.CreateMedicine(r.Context(), in, s.now())
	var errs form.Errors
	if errors.As(err, &errs) {
		s.renderMedicines(w, r, user, http.StatusUnprocessableEntity, in, errs)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	m, err := s.inventory.Medicine(r.Context(), id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	setFlash(w, r, "ok", fmt.Sprintf("%s cadastrado. Já aparece em Entrada.", m.Label()))
	http.Redirect(w, r, "/medicamentos", http.StatusSeeOther)
}

// targetMedicine loads the medicine named in the URL, answering 404 when there is none.
func (s *server) targetMedicine(w http.ResponseWriter, r *http.Request) (stock.Medicine, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return stock.Medicine{}, false
	}
	m, err := s.inventory.Medicine(r.Context(), id)
	if errors.Is(err, stock.ErrMedicineNotFound) {
		http.NotFound(w, r)
		return stock.Medicine{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return stock.Medicine{}, false
	}
	return m, true
}

type medicineEditView struct {
	Medicine    stock.Medicine
	Values      stock.MedicineInput
	Suggestions stock.Suggestions
}

func (s *server) editMedicinePage(w http.ResponseWriter, r *http.Request, user auth.User) {
	m, ok := s.targetMedicine(w, r)
	if !ok {
		return
	}
	values := stock.MedicineInput{Ingredient: m.Ingredient, Strength: m.Strength, DosageForm: m.DosageForm, Unit: m.Unit}
	s.renderMedicineEdit(w, r, user, http.StatusOK, m, values, nil)
}

func (s *server) renderMedicineEdit(w http.ResponseWriter, r *http.Request, user auth.User, status int, m stock.Medicine, values stock.MedicineInput, errs form.Errors) {
	suggestions, err := s.inventory.CatalogSuggestions(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, status, "medicine_edit.html", pageData{
		Title: "Editar medicamento", Active: "/medicamentos", User: user, Errors: errs,
		Page: medicineEditView{Medicine: m, Values: values, Suggestions: suggestions},
	})
}

func (s *server) updateMedicine(w http.ResponseWriter, r *http.Request, user auth.User) {
	m, ok := s.targetMedicine(w, r)
	if !ok {
		return
	}
	in := medicineInput(r)
	changed, err := s.inventory.UpdateMedicine(r.Context(), m.ID, in, user.ID, s.now())
	var errs form.Errors
	if errors.As(err, &errs) {
		s.renderMedicineEdit(w, r, user, http.StatusUnprocessableEntity, m, in, errs)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if changed {
		setFlash(w, r, "ok", "Medicamento atualizado. A alteração ficou registrada no histórico.")
	}
	http.Redirect(w, r, "/medicamentos", http.StatusSeeOther)
}

func (s *server) archiveMedicine(w http.ResponseWriter, r *http.Request, user auth.User) {
	s.setMedicineArchived(w, r, user, true)
}

func (s *server) restoreMedicine(w http.ResponseWriter, r *http.Request, user auth.User) {
	s.setMedicineArchived(w, r, user, false)
}

func (s *server) setMedicineArchived(w http.ResponseWriter, r *http.Request, user auth.User, archived bool) {
	m, ok := s.targetMedicine(w, r)
	if !ok {
		return
	}
	err := s.inventory.SetMedicineArchived(r.Context(), m.ID, archived, user.ID, s.now())
	switch {
	case errors.Is(err, stock.ErrStillInStock):
		setFlash(w, r, "error", "Este medicamento ainda tem estoque. Registre a saída (por exemplo, Vencimento/Descarte) antes de arquivar.")
	case err != nil:
		s.serverError(w, err)
		return
	case archived:
		setFlash(w, r, "ok", m.Label()+" arquivado.")
	default:
		setFlash(w, r, "ok", m.Label()+" restaurado.")
	}
	http.Redirect(w, r, "/medicamentos", http.StatusSeeOther)
}

func (s *server) deleteMedicine(w http.ResponseWriter, r *http.Request, _ auth.User) {
	m, ok := s.targetMedicine(w, r)
	if !ok {
		return
	}
	err := s.inventory.DeleteMedicine(r.Context(), m.ID)
	switch {
	case errors.Is(err, stock.ErrHasMovements):
		setFlash(w, r, "error", "Este medicamento já teve entrada ou saída. Arquive em vez de excluir.")
	case err != nil:
		s.serverError(w, err)
		return
	default:
		setFlash(w, r, "ok", m.Label()+" excluído.")
	}
	http.Redirect(w, r, "/medicamentos", http.StatusSeeOther)
}
