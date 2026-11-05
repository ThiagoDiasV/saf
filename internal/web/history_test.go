package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/auth"
	"saf/internal/stock"
)

// seedHistory: seedStock's Saldo inicial (dated 2026-10-01) and a Dispensação to Maria of 30 from
// B777, saved an hour later. Returns both movement ids.
func (app *testApp) seedHistory(t *testing.T) (entryID, exitID int64) {
	t.Helper()
	dipironaID := app.seedStock(t)
	require.NoError(t, app.conn.QueryRow(`SELECT id FROM movements WHERE type = 'Saldo inicial'`).Scan(&entryID))
	lots := app.dipironaLots(t, dipironaID)
	b777, _ := strconv.ParseInt(lots["B777"], 10, 64)
	exitID, err := app.inventory.RecordExit(context.Background(), stock.Exit{Type: "Dispensação", Date: "2026-10-07",
		PatientName: "Maria", Note: "UBS Centro", Lines: []stock.ExitLine{
			{Medicine: "Dipirona 500 mg comprimido", Quantity: "30", Takes: map[int64]string{b777: "30"}},
		}}, 1, app.now.Add(time.Hour))
	require.NoError(t, err)
	return entryID, exitID
}

func movementPath(id int64, action string) string {
	return "/movimentos/" + strconv.FormatInt(id, 10) + action
}

func TestHistoryPage(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	_, exitID := app.seedHistory(t)

	body := app.get("/historico", admin).Body.String()

	assert.Contains(t, body, `name="de" value="2026-09-07"`, "first visit shows the last 30 days")
	assert.Contains(t, body, `name="ate" value="2026-10-07"`)
	assert.Contains(t, body, "2 movimentações no filtro")
	assert.Contains(t, body, "nomes com mais de 6 meses são apagados")
	assert.Contains(t, body, "Dipirona 500 mg comprimido · 30")
	assert.Contains(t, body, `<td data-label="Dispensado para">Maria</td>`)
	assert.Contains(t, body, "07/10/2026 13:00", "recorded the same day: date and time")
	assert.Contains(t, body, "01/10/2026<span class=\"cell-sub\">registrado 07/10/2026 12:00</span>", "backdated entry")
	assert.Contains(t, body, "e mais 2 medicamentos")
	assert.Contains(t, body, `href="`+movementPath(exitID, "")+`"`)
	assert.Less(t, strings.Index(body, `<span class="cell-sub">Dispensação`), strings.Index(body, `<span class="cell-sub">Saldo inicial`), "newest first")
}

