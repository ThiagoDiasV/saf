package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/auth"
	"saf/internal/stock"
)

func dipironaForm() url.Values {
	return url.Values{"ingredient": {"Dipirona"}, "strength": {"500 mg"}, "dosage_form": {"Comprimido"}, "unit": {"comprimido"}}
}

// adminSession creates an admin, logs in and returns the session cookie.
func adminSession(t *testing.T, app *testApp) *http.Cookie {
	t.Helper()
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	return app.login(t, "paulo.mendes", testPassword)
}

func (app *testApp) addMedicine(t *testing.T, in stock.MedicineInput) int64 {
	t.Helper()
	id, err := app.inventory.CreateMedicine(context.Background(), in, t0)
	require.NoError(t, err)
	return id
}

// move puts quantity units of medicineID through a raw movement, until Entrada exists.
func (app *testApp) move(t *testing.T, medicineID, quantity int64) {
	t.Helper()
	_, err := app.conn.Exec(`INSERT INTO batches (medicine_id, lot, manufacturer, expiry_month, match_key)
		VALUES (?, 'A123', 'EMS', '2027-03', 'a123ems') ON CONFLICT (medicine_id, match_key) DO NOTHING`, medicineID)
	require.NoError(t, err)
	res, err := app.conn.Exec(`INSERT INTO movements (kind, type, occurred_on, recorded_at, user_id)
		VALUES ('in', 'Doação', '2026-10-07', '2026-10-07T15:00:00Z', 1)`)
	require.NoError(t, err)
	movementID, _ := res.LastInsertId()
	_, err = app.conn.Exec(`INSERT INTO movement_lines (movement_id, batch_id, quantity)
		VALUES (?, (SELECT id FROM batches WHERE medicine_id = ?), ?)`, movementID, medicineID, quantity)
	require.NoError(t, err)
}

func medicinePath(id int64, action string) string {
	return "/medicamentos/" + strconv.FormatInt(id, 10) + action
}

func TestCreateMedicine(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)

	res := app.post("/medicamentos", dipironaForm(), admin)

	require.Equal(t, http.StatusSeeOther, res.Code)
	page := app.get("/medicamentos", admin, cookieNamed(res, flashCookie)).Body.String()
	assert.Contains(t, page, "Dipirona 500 mg comprimido cadastrado. Já aparece em Entrada.")
	assert.Contains(t, page, "<strong>Dipirona</strong> 500 mg · comprimido")
	assert.Contains(t, page, "Nunca movimentado")
	assert.Contains(t, page, `<option value="Dipirona">`, "autocomplete learns the new ingredient")
	assert.Contains(t, page, `data-confirm="Excluir Dipirona 500 mg comprimido?"`)
}

func TestCreateMedicineErrors(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)

	res := app.post("/medicamentos", url.Values{"ingredient": {"Dipirona"}}, admin)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	body := res.Body.String()
	assert.Contains(t, body, "Informe a concentração.")
	assert.Contains(t, body, "Informe a unidade de contagem.")
	assert.Contains(t, body, `value="Dipirona"`, "typed values are kept")

	app.post("/medicamentos", dipironaForm(), admin)
	res = app.post("/medicamentos", url.Values{"ingredient": {"DIPIRONA"}, "strength": {"500mg"}, "dosage_form": {"comprimido"}, "unit": {"cx"}}, admin)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Já existe: Dipirona 500 mg comprimido. Maiúsculas, acentos e espaços não contam.")
}

