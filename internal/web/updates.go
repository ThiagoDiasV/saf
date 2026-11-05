package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"saf/internal/auth"
)

// acceptUpdate is an administrator accepting a new major version from the banner. The download
// runs after the answer: SAF closes and opens on the new version when it is in place.
func (s *server) acceptUpdate(w http.ResponseWriter, r *http.Request, _ auth.User) {
	if s.updates == nil {
		http.NotFound(w, r)
		return
	}
	version := s.updates.Status().Waiting
	if version == "" {
		http.Redirect(w, r, "/estoque", http.StatusSeeOther)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := s.updates.Accept(ctx); err != nil {
			slog.Error("update failed", "version", version, "err", err)
		}
	}()
	setFlash(w, r, "ok", fmt.Sprintf("Baixando a versão %s. O SAF fecha e abre sozinho quando terminar.", version))
	http.Redirect(w, r, "/estoque", http.StatusSeeOther)
}

// restartForUpdate closes SAF and opens it on the version already installed.
func (s *server) restartForUpdate(w http.ResponseWriter, r *http.Request, _ auth.User) {
	if s.updates == nil {
		http.NotFound(w, r)
		return
	}
	if s.updates.Status().Installed != "" {
		s.updates.Restart()
	}
	http.Redirect(w, r, "/estoque", http.StatusSeeOther)
}
