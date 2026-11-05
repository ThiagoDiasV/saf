package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"saf/internal/auth"
	"saf/internal/db"
	"saf/internal/seal"
	"saf/internal/stock"
)

var t0 = time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC) // 12:00 in Fortaleza

const testPassword = "farmacia-2026!"

// testApp is the whole web stack on a real SQLite database in a temp dir, with a clock the
// test can move.
type testApp struct {
	handler   http.Handler
	conn      *sql.DB
	accounts  *auth.Accounts
	inventory *stock.Inventory
	now       time.Time
}

func newTestApp(t *testing.T, domain string) *testApp {
	t.Helper()
	conn, err := db.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	box, err := seal.New(make([]byte, 32)) // a fixed key is fine for tests
	require.NoError(t, err)
	app := &testApp{conn: conn, accounts: auth.NewAccounts(conn, bcrypt.MinCost), inventory: stock.NewInventory(conn, box, t.TempDir()), now: t0}
	app.handler = Handler(Config{Accounts: app.accounts, Inventory: app.inventory, Domain: domain,
		Now: func() time.Time { return app.now }})
	return app
}

func (app *testApp) addUser(t *testing.T, name, login, role string) int64 {
	t.Helper()
	id, err := app.accounts.Create(context.Background(),
		auth.NewUser{Name: name, Login: login, Password: testPassword, Role: role}, t0)
	require.NoError(t, err)
	return id
}

func (app *testApp) serve(r *http.Request, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	for _, c := range cookies {
		r.AddCookie(c)
	}
	res := httptest.NewRecorder()
	app.handler.ServeHTTP(res, r)
	return res
}

func (app *testApp) get(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return app.serve(httptest.NewRequest(http.MethodGet, path, nil), cookies...)
}

func (app *testApp) post(path string, values url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return app.serve(r, cookies...)
}

// login logs in through the login form and returns the session cookie.
func (app *testApp) login(t *testing.T, login, password string) *http.Cookie {
	t.Helper()
	res := app.post("/login", url.Values{"login": {login}, "password": {password}})
	require.Equal(t, http.StatusSeeOther, res.Code, res.Body.String())
	c := cookieNamed(res, sessionCookie)
	require.NotNil(t, c)
	return c
}

func cookieNamed(res *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range res.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestRootRedirectsToEstoque(t *testing.T) {
	res := newTestApp(t, "").get("/")

	assert.Equal(t, http.StatusSeeOther, res.Code)
	assert.Equal(t, "/estoque", res.Header().Get("Location"))
}

func TestPagesNeedLogin(t *testing.T) {
	app := newTestApp(t, "")

	for _, path := range []string{"/estoque", "/historico", "/usuarios", "/minha-senha"} {
		res := app.get(path)
		assert.Equal(t, http.StatusSeeOther, res.Code, path)
		assert.Equal(t, "/login", res.Header().Get("Location"), path)
	}
	res := app.post("/usuarios", url.Values{"name": {"Intrusa"}})
	assert.Equal(t, "/login", res.Header().Get("Location"))
}

func TestLoginStartsSession(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	assert.Contains(t, app.get("/login").Body.String(), `action="/login"`)

	res := app.post("/login", url.Values{"login": {"paulo.mendes"}, "password": {testPassword}})

	require.Equal(t, http.StatusSeeOther, res.Code)
	assert.Equal(t, "/estoque", res.Header().Get("Location"))
	c := cookieNamed(res, sessionCookie)
	require.NotNil(t, c)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Equal(t, "/", c.Path)
	assert.Equal(t, 12*60*60, c.MaxAge)

	page := app.get("/estoque", c)
	assert.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), "Paulo Mendes")
	assert.Equal(t, "/estoque", app.get("/login", c).Header().Get("Location"), "already logged in")
}

