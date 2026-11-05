package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/auth"
)

var wantHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; img-src 'self' blob:; connect-src 'self'; " +
		"form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'",
	"X-Content-Type-Options":       "nosniff",
	"X-Frame-Options":              "DENY",
	"Referrer-Policy":              "no-referrer",
	"Cross-Origin-Opener-Policy":   "same-origin",
	"Cross-Origin-Resource-Policy": "same-origin",
	"Permissions-Policy":           "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	viewer := app.login(t, "thiago.dias", testPassword)
	jsonPost := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{}`))
	jsonPost.Header.Set("Content-Type", "application/json")

	for name, res := range map[string]*httptest.ResponseRecorder{
		"200 page":   app.get("/login"),
		"303":        app.get("/estoque"),
		"403":        app.get("/usuarios", viewer),
		"404":        app.get("/nao-existe"),
		"415":        app.serve(jsonPost),
		"static 200": app.get("/static/app.css"),
	} {
		for header, want := range wantHeaders {
			assert.Equal(t, want, res.Header().Get(header), "%s: %s", name, header)
		}
		assert.Empty(t, res.Header().Get("Strict-Transport-Security"), "%s: no HSTS over plain HTTP", name)
	}
}

func TestHSTSOverHTTPS(t *testing.T) {
	res := newTestApp(t, "").serve(httptest.NewRequest(http.MethodGet, "https://saf.example/login", nil))

	assert.Equal(t, "max-age=31536000; includeSubDomains", res.Header().Get("Strict-Transport-Security"))
}

func TestCacheControl(t *testing.T) {
	app := newTestApp(t, "")

	assert.Equal(t, "public, max-age=86400", app.get("/static/app.css").Header().Get("Cache-Control"))
	assert.Equal(t, "no-store", app.get("/login").Header().Get("Cache-Control"))
	assert.Equal(t, "no-store", app.get("/estoque").Header().Get("Cache-Control"))
}

func TestCrossSiteLoadsAreRefused(t *testing.T) {
	app := newTestApp(t, "")
	request := func(site, mode, dest string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/login", nil)
		r.Header.Set("Sec-Fetch-Site", site)
		r.Header.Set("Sec-Fetch-Mode", mode)
		r.Header.Set("Sec-Fetch-Dest", dest)
		return app.serve(r)
	}

	assert.Equal(t, http.StatusForbidden, request("cross-site", "no-cors", "image").Code, "<img> on another site")
	assert.Equal(t, http.StatusForbidden, request("cross-site", "cors", "empty").Code, "fetch from another site")
	assert.Equal(t, http.StatusForbidden, request("same-site", "no-cors", "script").Code)
	assert.Equal(t, http.StatusOK, request("cross-site", "navigate", "document").Code, "link clicked on another site")
	assert.Equal(t, http.StatusOK, request("same-origin", "no-cors", "image").Code)
	assert.Equal(t, http.StatusOK, request("none", "navigate", "document").Code, "typed URL or bookmark")
	assert.Equal(t, http.StatusOK, app.get("/login").Code, "no Fetch Metadata (old browser, curl)")
}

func TestCrossOriginPostIsRefused(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	form := url.Values{"login": {"paulo.mendes"}, "password": {testPassword}}.Encode()

	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example")
	res := app.serve(r)

	assert.Equal(t, http.StatusForbidden, res.Code)
	assert.Nil(t, cookieNamed(res, sessionCookie))
}

func TestNoCORSHeaders(t *testing.T) {
	app := newTestApp(t, "")
	get := httptest.NewRequest(http.MethodGet, "/login", nil)
	get.Header.Set("Origin", "https://evil.example")
	options := httptest.NewRequest(http.MethodOptions, "/login", nil)
	options.Header.Set("Origin", "https://evil.example")
	options.Header.Set("Access-Control-Request-Method", "POST")

	for name, res := range map[string]*httptest.ResponseRecorder{"GET": app.serve(get), "OPTIONS": app.serve(options)} {
		for header := range res.Header() {
			assert.False(t, strings.HasPrefix(header, "Access-Control-"), "%s sent %s", name, header)
		}
	}
	assert.Equal(t, http.StatusMethodNotAllowed, app.serve(options).Code)
}

func TestHostCheck(t *testing.T) {
	app := newTestApp(t, "saf-farmacia.duckdns.org")
	request := func(host string) int {
		r := httptest.NewRequest(http.MethodGet, "/login", nil)
		r.Host = host
		return app.serve(r).Code
	}

	assert.Equal(t, http.StatusMisdirectedRequest, request("34.75.10.20"), "bare IP")
	assert.Equal(t, http.StatusMisdirectedRequest, request("evil.example"))
	assert.Equal(t, http.StatusOK, request("saf-farmacia.duckdns.org"))
	assert.Equal(t, http.StatusOK, request("SAF-Farmacia.duckdns.org:443"))
}

func TestOnlyFormPostsAreAccepted(t *testing.T) {
	app := newTestApp(t, "")

	jsonPost := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"login":"paulo.mendes"}`))
	jsonPost.Header.Set("Content-Type", "application/json")
	assert.Equal(t, http.StatusUnsupportedMediaType, app.serve(jsonPost).Code)

	huge := app.post("/login", url.Values{"login": {strings.Repeat("a", maxFormBytes)}})
	assert.Equal(t, http.StatusRequestEntityTooLarge, huge.Code)
}

func TestGeneralRateLimitPerIP(t *testing.T) {
	app := newTestApp(t, "")
	from := func(ip string) int {
		r := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
		r.RemoteAddr = ip + ":40000"
		return app.serve(r).Code
	}

	for i := 1; i <= 300; i++ {
		require.Equal(t, http.StatusOK, from("192.0.2.1"), "request %d", i)
	}
	r := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	r.RemoteAddr = "192.0.2.1:40000"
	blocked := app.serve(r)
	assert.Equal(t, http.StatusTooManyRequests, blocked.Code)
	assert.Equal(t, "60", blocked.Header().Get("Retry-After"))
	assert.Contains(t, blocked.Body.String(), "Muitas requisições. Aguarde um minuto.")
	assert.Equal(t, wantHeaders["X-Frame-Options"], blocked.Header().Get("X-Frame-Options"), "429 has the headers too")

	assert.Equal(t, http.StatusOK, from("203.0.113.9"), "another IP is not affected")
	app.now = app.now.Add(time.Minute)
	assert.Equal(t, http.StatusOK, from("192.0.2.1"), "next minute starts over")
}

func TestLoginRateLimitPerIP(t *testing.T) {
	app := newTestApp(t, "")
	attempt := func() int {
		return app.post("/login", url.Values{"login": {"ninguem"}, "password": {"qualquer-coisa"}}).Code
	}

	for i := 1; i <= 10; i++ {
		require.Equal(t, http.StatusUnprocessableEntity, attempt(), "attempt %d", i)
	}
	assert.Equal(t, http.StatusTooManyRequests, attempt())
}

func TestClientIP(t *testing.T) {
	for remote, want := range map[string]string{
		"192.0.2.1:5000":                          "192.0.2.1",
		"[::ffff:192.0.2.1]:5000":                 "192.0.2.1",
		"[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:5000": "2001:db8:1:2::/64",
		"[2001:db8:1:2:1111:2222:3333:4444]:5000": "2001:db8:1:2::/64",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		assert.Equal(t, want, clientIP(r), remote)
	}
}

func mustSub(t *testing.T) fs.FS {
	t.Helper()
	static, err := fs.Sub(staticFiles, "static")
	require.NoError(t, err)
	return static
}