func TestMedicinesSearchAndArchivedFilter(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.addMedicine(t, stock.MedicineInput{Ingredient: "Dipirona sódica", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	paracetamol := app.addMedicine(t, stock.MedicineInput{Ingredient: "Paracetamol", Strength: "750 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	require.NoError(t, app.inventory.SetMedicineArchived(context.Background(), paracetamol, true, 1, t0))

	page := app.get("/medicamentos", admin).Body.String()
	assert.Contains(t, page, "Dipirona sódica")
	assert.NotContains(t, page, "<strong>Paracetamol</strong>", "archived medicines are hidden by default")

	page = app.get("/medicamentos?arquivados=1", admin).Body.String()
	assert.Contains(t, page, "<strong>Paracetamol</strong>")
	assert.Contains(t, page, "Arquivado")
	assert.Contains(t, page, "Restaurar")

	page = app.get("/medicamentos?q=SODICA", admin).Body.String()
	assert.Contains(t, page, "Dipirona sódica")
	page = app.get("/medicamentos?q=ibuprofeno", admin).Body.String()
	assert.Contains(t, page, "Nenhum medicamento encontrado.")
}

func TestEditMedicine(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	id := app.addMedicine(t, stock.MedicineInput{Ingredient: "Dipirna", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"})

	page := app.get(medicinePath(id, "/editar"), admin).Body.String()
	assert.Contains(t, page, `value="Dipirna"`)

	res := app.post(medicinePath(id, ""), url.Values{"ingredient": {""}, "strength": {"500 mg"}, "dosage_form": {"Comprimido"}, "unit": {"comprimido"}}, admin)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Informe o princípio ativo.")

	res = app.post(medicinePath(id, ""), dipironaForm(), admin)
	require.Equal(t, http.StatusSeeOther, res.Code)
	assert.Contains(t, app.get("/medicamentos", admin, cookieNamed(res, flashCookie)).Body.String(),
		"Medicamento atualizado. A alteração ficou registrada no histórico.")

	res = app.post(medicinePath(id, ""), dipironaForm(), admin)
	assert.Nil(t, cookieNamed(res, flashCookie), "nothing changed, nothing to say")

	assert.Equal(t, http.StatusNotFound, app.get(medicinePath(999, "/editar"), admin).Code)
}

func TestArchiveRestoreAndDelete(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	inStock := app.addMedicine(t, dipironaInput())
	emptied := app.addMedicine(t, stock.MedicineInput{Ingredient: "Paracetamol", Strength: "750 mg", DosageForm: "Comprimido", Unit: "comprimido"})
	neverMoved := app.addMedicine(t, stock.MedicineInput{Ingredient: "Cetoconazol", Strength: "20 mg/g", DosageForm: "Creme", Unit: "bisnaga"})
	app.move(t, inStock, 30)
	app.move(t, emptied, 10)
	app.move(t, emptied, -10)

	page := app.get("/medicamentos", admin).Body.String()
	assert.Contains(t, page, "Zere o estoque para arquivar", "archive is offered but disabled while there is stock")

	flashAfter := func(path string) string {
		res := app.post(path, nil, admin)
		require.Equal(t, http.StatusSeeOther, res.Code, path)
		return app.get("/medicamentos?arquivados=1", admin, cookieNamed(res, flashCookie)).Body.String()
	}
	assert.Contains(t, flashAfter(medicinePath(inStock, "/arquivar")),
		"Este medicamento ainda tem estoque. Registre a saída (por exemplo, Vencimento/Descarte) antes de arquivar.")
	assert.Contains(t, flashAfter(medicinePath(emptied, "/arquivar")), "Paracetamol 750 mg comprimido arquivado.")
	assert.Contains(t, flashAfter(medicinePath(emptied, "/restaurar")), "Paracetamol 750 mg comprimido restaurado.")
	assert.Contains(t, flashAfter(medicinePath(emptied, "/excluir")), "Este medicamento já teve entrada ou saída. Arquive em vez de excluir.")
	assert.Contains(t, flashAfter(medicinePath(neverMoved, "/excluir")), "Cetoconazol 20 mg/g creme excluído.")
	assert.Equal(t, http.StatusNotFound, app.post(medicinePath(neverMoved, "/arquivar"), nil, admin).Code)
}

func dipironaInput() stock.MedicineInput {
	return stock.MedicineInput{Ingredient: "Dipirona", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"}
}
