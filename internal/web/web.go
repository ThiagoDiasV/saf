// Package web serves SAF's pages and static files.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"saf/internal/auth"
	"saf/internal/form"
	"saf/internal/stock"
)

//go:embed templates
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

const (
	sessionCookie = "saf_sessao"
	flashCookie   = "saf_aviso"
)

// Config is what Handler needs from main.
type Config struct {
	Accounts  *auth.Accounts
	Inventory *stock.Inventory
	// Domain, when set, is the only Host accepted: the desktop app passes 127.0.0.1, so a web page
	// that points its own name at this computer (DNS rebinding) gets nothing. Empty in development.
	Domain string
	// Now is the clock; nil means time.Now. Tests move it to expire sessions and rate limits.
	Now func() time.Time
}

type server struct {
	accounts      *auth.Accounts
	inventory     *stock.Inventory
	now           func() time.Time
	pages         map[string]*template.Template
	fragments     *template.Template // the partials alone, for HTML the browser fetches into a page
	staticVersion string
	local         *time.Location
}

type tab struct {
	Path      string
	Label     string
	Icon      string // symbol id in static/icons.svg, without the "i-" prefix
	AdminOnly bool
	DeskOnly  bool // on phones it moves into the "Mais" menu
}

var tabs = []tab{
	{Path: "/estoque", Label: "Estoque", Icon: "box"},
	{Path: "/entrada", Label: "Entrada", Icon: "in", AdminOnly: true},
	{Path: "/saida", Label: "Saída", Icon: "out", AdminOnly: true},
	{Path: "/historico", Label: "Histórico", Icon: "clock"},
	{Path: "/medicamentos", Label: "Medicamentos", Icon: "pill", AdminOnly: true, DeskOnly: true},
	{Path: "/usuarios", Label: "Usuários", Icon: "users", AdminOnly: true, DeskOnly: true},
}

// pageData is what templates/layout.html and the pages read.
type pageData struct {
	Title  string
	Active string    // Path of the current tab
	User   auth.User // zero on the login page
	Errors form.Errors
	Page   any // page-specific data

	// Filled in by render.
	Tabs     []tab
	MoreTabs []tab
	Flash    *flash
	Static   string
}

type flash struct {
	Kind    string // "ok" or "error"
	Message string
}

