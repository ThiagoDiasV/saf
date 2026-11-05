package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"saf/internal/auth"
	"saf/internal/form"
)

type loginView struct {
	Login string
	Error string
}

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.currentUser(r); err == nil {
		http.Redirect(w, r, "/estoque", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login.html", pageData{Title: "Entrar", Page: loginView{}})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	login := r.PostForm.Get("login")
	user, err := s.accounts.Authenticate(r.Context(), login, r.PostForm.Get("password"), s.now())
	if err != nil {
		var message string
		switch {
		case errors.Is(err, auth.ErrLocked):
			message = "Muitas tentativas. Aguarde 15 minutos e tente de novo."
		case errors.Is(err, auth.ErrInvalidLogin):
			message = "Usuário ou senha incorretos."
		default:
			s.serverError(w, err)
			return
		}
		slog.Warn("login failed", "ip", clientIP(r), "reason", err.Error())
		s.render(w, r, http.StatusUnprocessableEntity, "login.html",
			pageData{Title: "Entrar", Page: loginView{Login: login, Error: message}})
		return
	}
	if err := s.startSession(w, r, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	if user.NeedsSetup() {
		http.Redirect(w, r, setupPath, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/estoque", http.StatusSeeOther)
}

// setupPath is where the first-run administrator (auth.SetupLogin) becomes the real one.
const setupPath = "/configurar"

// setupView is "Configure o administrador": the key to print, and the form refilled after an
// error. Passwords never come back.
type setupView struct {
	Key   string
	Name  string
	Login string
}

func (s *server) setupPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	if !user.NeedsSetup() {
		http.Redirect(w, r, "/estoque", http.StatusSeeOther)
		return
	}
	// No User in pageData: the page shows without tabs or menu, like the login.
	s.render(w, r, http.StatusOK, "setup.html", pageData{Title: "Configure o administrador", Page: setupView{Key: s.keyText}})
}

func (s *server) completeSetup(w http.ResponseWriter, r *http.Request, user auth.User) {
	if !user.NeedsSetup() {
		http.Redirect(w, r, "/estoque", http.StatusSeeOther)
		return
	}
	in := auth.Setup{
		Name:     r.PostForm.Get("name"),
		Login:    r.PostForm.Get("login"),
		Password: r.PostForm.Get("password"),
		Confirm:  r.PostForm.Get("confirm"),
	}
	// The key's only copy outside this PC is the printout: setup waits until it exists.
	err := error(form.Errors{"key_saved": "Imprima a chave, guarde a folha e marque aqui."})
	if r.PostForm.Get("key_saved") == "1" {
		err = s.accounts.CompleteSetup(r.Context(), user.ID, in)
	}
	var errs form.Errors
	if errors.As(err, &errs) {
		s.render(w, r, http.StatusUnprocessableEntity, "setup.html", pageData{
			Title: "Configure o administrador", Errors: errs, Page: setupView{Key: s.keyText, Name: in.Name, Login: in.Login},
		})
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	// The setup ended every session, this one included: start a fresh one here.
	if err := s.startSession(w, r, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	setFlash(w, r, "ok", fmt.Sprintf("Pronto. De agora em diante, entre como %s.", strings.ToLower(strings.TrimSpace(in.Login))))
	http.Redirect(w, r, "/estoque", http.StatusSeeOther)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, _ auth.User) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.accounts.EndSession(r.Context(), c.Value); err != nil {
			s.serverError(w, err)
			return
		}
	}
	clearSessionCookie(w, r)
	// Drops pages cached on a shared PC. Not "storage": that would also wipe the theme choice.
	w.Header().Set("Clear-Site-Data", `"cache"`)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type usersView struct {
	Users []auth.User
	New   newUserValues
}

// newUserValues refills the "Novo usuário" form after an error. The password never comes back.
type newUserValues struct {
	Name  string
	Login string
	Role  string
}

func (s *server) usersPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	s.renderUsers(w, r, user, http.StatusOK, newUserValues{Role: auth.RoleViewer}, nil)
}

func (s *server) renderUsers(w http.ResponseWriter, r *http.Request, user auth.User, status int, values newUserValues, errs form.Errors) {
	users, err := s.accounts.List(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, status, "users.html", pageData{
		Title: "Usuários", Active: "/usuarios", User: user, Errors: errs,
		Page: usersView{Users: users, New: values},
	})
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request, user auth.User) {
	in := auth.NewUser{
		Name:     r.PostForm.Get("name"),
		Login:    r.PostForm.Get("login"),
		Password: r.PostForm.Get("password"),
		Role:     r.PostForm.Get("role"),
	}
	_, err := s.accounts.Create(r.Context(), in, s.now())
	var errs form.Errors
	if errors.As(err, &errs) {
		s.renderUsers(w, r, user, http.StatusUnprocessableEntity, newUserValues{Name: in.Name, Login: in.Login, Role: in.Role}, errs)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	setFlash(w, r, "ok", fmt.Sprintf("Usuário %s criado.", strings.ToLower(strings.TrimSpace(in.Login))))
	http.Redirect(w, r, "/usuarios", http.StatusSeeOther)
}

// targetUser loads the user named in the URL, answering 404 when there is none.
func (s *server) targetUser(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return auth.User{}, false
	}
	target, err := s.accounts.Get(r.Context(), id)
	if errors.Is(err, auth.ErrUserNotFound) {
		http.NotFound(w, r)
		return auth.User{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return auth.User{}, false
	}
	return target, true
}

type resetPasswordView struct {
	Target auth.User
}

func (s *server) resetPasswordPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if target.ID == user.ID { // your own password needs the current one
		http.Redirect(w, r, "/minha-senha", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "reset_password.html",
		pageData{Title: "Redefinir senha", Active: "/usuarios", User: user, Page: resetPasswordView{Target: target}})
}

func (s *server) resetPassword(w http.ResponseWriter, r *http.Request, user auth.User) {
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if target.ID == user.ID {
		http.Redirect(w, r, "/minha-senha", http.StatusSeeOther)
		return
	}
	err := s.accounts.ResetPassword(r.Context(), target.ID, r.PostForm.Get("password"))
	var errs form.Errors
	if errors.As(err, &errs) {
		s.render(w, r, http.StatusUnprocessableEntity, "reset_password.html", pageData{
			Title: "Redefinir senha", Active: "/usuarios", User: user, Errors: errs, Page: resetPasswordView{Target: target},
		})
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	setFlash(w, r, "ok", fmt.Sprintf("Senha de %s redefinida.", target.Name))
	http.Redirect(w, r, "/usuarios", http.StatusSeeOther)
}

func (s *server) setActive(w http.ResponseWriter, r *http.Request, user auth.User) {
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	active := r.PostForm.Get("active") == "1"
	err := s.accounts.SetActive(r.Context(), user.ID, target.ID, active)
	var errs form.Errors
	switch {
	case errors.As(err, &errs):
		setFlash(w, r, "error", errs["active"])
	case err != nil:
		s.serverError(w, err)
		return
	case active:
		setFlash(w, r, "ok", fmt.Sprintf("%s pode entrar de novo.", target.Name))
	default:
		setFlash(w, r, "ok", fmt.Sprintf("%s desativado.", target.Name))
	}
	http.Redirect(w, r, "/usuarios", http.StatusSeeOther)
}

func (s *server) ownPasswordPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	s.render(w, r, http.StatusOK, "own_password.html", pageData{Title: "Alterar minha senha", User: user})
}

func (s *server) changeOwnPassword(w http.ResponseWriter, r *http.Request, user auth.User) {
	err := s.accounts.ChangeOwnPassword(r.Context(), user.ID,
		r.PostForm.Get("current"), r.PostForm.Get("password"), r.PostForm.Get("confirm"))
	var errs form.Errors
	if errors.As(err, &errs) {
		s.render(w, r, http.StatusUnprocessableEntity, "own_password.html",
			pageData{Title: "Alterar minha senha", User: user, Errors: errs})
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	// Changing the password ended every session, this one included: start a fresh one here.
	if err := s.startSession(w, r, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	setFlash(w, r, "ok", "Sua senha foi alterada.")
	http.Redirect(w, r, "/estoque", http.StatusSeeOther)
}
