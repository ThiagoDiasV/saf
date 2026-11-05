package web

import (
	"errors"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"

	"saf/internal/auth"
)

const (
	maxFormBytes   = 1 << 20
	maxUploadBytes = 8 << 20 // a form with photos; the browser shrinks each to a few hundred KB
)

// securityHeaders sets the response headers listed in docs/PLAN.md §3, on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; connect-src 'self'; "+
			"form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		// Static URLs carry the build version (?v=), so a day of caching never serves stale files.
		// Everything else may show patient data on a shared counter PC: never cache it.
		if strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "public, max-age=86400")
		} else {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// checkHost answers 421 to any Host other than domain, so scanners hitting the bare IP get
// nothing. An empty domain (local development) accepts any Host.
func checkHost(domain string, next http.Handler) http.Handler {
	if domain == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !strings.EqualFold(host, domain) {
			http.Error(w, "Endereço desconhecido.", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isolateResources uses the Fetch Metadata headers browsers send to refuse loads started by
// another site (an <img> of a prescription photo, a script tag, a fetch). A link clicked on
// another site still opens the page. Requests without the headers (old browsers, curl) pass.
func isolateResources(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
			next.ServeHTTP(w, r)
			return
		}
		navigation := (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document"
		if !navigation {
			http.Error(w, "Requisição de outro site bloqueada.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// crossOriginProtection refuses POSTs from other origins (CSRF).
func crossOriginProtection(next http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Requisição de outro site bloqueada.", http.StatusForbidden)
	}))
	return protection.Handler(next)
}

// parseForms accepts only HTML form posts, caps their size, and parses them, so handlers can
// read r.PostForm (and r.MultipartForm for photos) without checking errors.
func parseForms(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		var err error
		switch mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType {
		case "application/x-www-form-urlencoded":
			r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
			err = r.ParseForm()
		case "multipart/form-data":
			r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
			// All in memory: the container's file system is read-only, so no temporary files.
			err = r.ParseMultipartForm(maxUploadBytes)
		default:
			http.Error(w, "Formato de envio não aceito.", http.StatusUnsupportedMediaType)
			return
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "Envio grande demais.", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "Formulário inválido.", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// userHandler is a handler that runs only for a logged-in user.
type userHandler func(w http.ResponseWriter, r *http.Request, user auth.User)

// requireUser sends visitors without a valid session to the login page, and the first-run
// administrator to the setup page: that login can do nothing else.
func (s *server) requireUser(next userHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.currentUser(r)
		if errors.Is(err, auth.ErrNoSession) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
		if user.NeedsSetup() && r.URL.Path != setupPath && r.URL.Path != "/sair" {
			http.Redirect(w, r, setupPath, http.StatusSeeOther)
			return
		}
		next(w, r, user)
	})
}

// requireAdmin shows "Sem permissão" to view-only users.
func (s *server) requireAdmin(next userHandler) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request, user auth.User) {
		if !user.IsAdmin() {
			s.render(w, r, http.StatusForbidden, "forbidden.html", pageData{Title: "Sem permissão", User: user})
			return
		}
		next(w, r, user)
	})
}

func (s *server) serverError(w http.ResponseWriter, err error) {
	slog.Error("request failed", "err", err)
	http.Error(w, "Erro interno. Tente de novo.", http.StatusInternalServerError)
}