// Handler returns SAF with every route and protection in place.
//
//	http.ListenAndServe(":8080", web.Handler(web.Config{Accounts: accounts}))
func Handler(cfg Config) http.Handler {
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // the embedded directory is fixed at build time
	}
	s := &server{
		accounts:      cfg.Accounts,
		inventory:     cfg.Inventory,
		now:           cfg.Now,
		staticVersion: contentHash(static),
		local:         stock.TimeZone,
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.pages, s.fragments = s.parsePages()

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.Handle("GET /{$}", http.RedirectHandler("/estoque", http.StatusSeeOther))
	mux.HandleFunc("GET /login", s.loginPage)
	mux.Handle("POST /login", newRateLimiter(10, time.Minute, s.now).wrap(http.HandlerFunc(s.login)))
	mux.Handle("POST /sair", s.requireUser(s.logout))
	mux.Handle("GET "+setupPath, s.requireUser(s.setupPage))
	mux.Handle("POST "+setupPath, s.requireUser(s.completeSetup))
	mux.Handle("GET /minha-senha", s.requireUser(s.ownPasswordPage))
	mux.Handle("POST /minha-senha", s.requireUser(s.changeOwnPassword))
	mux.Handle("GET /usuarios", s.requireAdmin(s.usersPage))
	mux.Handle("POST /usuarios", s.requireAdmin(s.createUser))
	mux.Handle("GET /usuarios/{id}/senha", s.requireAdmin(s.resetPasswordPage))
	mux.Handle("POST /usuarios/{id}/senha", s.requireAdmin(s.resetPassword))
	mux.Handle("POST /usuarios/{id}/ativo", s.requireAdmin(s.setActive))
	mux.Handle("GET /estoque", s.requireUser(s.stockPage))
	mux.Handle("GET /estoque.csv", s.requireUser(s.stockCSV))
	mux.Handle("GET /lotes/{id}/editar", s.requireAdmin(s.editBatchPage))
	mux.Handle("POST /lotes/{id}", s.requireAdmin(s.updateBatch))
	mux.Handle("GET /saida", s.requireAdmin(s.exitPage))
	mux.Handle("POST /saida", s.requireAdmin(s.recordExit))
	mux.Handle("GET /saida/lotes", s.requireAdmin(s.exitBatches))
	mux.Handle("GET /entrada", s.requireAdmin(s.entryPage))
	mux.Handle("POST /entrada", s.requireAdmin(s.recordEntry))
	mux.Handle("GET /medicamentos", s.requireAdmin(s.medicinesPage))
	mux.Handle("POST /medicamentos", s.requireAdmin(s.createMedicine))
	mux.Handle("GET /medicamentos/{id}/editar", s.requireAdmin(s.editMedicinePage))
	mux.Handle("POST /medicamentos/{id}", s.requireAdmin(s.updateMedicine))
	mux.Handle("POST /medicamentos/{id}/arquivar", s.requireAdmin(s.archiveMedicine))
	mux.Handle("POST /medicamentos/{id}/restaurar", s.requireAdmin(s.restoreMedicine))
	mux.Handle("POST /medicamentos/{id}/excluir", s.requireAdmin(s.deleteMedicine))
	mux.Handle("GET /historico", s.requireUser(s.historyPage))
	mux.Handle("GET /historico.csv", s.requireUser(s.historyCSV))
	mux.Handle("GET /movimentos/{id}", s.requireUser(s.movementPage))
	mux.Handle("POST /movimentos/{id}/observacao", s.requireAdmin(s.updateNote))
	mux.Handle("POST /movimentos/{id}/estorno", s.requireAdmin(s.reverseMovement))
	mux.Handle("POST /movimentos/{id}/fotos", s.requireAdmin(s.addPhotos))
	mux.Handle("GET /fotos/{id}", s.requireUser(s.photo))
	mux.Handle("POST /fotos/{id}/remover", s.requireAdmin(s.removePhoto))

	// Outermost first: every response, even a 429, carries the security headers.
	var h http.Handler = mux
	h = parseForms(h)
	h = crossOriginProtection(h)
	h = isolateResources(h)
	h = checkHost(cfg.Domain, h)
	h = newRateLimiter(300, time.Minute, s.now).wrap(h)
	return securityHeaders(h)
}

// parsePages gives every file in templates/pages its own copy of the layout and the shared
// partials, so each page can define its own "content" block.
func (s *server) parsePages() (map[string]*template.Template, *template.Template) {
	funcs := template.FuncMap{
		"icon":      s.icon,
		"roleLabel": roleLabel,
		"lastLogin": s.lastLogin,
		"lower":     strings.ToLower,
		"date":      formatDate,
		"photoPicker": func(hint, problem string) photoPicker {
			return photoPicker{Hint: hint, Error: problem}
		},
		"localTime": func(t time.Time) string { return t.In(s.local).Format("02/01/2006 15:04") },
		"abs": func(n int64) int64 {
			if n < 0 {
				return -n
			}
			return n
		},
		"plural":    plural,
		"qty":       func(n int64, unit string) string { return fmt.Sprintf("%d %s", n, plural(unit, n)) },
		"entryLine": entryLine,
		"blankEntryLine": func() entryLineView {
			return entryLineView{Invalid: map[string]bool{}}
		},
		// The browser renames the copy's fields with the next free line number.
		"blankExitLine": func() exitLineView {
			return exitLineView{Index: -1, Invalid: map[string]bool{}}
		},
	}
	layout := template.Must(template.New("layout.html").Funcs(funcs).
		ParseFS(templateFiles, "templates/layout.html", "templates/partials/*.html"))
	names, err := fs.Glob(templateFiles, "templates/pages/*.html")
	if err != nil {
		panic(err)
	}
	pages := make(map[string]*template.Template, len(names))
	for _, name := range names {
		pages[path.Base(name)] = template.Must(template.Must(layout.Clone()).ParseFS(templateFiles, name))
	}
	fragments := template.Must(template.New("fragments").Funcs(funcs).ParseFS(templateFiles, "templates/partials/*.html"))
	return pages, fragments
}

