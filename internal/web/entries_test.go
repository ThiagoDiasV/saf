package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/stock"
)

func TestEntryPage(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.addMedicine(t, dipironaInput())

	body := app.get("/entrada", admin).Body.String()

	assert.Contains(t, body, `<option value="Dipirona 500 mg comprimido" data-unit="comprimidos" data-unit-one="comprimido">`)
	assert.Contains(t, body, `name="date" value="2026-10-07" max="2026-10-07"`)
	assert.Contains(t, body, "<option>Saldo inicial</option>")
	assert.Equal(t, 1+1, strings.Count(body, `class="line"`), "one line on the form, one in the template for new lines")
}

func TestRecordEntry(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	dipironaID := app.addMedicine(t, dipironaInput())
	paracetamolID := app.addMedicine(t, stock.MedicineInput{Ingredient: "Paracetamol", Strength: "750 mg", DosageForm: "Comprimido", Unit: "comprimido"})

	res := app.post("/entrada", url.Values{
		"type": {"Doação"}, "date": {"2026-10-06"}, "note": {"Clínica Escola"},
		"medicine":     {"Dipirona 500 mg comprimido", "", "Paracetamol 750 mg comprimido"},
		"lot":          {"A123", "", "P750"},
		"manufacturer": {"EMS", "", "Medley"},
		"expiry":       {"03/2027", "", "06/2027"},
		"quantity":     {"200", "", "40"},
	}, admin)

	require.Equal(t, http.StatusSeeOther, res.Code, res.Body.String())
	assert.Equal(t, "/entrada", res.Header().Get("Location"))
	assert.Contains(t, app.get("/entrada", admin, cookieNamed(res, flashCookie)).Body.String(),
		"Entrada registrada com 2 medicamentos.", "the empty middle row is ignored")
	for id, want := range map[int64]int64{dipironaID: 200, paracetamolID: 40} {
		m, err := app.inventory.Medicine(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, want, m.OnHand, m.Label())
	}
}

func TestRecordEntryErrorsKeepTheForm(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.addMedicine(t, dipironaInput())

	res := app.post("/entrada", url.Values{
		"type": {"Doação"}, "date": {"2026-10-07"},
		"medicine":     {"Dipirona 500 mg comprimido", "Ibuprofeno"},
		"lot":          {"A123", "I1"},
		"manufacturer": {"EMS", "EMS"},
		"expiry":       {"3/2027", "05/2027"},
		"quantity":     {"200", "10"},
	}, admin)

	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	body := res.Body.String()
	assert.Contains(t, body, "Corrija os campos destacados.")
	assert.Contains(t, body, "Validade no formato MM/AAAA.")
	assert.Contains(t, body, "Escolha um medicamento da lista. Se não existir, cadastre em Medicamentos.")
	assert.Contains(t, body, `value="3/2027" inputmode="numeric" placeholder="MM/AAAA" maxlength="7" data-mask="expiry" autocomplete="off" aria-invalid="true"`)
	assert.Contains(t, body, `value="Ibuprofeno"`)
	assert.Equal(t, 2+1, strings.Count(body, `class="line"`), "both lines come back")
	assert.Contains(t, body, "<option selected>Doação</option>")
}

func TestRecordEntryRefusesAnotherExpiryForALot(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.seedStock(t)

	res := app.post("/entrada", url.Values{
		"type": {"Doação"}, "date": {"2026-10-07"},
		"medicine": {"Dipirona 500 mg comprimido"}, "lot": {"k12"}, "manufacturer": {"prati-donaduzzi"},
		"expiry": {"09/2027"}, "quantity": {"10"},
	}, admin)

	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "O lote K12 (Prati-Donaduzzi) já existe com validade 08/2027. Corrija aqui ou edite o lote no Estoque.")
}
