package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"saf/internal/db"
	"saf/internal/form"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
)

const goodPassword = "farmacia-2026!"

func newAccounts(t *testing.T) *Accounts {
	t.Helper()
	conn, err := db.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return NewAccounts(conn, bcrypt.MinCost)
}

func createUser(t *testing.T, a *Accounts, login, role string) int64 {
	t.Helper()
	id, err := a.Create(ctx, NewUser{Name: "Pessoa " + login, Login: login, Password: goodPassword, Role: role}, t0)
	require.NoError(t, err)
	return id
}

func TestPasswordRules(t *testing.T) {
	for _, tc := range []struct {
		password string
		problem  string
	}{
		{"onze-chars!", "A senha precisa de pelo menos 12 caracteres."},
		{"doze-chars!!", ""},
		{strings.Repeat("a", 64), ""},
		{strings.Repeat("a", 65), "Use no máximo 64 caracteres."},
		{strings.Repeat("ç", 37), "Senha longa demais: use menos acentos ou símbolos."}, // 37 runes, 74 bytes
	} {
		assert.Equal(t, tc.problem, passwordProblem(tc.password), tc.password)
	}
}

func TestCreateValidatesAndNormalizes(t *testing.T) {
	a := newAccounts(t)

	_, err := a.Create(ctx, NewUser{Name: "  ", Login: "Jo", Password: "curta", Role: "chefe"}, t0)

	assert.Equal(t, form.Errors{
		"name":     "Informe o nome.",
		"login":    "Use de 3 a 32 caracteres: letras minúsculas, números, ponto ou hífen.",
		"password": "A senha precisa de pelo menos 12 caracteres.",
		"role":     "Escolha o perfil.",
	}, err)

	id, err := a.Create(ctx, NewUser{Name: "  Paulo   Mendes ", Login: " Paulo.Mendes ", Password: goodPassword, Role: RoleAdmin}, t0)
	require.NoError(t, err)
	u, err := a.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, User{ID: id, Name: "Paulo Mendes", Login: "paulo.mendes", Role: RoleAdmin, Active: true}, u)
}

func TestCreateRejectsDuplicateLogin(t *testing.T) {
	a := newAccounts(t)
	createUser(t, a, "paulo.mendes", RoleAdmin)

	_, err := a.Create(ctx, NewUser{Name: "Outro", Login: "PAULO.MENDES", Password: goodPassword, Role: RoleViewer}, t0)

	assert.Equal(t, form.Errors{"login": "Este login já existe."}, err)
}

func TestCreateRefusesTheSetupLogin(t *testing.T) {
	a := newAccounts(t)

	_, err := a.Create(ctx, NewUser{Name: "Outro", Login: " Admin ", Password: goodPassword, Role: RoleAdmin}, t0)

	assert.Equal(t, form.Errors{"login": "Este login é reservado. Escolha outro."}, err)
}

func TestSetupUserOnlyOnAFreshInstall(t *testing.T) {
	a := newAccounts(t)

	created, err := a.CreateSetupUser(ctx, t0)
	require.NoError(t, err)
	assert.True(t, created)
	created, err = a.CreateSetupUser(ctx, t0)
	require.NoError(t, err)
	assert.False(t, created, "a second start adds nothing")

	u, err := a.Authenticate(ctx, SetupLogin, SetupPassword, t0)
	require.NoError(t, err)
	assert.True(t, u.IsAdmin())
	assert.True(t, u.NeedsSetup())

	inUse := newAccounts(t)
	createUser(t, inUse, "paulo.mendes", RoleAdmin)
	created, err = inUse.CreateSetupUser(ctx, t0)
	require.NoError(t, err)
	assert.False(t, created, "an install in use never gets the setup login")
}

func TestCompleteSetup(t *testing.T) {
	a := newAccounts(t)
	_, err := a.CreateSetupUser(ctx, t0)
	require.NoError(t, err)
	setup, err := a.Authenticate(ctx, SetupLogin, SetupPassword, t0)
	require.NoError(t, err)
	token, err := a.StartSession(ctx, setup.ID, t0)
	require.NoError(t, err)

	err = a.CompleteSetup(ctx, setup.ID, Setup{Name: " ", Login: "admin", Password: "nova-senha-segura", Confirm: "outra-senha-segura"})
	assert.Equal(t, form.Errors{
		"name":    "Informe o nome.",
		"login":   "Este login é reservado. Escolha outro.",
		"confirm": "As senhas não são iguais.",
	}, err)

	require.NoError(t, a.CompleteSetup(ctx, setup.ID, Setup{Name: "Paulo  Mendes", Login: "Paulo.Mendes", Password: "nova-senha-segura", Confirm: "nova-senha-segura"}))

	u, err := a.Authenticate(ctx, "paulo.mendes", "nova-senha-segura", t0)
	require.NoError(t, err)
	assert.Equal(t, User{ID: setup.ID, Name: "Paulo Mendes", Login: "paulo.mendes", Role: RoleAdmin, Active: true, LastLoginAt: t0}, u)
	_, err = a.Authenticate(ctx, SetupLogin, SetupPassword, t0)
	assert.ErrorIs(t, err, ErrInvalidLogin, "admin no longer logs in")
	_, err = a.SessionUser(ctx, token, t0)
	assert.ErrorIs(t, err, ErrNoSession, "the setup session ended")
	assert.ErrorIs(t, a.CompleteSetup(ctx, setup.ID, Setup{Name: "X", Login: "outro.login", Password: "nova-senha-segura", Confirm: "nova-senha-segura"}),
		ErrUserNotFound, "setup runs once")
}