func TestLoginErrors(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)

	res := app.post("/login", url.Values{"login": {"paulo.mendes"}, "password": {"senha-errada-123"}})

	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Usuário ou senha incorretos.")
	assert.Contains(t, res.Body.String(), `value="paulo.mendes"`, "login is kept, password is not")
	assert.Nil(t, cookieNamed(res, sessionCookie))

	for range 4 {
		res = app.post("/login", url.Values{"login": {"paulo.mendes"}, "password": {"senha-errada-123"}})
	}
	assert.Contains(t, res.Body.String(), "Muitas tentativas. Aguarde 15 minutos e tente de novo.")
}

func TestLogoutEndsSession(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	c := app.login(t, "paulo.mendes", testPassword)

	res := app.post("/sair", nil, c)

	assert.Equal(t, http.StatusSeeOther, res.Code)
	assert.Equal(t, "/login", res.Header().Get("Location"))
	assert.Equal(t, `"cache"`, res.Header().Get("Clear-Site-Data"))
	assert.Less(t, cookieNamed(res, sessionCookie).MaxAge, 0, "cookie removed")
	assert.Equal(t, "/login", app.get("/estoque", c).Header().Get("Location"), "old cookie no longer works")
}

func TestSessionExpiresAfterTwelveHours(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	c := app.login(t, "paulo.mendes", testPassword)

	app.now = t0.Add(12 * time.Hour)

	assert.Equal(t, "/login", app.get("/estoque", c).Header().Get("Location"))
}

func TestViewerSeesOnlyEstoqueAndHistorico(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	c := app.login(t, "thiago.dias", testPassword)

	body := app.get("/historico", c).Body.String()
	assert.Contains(t, body, `href="/estoque"`)
	assert.Contains(t, body, `<a class="tab" href="/historico" aria-current="page">`)
	assert.Contains(t, body, "Somente visualização")
	for _, hidden := range []string{`href="/entrada"`, `href="/saida"`, `href="/medicamentos"`, `href="/usuarios"`, `aria-controls="more-menu"`} {
		assert.NotContains(t, body, hidden)
	}

	for _, path := range []string{"/entrada", "/saida", "/medicamentos", "/usuarios", "/usuarios/1/senha"} {
		res := app.get(path, c)
		assert.Equal(t, http.StatusForbidden, res.Code, path)
		assert.Contains(t, res.Body.String(), "Sem permissão", path)
	}
	res := app.post("/usuarios", url.Values{"name": {"Intrusa"}, "login": {"intrusa"}, "password": {testPassword}, "role": {"admin"}}, c)
	assert.Equal(t, http.StatusForbidden, res.Code)
	users, err := app.accounts.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, users, 1, "nobody was created")
}

func TestAdminSeesEveryTab(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	c := app.login(t, "paulo.mendes", testPassword)

	for _, path := range []string{"/estoque", "/entrada", "/saida", "/historico", "/medicamentos", "/usuarios"} {
		res := app.get(path, c)
		require.Equal(t, http.StatusOK, res.Code, path)
		body := res.Body.String()
		assert.Contains(t, body, `<a class="tab" href="`+path+`" aria-current="page">`, path)
		assert.Equal(t, 1, strings.Count(body, `aria-current="page"`), path)
		assert.Contains(t, body, `<a href="/medicamentos">`, "phone Mais menu")
		assert.NotContains(t, body, "Somente visualização")
	}
}

func TestCreateUser(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	c := app.login(t, "paulo.mendes", testPassword)

	res := app.post("/usuarios", url.Values{"name": {"Maria Souza"}, "login": {"Ma"}, "password": {"curta"}, "role": {"viewer"}}, c)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	body := res.Body.String()
	assert.Contains(t, body, "Use de 3 a 32 caracteres")
	assert.Contains(t, body, "A senha precisa de pelo menos 12 caracteres.")
	assert.Contains(t, body, `value="Maria Souza"`, "name is kept")

	res = app.post("/usuarios", url.Values{"name": {"Maria Souza"}, "login": {"maria.souza"}, "password": {testPassword}, "role": {"viewer"}}, c)
	require.Equal(t, http.StatusSeeOther, res.Code)
	toast := cookieNamed(res, flashCookie)
	require.NotNil(t, toast)

	page := app.get("/usuarios", c, toast).Body.String()
	assert.Contains(t, page, "Usuário maria.souza criado.")
	assert.Contains(t, page, "<strong>Maria Souza</strong>")
	assert.NotContains(t, app.get("/usuarios", c).Body.String(), "criado.", "a toast shows once")
}