// renderFragment sends one partial, without the layout, for the browser to place in the page.
func (s *server) renderFragment(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.fragments.ExecuteTemplate(&buf, name, data); err != nil {
		s.serverError(w, fmt.Errorf("render fragment %s: %w", name, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// render executes a page into a buffer first, so a template error becomes a clean 500
// instead of half a page.
func (s *server) render(w http.ResponseWriter, r *http.Request, status int, page string, data pageData) {
	if data.User.ID != 0 {
		data.Tabs, data.MoreTabs = tabsFor(data.User)
	}
	if data.Flash == nil {
		data.Flash = takeFlash(w, r)
	}
	data.Static = s.staticVersion
	var buf bytes.Buffer
	if err := s.pages[page].Execute(&buf, data); err != nil {
		s.serverError(w, fmt.Errorf("render %s: %w", page, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func tabsFor(user auth.User) (visible, more []tab) {
	for _, t := range tabs {
		if t.AdminOnly && !user.IsAdmin() {
			continue
		}
		visible = append(visible, t)
		if t.DeskOnly {
			more = append(more, t)
		}
	}
	return visible, more
}

// icon draws a symbol from static/icons.svg.
func (s *server) icon(name string) template.HTML {
	return template.HTML(fmt.Sprintf(`<svg class="icon" aria-hidden="true"><use href="/static/icons.svg?v=%s#i-%s"/></svg>`,
		s.staticVersion, html.EscapeString(name)))
}

func roleLabel(role string) string {
	if role == auth.RoleAdmin {
		return "Administrador"
	}
	return "Visualização"
}

// plural spells a counting unit for n: "comprimido" → "comprimidos", "frasco conta-gotas" →
// "frascos conta-gotas", "sachê" → "sachês", "ampola" → "ampolas".
func plural(unit string, n int64) string {
	if n == 1 || n == -1 {
		return unit
	}
	first, rest, compound := strings.Cut(unit, " ")
	switch {
	case strings.HasSuffix(first, "ão"):
		first = strings.TrimSuffix(first, "ão") + "ões"
	case strings.HasSuffix(first, "r"), strings.HasSuffix(first, "z"):
		first += "es"
	case strings.HasSuffix(first, "l"):
		first = strings.TrimSuffix(first, "l") + "is"
	case strings.HasSuffix(first, "s"):
	default:
		first += "s"
	}
	if compound {
		return first + " " + rest
	}
	return first
}

// lastLogin reads like the prototype: "nunca", "hoje 14:02", "ontem 09:10", "03/10/2026 14:02".
func (s *server) lastLogin(t time.Time) string {
	if t.IsZero() {
		return "nunca"
	}
	t = t.In(s.local)
	today := s.now().In(s.local)
	switch t.Format("2006-01-02") {
	case today.Format("2006-01-02"):
		return "hoje " + t.Format("15:04")
	case today.AddDate(0, 0, -1).Format("2006-01-02"):
		return "ontem " + t.Format("15:04")
	}
	return t.Format("02/01/2006 15:04")
}

// contentHash fingerprints the static files; it goes into their URLs as ?v=.
func contentHash(files fs.FS) string {
	h := sha256.New()
	err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(files, name)
		h.Write([]byte(name))
		h.Write(data)
		return err
	})
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// setFlash leaves a message for the next page, after a POST redirects (toast).
func setFlash(w http.ResponseWriter, r *http.Request, kind, message string) {
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + message)),
		Path: "/", MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func takeFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Path: "/", MaxAge: -1, HttpOnly: true})
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	kind, message, ok := strings.Cut(string(raw), ":")
	if !ok || (kind != "ok" && kind != "error") {
		return nil
	}
	return &flash{Kind: kind, Message: message}
}

func (s *server) currentUser(r *http.Request) (auth.User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return auth.User{}, auth.ErrNoSession
	}
	return s.accounts.SessionUser(r.Context(), c.Value, s.now())
}

// startSession logs userID in on this browser.
func (s *server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := s.accounts.StartSession(r.Context(), userID, s.now())
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: int(auth.SessionDuration.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true})
}
