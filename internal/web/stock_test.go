package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/auth"
	"saf/internal/stock"
)

// seedStock: Dipirona with an expired (X999), an expiring (B777) and a good lot (K12);
// Cetoconazol never received. Today is 2026-10-07.
func (app *testApp) seedStock(t *testing.T) (dipironaID int64) {
	t.Helper()
	dipironaID = app.addMedicine(t, dipironaInput())
	app.addMedicine(t, stock.MedicineInput{Ingredient: "Cetoconazol", Strength: "20 mg/g", DosageForm: "Creme", Unit: "bisnaga"})
	_, err := app.inventory.RecordEntry(context.Background(), stock.Entry{Type: "Saldo inicial", Date: "2026-10-01", Lines: []stock.EntryLine{
		{Medicine: "Dipirona 500 mg comprimido", Lot: "K12", Manufacturer: "Prati-Donaduzzi", Expiry: "08/2027", Quantity: "100"},
		{Medicine: "Dipirona 500 mg comprimido", Lot: "X999", Manufacturer: "EMS", Expiry: "09/2026", Quantity: "20"},
		{Medicine: "Dipirona 500 mg comprimido", Lot: "B777", Manufacturer: "=HYPERLINK(\"x\")", Expiry: "12/2026", Quantity: "150"},
	}}, 1, app.now)
	require.NoError(t, err)
	return dipironaID
}

func TestStockPage(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	dipironaID := app.seedStock(t)

	body := app.get("/estoque", admin).Body.String()

	assert.Contains(t, body, "2 medicamentos · 3 lotes · hoje, 07/10/2026")
	assert.Contains(t, app.get("/estoque?q=cetoconazol", admin).Body.String(), "2 medicamentos", "the summary counts everything")
	assert.Contains(t, body, `<details class="card med" id="med-`+strconv.FormatInt(dipironaID, 10)+`">`, "groups start collapsed")
	assert.Contains(t, body, "270 comprimidos")
	assert.Contains(t, body, "20 comprimidos em lote vencido, não dispensáveis")
	assert.Contains(t, body, "1 lote vencido")
	assert.Contains(t, body, "1 lote vencendo")
	assert.Contains(t, body, "VENCIDO")
	assert.Contains(t, body, "vence em 85 dias", "B777 is good until 31/12/2026")
	assert.Contains(t, body, "Sem estoque", "Cetoconazol never received")
	assert.Less(t, strings.Index(body, ">X999<"), strings.Index(body, ">B777<"), "closest expiry first")
	assert.Contains(t, body, `href="/lotes/`, "admin can edit lots")
	assert.Contains(t, body, `<span class="count">1</span>`)
}

func TestStockPageForViewer(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	app.seedStock(t)
	viewer := app.login(t, "thiago.dias", testPassword)

	body := app.get("/estoque", viewer).Body.String()

	assert.Contains(t, body, "270 comprimidos")
	assert.NotContains(t, body, `href="/lotes/`)
	assert.NotContains(t, body, "Nova entrada")
	assert.Equal(t, http.StatusOK, app.get("/estoque.csv", viewer).Code, "viewer may export the stock")
}

func TestStockFiltersOpenTheGroups(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.seedStock(t)

	body := app.get("/estoque?validade=vencidos", admin).Body.String()

	assert.Contains(t, body, " open>", "a filter opens what it finds")
	assert.Contains(t, body, ">X999<")
	assert.NotContains(t, body, ">K12<")
	assert.NotContains(t, body, "Cetoconazol", "nothing expired there")
	assert.Contains(t, body, `href="/estoque.csv?q=&amp;validade=vencidos"`, "CSV keeps the filter")

	body = app.get("/estoque?q=prati", admin).Body.String()
	assert.Contains(t, body, ">K12<")
	assert.NotContains(t, body, ">X999<")
}

func TestStockCSV(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.seedStock(t)

	res := app.get("/estoque.csv", admin)

	require.Equal(t, http.StatusOK, res.Code)
	assert.Equal(t, "text/csv; charset=utf-8", res.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="estoque-2026-10-07.csv"`, res.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", res.Header().Get("Cache-Control"))
	body := res.Body.String()
	require.True(t, strings.HasPrefix(body, "\xef\xbb\xbf"), "byte order mark for Excel")
	assert.Equal(t, strings.Join([]string{
		"Medicamento;Unidade;Lote;Fabricante;Validade;Quantidade;Situação",
		"Cetoconazol 20 mg/g creme;bisnaga;;;;0;Sem estoque",
		"Dipirona 500 mg comprimido;comprimido;X999;EMS;09/2026;20;Vencido",
		`Dipirona 500 mg comprimido;comprimido;B777;"'=HYPERLINK(""x"")";12/2026;150;Vence em 85 dias`,
		"Dipirona 500 mg comprimido;comprimido;K12;Prati-Donaduzzi;08/2027;100;Dentro da validade",
		"",
	}, "\r\n"), strings.TrimPrefix(body, "\xef\xbb\xbf"))

	filtered := app.get("/estoque.csv?validade=vencidos", admin).Body.String()
	assert.Equal(t, 2, strings.Count(filtered, "\r\n"), "header + X999")
}

func TestCSVCell(t *testing.T) {
	for in, want := range map[string]string{
		"EMS": "EMS", "-20": "-20", "150": "150", "=1+1": "'=1+1", "+55 85": "'+55 85", "@SUM(A1)": "'@SUM(A1)", "-x": "'-x", "": "",
	} {
		assert.Equal(t, want, csvCell(in), in)
	}
}

func TestEditBatch(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	dipironaID := app.seedStock(t)
	groups, _, err := app.inventory.StockGroups(context.Background(), stock.StockFilter{Query: "K12"}, app.now)
	require.NoError(t, err)
	path := "/lotes/" + strconv.FormatInt(groups[0].Batches[0].ID, 10)

	page := app.get(path+"/editar", admin).Body.String()
	assert.Contains(t, page, `value="K12"`)
	assert.Contains(t, page, `value="08/2027"`)

	res := app.post(path, url.Values{"lot": {"K12"}, "manufacturer": {"Prati-Donaduzzi"}, "expiry": {"8/27"}}, admin)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Use o formato MM/AAAA.")

	res = app.post(path, url.Values{"lot": {"K13"}, "manufacturer": {"Prati-Donaduzzi"}, "expiry": {"08/2027"}}, admin)
	require.Equal(t, http.StatusSeeOther, res.Code)
	assert.Equal(t, "/estoque#med-"+strconv.FormatInt(dipironaID, 10), res.Header().Get("Location"))
	assert.Contains(t, app.get("/estoque", admin, cookieNamed(res, flashCookie)).Body.String(),
		"Lote atualizado. A alteração ficou registrada no histórico.")

	assert.Equal(t, http.StatusNotFound, app.get("/lotes/999/editar", admin).Code)
	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	viewer := app.login(t, "thiago.dias", testPassword)
	assert.Equal(t, http.StatusForbidden, app.get(path+"/editar", viewer).Code)
}

func TestPlural(t *testing.T) {
	for unit, want := range map[string]string{
		"comprimido": "comprimidos", "cápsula": "cápsulas", "sachê": "sachês", "frasco conta-gotas": "frascos conta-gotas",
		"blister": "blisteres", "tubo": "tubos", "unidades": "unidades", "flaconete": "flaconetes",
	} {
		assert.Equal(t, want, plural(unit, 2), unit)
	}
	assert.Equal(t, "comprimido", plural("comprimido", 1))
	assert.Equal(t, "comprimido", plural("comprimido", -1))
}