func TestDeactivateAndReactivate(t *testing.T) {
	app := newTestApp(t, "")
	adminID := app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	viewerID := app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	admin := app.login(t, "paulo.mendes", testPassword)
	viewer := app.login(t, "thiago.dias", testPassword)

	res := app.post("/usuarios/"+strconv.FormatInt(viewerID, 10)+"/ativo", url.Values{"active": {"0"}}, admin)
	assert.Equal(t, http.StatusSeeOther, res.Code)
	assert.Contains(t, app.get("/usuarios", admin, cookieNamed(res, flashCookie)).Body.String(), "Thiago Dias desativado.")
	assert.Equal(t, "/login", app.get("/estoque", viewer).Header().Get("Location"), "session ended at once")

	res = app.post("/usuarios/"+strconv.FormatInt(adminID, 10)+"/ativo", url.Values{"active": {"0"}}, admin)
	page := app.get("/usuarios", admin, cookieNamed(res, flashCookie)).Body.String()
	assert.Contains(t, page, "Você não pode desativar o próprio usuário.")
	assert.Contains(t, page, "toast-error")

	res = app.post("/usuarios/"+strconv.FormatInt(viewerID, 10)+"/ativo", url.Values{"active": {"1"}}, admin)
	assert.Contains(t, app.get("/usuarios", admin, cookieNamed(res, flashCookie)).Body.String(), "Thiago Dias pode entrar de novo.")
	app.login(t, "thiago.dias", testPassword)
}

func TestResetPassword(t *testing.T) {
	app := newTestApp(t, "")
	adminID := app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	viewerID := app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	admin := app.login(t, "paulo.mendes", testPassword)
	viewer := app.login(t, "thiago.dias", testPassword)
	path := "/usuarios/" + strconv.FormatInt(viewerID, 10) + "/senha"

	assert.Contains(t, app.get(path, admin).Body.String(), "Thiago Dias · thiago.dias")
	res := app.post(path, url.Values{"password": {"curta"}}, admin)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "A senha precisa de pelo menos 12 caracteres.")

	res = app.post(path, url.Values{"password": {"nova-senha-segura"}}, admin)
	assert.Equal(t, http.StatusSeeOther, res.Code)
	assert.Equal(t, "/login", app.get("/estoque", viewer).Header().Get("Location"), "their sessions ended")
	app.login(t, "thiago.dias", "nova-senha-segura")

	assert.Equal(t, "/minha-senha", app.get("/usuarios/"+strconv.FormatInt(adminID, 10)+"/senha", admin).Header().Get("Location"),
		"own password goes through the form that asks for the current one")
	assert.Equal(t, http.StatusNotFound, app.get("/usuarios/999/senha", admin).Code)
	assert.Equal(t, http.StatusNotFound, app.get("/usuarios/abc/senha", admin).Code)
}

func TestChangeOwnPassword(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	c := app.login(t, "thiago.dias", testPassword)

	res := app.post("/minha-senha", url.Values{"current": {"errada"}, "password": {"nova-senha-segura"}, "confirm": {"nova-senha-segura"}}, c)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Senha atual incorreta.")

	res = app.post("/minha-senha", url.Values{"current": {testPassword}, "password": {"nova-senha-segura"}, "confirm": {"nova-senha-segura"}}, c)
	require.Equal(t, http.StatusSeeOther, res.Code)
	fresh := cookieNamed(res, sessionCookie)
	require.NotNil(t, fresh)
	assert.Equal(t, "/login", app.get("/estoque", c).Header().Get("Location"), "old session ended")
	assert.Equal(t, http.StatusOK, app.get("/estoque", fresh).Code, "this browser stays logged in")
}

