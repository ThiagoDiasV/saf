package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/auth"
	"saf/internal/stock"
)

// dipironaLots returns the lot ids of seedStock's Dipirona: X999 (expired, 20), B777 (150), K12 (100).
func (app *testApp) dipironaLots(t *testing.T, medicineID int64) map[string]string {
	t.Helper()
	batches, err := app.inventory.AvailableBatches(context.Background(), medicineID, app.now)
	require.NoError(t, err)
	ids := map[string]string{}
	for _, b := range batches {
		ids[b.Lot] = strconv.FormatInt(b.ID, 10)
	}
	return ids
}

func TestExitPage(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.seedStock(t)

	body := app.get("/saida", admin).Body.String()

	assert.Contains(t, body, "<option>Vencimento/Descarte</option>")
	assert.Contains(t, body, `<label class="field" id="field-patient" hidden>`, "shown only for Dispensação")
	assert.Contains(t, body, `name="lines.0.medicine"`)
	assert.Contains(t, body, `name="lines.-1.medicine"`, "template for new lines, renamed by the browser")
	assert.Contains(t, body, `data-next-index="1"`)
	assert.Contains(t, body, "Não escreva dados de pacientes aqui.")
}

func TestExitBatchesFragmentPrefillsFEFO(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	lots := app.dipironaLots(t, app.seedStock(t))
	fragment := func(query url.Values) *httptest.ResponseRecorder {
		return app.get("/saida/lotes?"+query.Encode(), admin)
	}
	query := url.Values{"linha": {"2"}, "medicamento": {"Dipirona 500 mg comprimido"}, "quantidade": {"160"}, "tipo": {"Dispensação"}}

	res := fragment(query)

	require.Equal(t, http.StatusOK, res.Code)
	body := res.Body.String()
	assert.NotContains(t, body, "<html", "a piece of the page, not a page")
	assert.Contains(t, body, `name="lines.2.take.`+lots["X999"]+`" type="number" min="0" max="20" step="1" inputmode="numeric" value="0" aria-label="Retirar do lote X999" disabled>`)
	assert.Contains(t, body, "Só sai por Vencimento/Descarte")
	assert.Contains(t, body, `name="lines.2.take.`+lots["B777"]+`" type="number" min="0" max="150" step="1" inputmode="numeric" value="150"`)
	assert.Contains(t, body, `name="lines.2.take.`+lots["K12"]+`" type="number" min="0" max="100" step="1" inputmode="numeric" value="10"`)
	assert.Less(t, strings.Index(body, ">X999<"), strings.Index(body, ">K12<"), "closest expiry first")

	query.Set("tipo", "Vencimento/Descarte")
	query.Set("quantidade", "20")
	body = fragment(query).Body.String()
	assert.Contains(t, body, `name="lines.2.take.`+lots["X999"]+`" type="number" min="0" max="20" step="1" inputmode="numeric" value="20" aria-label="Retirar do lote X999">`,
		"a discard takes the expired lot first")

	query.Set("medicamento", "Cetoconazol 20 mg/g creme")
	assert.Contains(t, fragment(query).Body.String(), "Sem estoque deste medicamento.")
	query.Set("medicamento", "Ibuprofeno")
	assert.Empty(t, strings.TrimSpace(fragment(query).Body.String()), "no such medicine: no lots")
	query.Set("linha", "-1")
	assert.Equal(t, http.StatusBadRequest, fragment(query).Code)

	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	viewer := app.login(t, "thiago.dias", testPassword)
	assert.Equal(t, http.StatusForbidden, app.get("/saida/lotes?linha=0", viewer).Code)
}

func TestRecordExit(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	dipironaID := app.seedStock(t)
	lots := app.dipironaLots(t, dipironaID)

	res := app.post("/saida", url.Values{
		"type": {"Dispensação"}, "date": {"2026-10-07"}, "patient_name": {"Maria"},
		"lines.3.medicine": {"Dipirona 500 mg comprimido"}, "lines.3.quantity": {"12"},
		"lines.3.take." + lots["B777"]: {"10"}, "lines.3.take." + lots["K12"]: {"2"},
		"lines.7.medicine": {""}, "lines.7.quantity": {""},
	}, admin)

	require.Equal(t, http.StatusSeeOther, res.Code, res.Body.String())
	assert.Contains(t, app.get("/saida", admin, cookieNamed(res, flashCookie)).Body.String(), "Saída registrada: dispensação.")
	m, err := app.inventory.Medicine(context.Background(), dipironaID)
	require.NoError(t, err)
	assert.Equal(t, int64(258), m.OnHand)

	var movementID int64
	require.NoError(t, app.conn.QueryRow(`SELECT id FROM movements WHERE kind = 'out'`).Scan(&movementID))
	name, err := app.inventory.PatientName(context.Background(), movementID)
	require.NoError(t, err)
	assert.Equal(t, "Maria", name)
}

func TestRecordExitErrorsKeepTheForm(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	lots := app.dipironaLots(t, app.seedStock(t))

	res := app.post("/saida", url.Values{
		"type": {"Dispensação"}, "date": {"2026-10-07"}, "patient_name": {"Maria Silva"},
		"lines.4.medicine": {"Dipirona 500 mg comprimido"}, "lines.4.quantity": {"200"},
		"lines.4.take." + lots["B777"]: {"151"}, "lines.4.take." + lots["K12"]: {"49"},
	}, admin)

	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	body := res.Body.String()
	assert.Contains(t, body, "Corrija os campos destacados.")
	assert.Contains(t, body, "Use só o primeiro nome, sem sobrenome.")
	assert.Contains(t, body, `<label class="field" id="field-patient">`, "name field stays open for Dispensação")
	assert.Contains(t, body, "Máximo 150.")
	assert.Contains(t, body, `name="lines.0.take.`+lots["B777"]+`" type="number" min="0" max="150" step="1" inputmode="numeric" value="151"`,
		"typed takes come back, renumbered from 0")
	assert.Contains(t, body, `data-next-index="1"`)
}

func TestExitLinesParsing(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/saida", strings.NewReader(url.Values{
		"lines.9.medicine": {"B"}, "lines.9.quantity": {"2"}, "lines.9.take.5": {"2"},
		"lines.2.medicine": {"A"}, "lines.2.quantity": {"1"}, "lines.2.take.4": {"1"}, "lines.2.take.x": {"7"},
		"lines.5.medicine": {""}, "lines.5.quantity": {" "},
		"lines.999.medicine": {"too far"}, "lines.abc.medicine": {"bad"}, "type": {"Perda"},
	}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.NoError(t, r.ParseForm())

	assert.Equal(t, []stock.ExitLine{
		{Medicine: "A", Quantity: "1", Takes: map[int64]string{4: "1"}},
		{Medicine: "B", Quantity: "2", Takes: map[int64]string{5: "2"}},
	}, exitLines(r))
}