func TestAuthenticate(t *testing.T) {
	a := newAccounts(t)
	id := createUser(t, a, "paulo.mendes", RoleAdmin)

	u, err := a.Authenticate(ctx, " Paulo.Mendes ", goodPassword, t0)
	require.NoError(t, err)
	assert.Equal(t, id, u.ID)
	stored, err := a.Get(ctx, id)
	require.NoError(t, err)
	assert.True(t, stored.LastLoginAt.Equal(t0), "last login recorded")

	_, err = a.Authenticate(ctx, "paulo.mendes", "senha-errada-123", t0)
	assert.ErrorIs(t, err, ErrInvalidLogin)
	_, err = a.Authenticate(ctx, "ninguem", goodPassword, t0)
	assert.ErrorIs(t, err, ErrInvalidLogin, "unknown login looks the same as a wrong password")
}

func TestDeactivatedUserCannotLogIn(t *testing.T) {
	a := newAccounts(t)
	admin := createUser(t, a, "paulo.mendes", RoleAdmin)
	viewer := createUser(t, a, "thiago.dias", RoleViewer)
	require.NoError(t, a.SetActive(ctx, admin, viewer, false))

	_, err := a.Authenticate(ctx, "thiago.dias", goodPassword, t0)

	assert.ErrorIs(t, err, ErrInvalidLogin)
}

func TestFiveWrongPasswordsLockForFifteenMinutes(t *testing.T) {
	a := newAccounts(t)
	createUser(t, a, "paulo.mendes", RoleAdmin)

	for i := 1; i <= 4; i++ {
		_, err := a.Authenticate(ctx, "paulo.mendes", "senha-errada-123", t0)
		require.ErrorIs(t, err, ErrInvalidLogin, "attempt %d", i)
	}
	_, err := a.Authenticate(ctx, "paulo.mendes", "senha-errada-123", t0)
	assert.ErrorIs(t, err, ErrLocked, "fifth wrong password locks")

	_, err = a.Authenticate(ctx, "paulo.mendes", goodPassword, t0.Add(14*time.Minute))
	assert.ErrorIs(t, err, ErrLocked, "right password is refused while locked")

	_, err = a.Authenticate(ctx, "paulo.mendes", goodPassword, t0.Add(15*time.Minute+time.Second))
	assert.NoError(t, err)
}

func TestSuccessfulLoginResetsFailureCount(t *testing.T) {
	a := newAccounts(t)
	createUser(t, a, "paulo.mendes", RoleAdmin)
	for range 4 {
		_, _ = a.Authenticate(ctx, "paulo.mendes", "senha-errada-123", t0)
	}
	_, err := a.Authenticate(ctx, "paulo.mendes", goodPassword, t0)
	require.NoError(t, err)

	_, err = a.Authenticate(ctx, "paulo.mendes", "senha-errada-123", t0)

	assert.ErrorIs(t, err, ErrInvalidLogin, "count started over, so one more wrong password does not lock")
}

func TestSessionLifecycle(t *testing.T) {
	a := newAccounts(t)
	id := createUser(t, a, "paulo.mendes", RoleAdmin)

	token, err := a.StartSession(ctx, id, t0)
	require.NoError(t, err)
	assert.Len(t, token, 43, "32 random bytes, base64url")

	u, err := a.SessionUser(ctx, token, t0.Add(SessionDuration-time.Second))
	require.NoError(t, err)
	assert.Equal(t, "paulo.mendes", u.Login)

	_, err = a.SessionUser(ctx, token, t0.Add(SessionDuration))
	assert.ErrorIs(t, err, ErrNoSession, "expired after 12 h")

	require.NoError(t, a.EndSession(ctx, token))
	_, err = a.SessionUser(ctx, token, t0)
	assert.ErrorIs(t, err, ErrNoSession, "ended by logout")

	_, err = a.SessionUser(ctx, "", t0)
	assert.ErrorIs(t, err, ErrNoSession)
}