func TestFirstRunAdminMustSetUpBeforeAnythingElse(t *testing.T) {
	app := newTestApp(t, "")
	_, err := app.accounts.CreateSetupUser(context.Background(), t0)
	require.NoError(t, err)

	res := app.post("/login", url.Values{"login": {auth.SetupLogin}, "password": {auth.SetupPassword}})
	require.Equal(t, http.StatusSeeOther, res.Code)
	assert.Equal(t, "/configurar", res.Header().Get("Location"))
	setup := cookieNamed(res, sessionCookie)
	for _, path := range []string{"/estoque", "/usuarios", "/entrada", "/minha-senha"} {
		assert.Equal(t, "/configurar", app.get(path, setup).Header().Get("Location"), path)
	}
	assert.Contains(t, app.get("/configurar", setup).Body.String(), "Configure o administrador")

	res = app.post("/configurar", url.Values{"name": {"Paulo Mendes"}, "login": {"admin"}, "password": {"curta"}, "confirm": {"curta"}}, setup)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Este login é reservado. Escolha outro.")
	assert.Contains(t, res.Body.String(), `value="Paulo Mendes"`, "the name is kept")

	res = app.post("/configurar", url.Values{"name": {"Paulo Mendes"}, "login": {"paulo.mendes"},
		"password": {"nova-senha-segura"}, "confirm": {"nova-senha-segura"}}, setup)
	require.Equal(t, http.StatusSeeOther, res.Code, res.Body.String())
	assert.Equal(t, "/estoque", res.Header().Get("Location"))
	fresh := cookieNamed(res, sessionCookie)
	require.NotNil(t, fresh)
	assert.Equal(t, http.StatusOK, app.get("/estoque", fresh).Code, "this browser stays logged in as the new admin")
	assert.Equal(t, "/estoque", app.get("/configurar", fresh).Header().Get("Location"), "setup runs once")
	assert.Equal(t, "/login", app.get("/estoque", setup).Header().Get("Location"), "the admin session ended")

	res = app.post("/login", url.Values{"login": {auth.SetupLogin}, "password": {auth.SetupPassword}})
	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Usuário ou senha incorretos.")
}

func TestStaticFilesAreServed(t *testing.T) {
	app := newTestApp(t, "")
	for path, contentType := range map[string]string{
		"/static/app.css":   "text/css; charset=utf-8",
		"/static/app.js":    "text/javascript; charset=utf-8",
		"/static/icons.svg": "image/svg+xml",
	} {
		res := app.get(path)

		assert.Equal(t, http.StatusOK, res.Code, path)
		assert.Equal(t, contentType, res.Header().Get("Content-Type"), path)
	}
}

func TestStaticURLsCarryContentVersion(t *testing.T) {
	body := newTestApp(t, "").get("/login").Body.String()

	version := contentHash(mustSub(t))
	assert.Contains(t, body, `/static/app.css?v=`+version)
	assert.Contains(t, body, `/static/icons.svg?v=`+version+`#i-eye`)
}

func TestUnknownPathIs404(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, newTestApp(t, "").get("/nao-existe").Code)
}

func TestLastLoginLabels(t *testing.T) {
	s := &server{now: func() time.Time { return t0 }, local: mustFortaleza(t)}

	assert.Equal(t, "nunca", s.lastLogin(time.Time{}))
	assert.Equal(t, "hoje 08:30", s.lastLogin(time.Date(2026, 10, 7, 11, 30, 0, 0, time.UTC)))
	assert.Equal(t, "ontem 22:15", s.lastLogin(time.Date(2026, 10, 7, 1, 15, 0, 0, time.UTC)), "01:15 UTC is still yesterday in Fortaleza")
	assert.Equal(t, "03/10/2026 14:00", s.lastLogin(time.Date(2026, 10, 3, 17, 0, 0, 0, time.UTC)))
}

func mustFortaleza(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Fortaleza")
	require.NoError(t, err)
	return loc
}