func TestHistoryFiltersAndAjustes(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.seedHistory(t)
	_, err := app.inventory.UpdateMedicine(context.Background(), 1, stock.MedicineInput{Ingredient: "Dipirona sódica",
		Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"}, 1, app.now)
	require.NoError(t, err)
	page := func(q url.Values) string { return app.get("/historico?"+q.Encode(), admin).Body.String() }

	body := page(url.Values{"de": {""}, "ate": {""}, "tipo": {"adjust"}})
	assert.Contains(t, body, "1 movimentação no filtro")
	assert.Contains(t, body, "princípio ativo Dipirona → Dipirona sódica")

	assert.Contains(t, page(url.Values{"tipo": {"out:Dispensação"}}), "1 movimentação no filtro")
	assert.Contains(t, page(url.Values{"paciente": {"maria"}}), "1 movimentação no filtro")
	assert.Contains(t, page(url.Values{"paciente": {"joão"}}), "Nenhuma movimentação com esses filtros.")
	assert.Contains(t, page(url.Values{"lote": {"k12"}}), "1 movimentação no filtro")
	body = page(url.Values{"tipo": {"in:Inventado"}})
	assert.Contains(t, body, "3 movimentações no filtro", "an unknown type is ignored")
	assert.Contains(t, body, `<option value="" selected>Todos</option>`)
}

func TestHistoryPagination(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.addMedicine(t, dipironaInput())
	for i := range 12 {
		_, err := app.inventory.RecordEntry(context.Background(), stock.Entry{Type: "Doação", Date: "2026-10-07",
			Lines: []stock.EntryLine{{Medicine: "Dipirona 500 mg comprimido", Lot: "L" + strconv.Itoa(i), Manufacturer: "EMS",
				Expiry: "03/2027", Quantity: "1"}}}, 1, app.now.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
	}

	first := app.get("/historico?tipo=in", admin).Body.String()
	assert.Equal(t, 10, strings.Count(first, ">Detalhes<"))
	assert.Contains(t, first, `<span aria-disabled="true">Anterior</span>`)
	assert.Contains(t, first, `href="/historico?ate=&amp;de=&amp;lote=&amp;medicamento=&amp;paciente=&amp;pagina=2&amp;tipo=in"`, "pages keep the filter")

	second := app.get("/historico?tipo=in&pagina=2", admin).Body.String()
	assert.Equal(t, 2, strings.Count(second, ">Detalhes<"))
	assert.Contains(t, second, `aria-current="page">2</a>`)
	assert.Equal(t, 2, strings.Count(app.get("/historico?tipo=in&pagina=99", admin).Body.String(), ">Detalhes<"), "past the end: last page")
}

func TestHistoryCSVLeavesNamesOut(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.seedHistory(t)

	res := app.get("/historico.csv?de=&ate=", admin)

	require.Equal(t, http.StatusOK, res.Code)
	assert.Equal(t, `attachment; filename="historico-2026-10-07.csv"`, res.Header().Get("Content-Disposition"))
	body := strings.TrimPrefix(res.Body.String(), "\xef\xbb\xbf")
	lines := strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n")
	assert.Equal(t, "Data do movimento;Registrado em;Movimento;Tipo;Medicamento;Lote;Fabricante;Validade;Quantidade;Unidade;Observação;Usuário;Estornada", lines[0])
	assert.Equal(t, `07/10/2026;07/10/2026 13:00;Saída;Dispensação;Dipirona 500 mg comprimido;B777;"'=HYPERLINK(""x"")";12/2026;-30;comprimido;UBS Centro;Paulo Mendes;`, lines[1])
	assert.Len(t, lines, 1+1+3, "header, the exit, the entry's three lots")
	assert.NotContains(t, body, "Maria", "first names never go into a CSV")
}

func TestMovementPage(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	_, exitID := app.seedHistory(t)

	body := app.get(movementPath(exitID, ""), admin).Body.String()

	assert.Contains(t, body, "Saída · Dispensação")
	assert.Contains(t, body, "<dt>Dispensado para</dt><dd>Maria</dd>")
	assert.Contains(t, body, "<dd>07/10/2026 13:00 por Paulo Mendes</dd>")
	assert.Contains(t, body, "30 comprimidos")
	assert.Contains(t, body, "UBS Centro")
	assert.Contains(t, body, `data-confirm-body="Devolve ao estoque: 30 comprimidos do lote B777 (Dipirona 500 mg comprimido). A saída original`)
	assert.Contains(t, body, "?editar=observacao")
	assert.Equal(t, http.StatusNotFound, app.get(movementPath(999, ""), admin).Code)
}

func TestMovementPageForViewer(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	_, exitID := app.seedHistory(t)
	viewer := app.login(t, "thiago.dias", testPassword)

	body := app.get(movementPath(exitID, "?editar=observacao"), viewer).Body.String()

	assert.Contains(t, body, "Maria", "the viewer sees first names (open item 3, default yes)")
	assert.NotContains(t, body, "<textarea", "no editing for the viewer, even asking")
	assert.NotContains(t, body, "Estornar")
	assert.Equal(t, http.StatusForbidden, app.post(movementPath(exitID, "/estorno"), nil, viewer).Code)
	assert.Equal(t, http.StatusForbidden, app.post(movementPath(exitID, "/observacao"), url.Values{"note": {"x"}}, viewer).Code)
}

func TestEditNote(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	_, exitID := app.seedHistory(t)

	assert.Contains(t, app.get(movementPath(exitID, "?editar=observacao"), admin).Body.String(), ">UBS Centro</textarea>")

	res := app.post(movementPath(exitID, "/observacao"), url.Values{"note": {strings.Repeat("x", 1001)}}, admin)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Use no máximo 1000 caracteres.")

	res = app.post(movementPath(exitID, "/observacao"), url.Values{"note": {"UBS Centro, receita 123"}}, admin)
	require.Equal(t, http.StatusSeeOther, res.Code)
	body := app.get(movementPath(exitID, ""), admin, cookieNamed(res, flashCookie)).Body.String()
	assert.Contains(t, body, "Observação atualizada.")
	assert.Contains(t, body, "Registro de alterações")
	assert.Contains(t, body, "observação: “UBS Centro” → “UBS Centro, receita 123”")
}

func TestReverseFromTheDetailPage(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	entryID, exitID := app.seedHistory(t)

	entry := app.get(movementPath(entryID, ""), admin).Body.String()
	assert.Contains(t, entry, "Não dá para estornar:", "30 of B777 already left")
	assert.Contains(t, entry, "Lote B777 (Dipirona 500 mg comprimido): há 120 comprimidos, o estorno retiraria 150.")
	assert.NotContains(t, entry, `action="`+movementPath(entryID, "/estorno")+`"`)

	res := app.post(movementPath(exitID, "/estorno"), nil, admin)
	require.Equal(t, http.StatusSeeOther, res.Code)
	body := app.get(movementPath(exitID, ""), admin, cookieNamed(res, flashCookie)).Body.String()
	assert.Contains(t, body, "Saída estornada. O estoque foi atualizado.")
	assert.Contains(t, body, "Estornada em")
	assert.NotContains(t, body, `data-confirm-label="Estornar"`)

	res = app.post(movementPath(exitID, "/estorno"), nil, admin)
	assert.Contains(t, app.get(movementPath(exitID, ""), admin, cookieNamed(res, flashCookie)).Body.String(), "Esta movimentação já foi estornada.")
	assert.Contains(t, app.get("/historico", admin).Body.String(), "Estornada")
	assert.Contains(t, app.get("/historico", admin).Body.String(), "da saída de 07/10/2026")
}