func TestSessionStoresOnlyTheTokenHash(t *testing.T) {
	a := newAccounts(t)
	token, err := a.StartSession(ctx, createUser(t, a, "paulo.mendes", RoleAdmin), t0)
	require.NoError(t, err)

	var stored []byte
	require.NoError(t, a.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored))

	assert.Equal(t, hashToken(token), stored)
	assert.NotContains(t, string(stored), token)
}

func TestDeactivationEndsSessions(t *testing.T) {
	a := newAccounts(t)
	admin := createUser(t, a, "paulo.mendes", RoleAdmin)
	viewer := createUser(t, a, "thiago.dias", RoleViewer)
	token, err := a.StartSession(ctx, viewer, t0)
	require.NoError(t, err)

	require.NoError(t, a.SetActive(ctx, admin, viewer, false))
	_, err = a.SessionUser(ctx, token, t0)
	assert.ErrorIs(t, err, ErrNoSession)

	require.NoError(t, a.SetActive(ctx, admin, viewer, true))
	_, err = a.Authenticate(ctx, "thiago.dias", goodPassword, t0)
	assert.NoError(t, err, "reactivated user can log in again")
}

func TestNobodyCanDeactivateThemselves(t *testing.T) {
	a := newAccounts(t)
	admin := createUser(t, a, "paulo.mendes", RoleAdmin)

	err := a.SetActive(ctx, admin, admin, false)

	assert.Equal(t, form.Errors{"active": "Você não pode desativar o próprio usuário."}, err)
	u, _ := a.Get(ctx, admin)
	assert.True(t, u.Active)
}

func TestSetActiveUnknownUser(t *testing.T) {
	a := newAccounts(t)
	admin := createUser(t, a, "paulo.mendes", RoleAdmin)

	assert.ErrorIs(t, a.SetActive(ctx, admin, 999, false), ErrUserNotFound)
}

func TestResetPasswordUnlocksAndEndsSessions(t *testing.T) {
	a := newAccounts(t)
	id := createUser(t, a, "thiago.dias", RoleViewer)
	token, err := a.StartSession(ctx, id, t0)
	require.NoError(t, err)
	for range 5 {
		_, _ = a.Authenticate(ctx, "thiago.dias", "senha-errada-123", t0)
	}

	assert.Equal(t, form.Errors{"password": "A senha precisa de pelo menos 12 caracteres."},
		a.ResetPassword(ctx, id, "curta"))
	require.NoError(t, a.ResetPassword(ctx, id, "nova-senha-segura"))

	_, err = a.SessionUser(ctx, token, t0)
	assert.ErrorIs(t, err, ErrNoSession)
	_, err = a.Authenticate(ctx, "thiago.dias", "nova-senha-segura", t0)
	assert.NoError(t, err, "lock lifted, new password works at once")
	assert.ErrorIs(t, a.ResetPassword(ctx, 999, "nova-senha-segura"), ErrUserNotFound)
}

func TestChangeOwnPassword(t *testing.T) {
	a := newAccounts(t)
	id := createUser(t, a, "thiago.dias", RoleViewer)
	token, err := a.StartSession(ctx, id, t0)
	require.NoError(t, err)

	err = a.ChangeOwnPassword(ctx, id, "errada", "nova-senha-segura", "outra-senha-qualquer")
	assert.Equal(t, form.Errors{"current": "Senha atual incorreta.", "confirm": "As senhas não são iguais."}, err)

	require.NoError(t, a.ChangeOwnPassword(ctx, id, goodPassword, "nova-senha-segura", "nova-senha-segura"))

	_, err = a.SessionUser(ctx, token, t0)
	assert.ErrorIs(t, err, ErrNoSession, "other sessions end")
	_, err = a.Authenticate(ctx, "thiago.dias", goodPassword, t0)
	assert.ErrorIs(t, err, ErrInvalidLogin)
	_, err = a.Authenticate(ctx, "thiago.dias", "nova-senha-segura", t0)
	assert.NoError(t, err)
}

func TestListOrdersByName(t *testing.T) {
	a := newAccounts(t)
	_, err := a.Create(ctx, NewUser{Name: "Thiago Dias", Login: "thiago.dias", Password: goodPassword, Role: RoleViewer}, t0)
	require.NoError(t, err)
	_, err = a.Create(ctx, NewUser{Name: "Paulo Mendes", Login: "paulo.mendes", Password: goodPassword, Role: RoleAdmin}, t0)
	require.NoError(t, err)

	users, err := a.List(ctx)

	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, []string{"Paulo Mendes", "Thiago Dias"}, []string{users[0].Name, users[1].Name})
}
