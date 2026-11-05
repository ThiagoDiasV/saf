// Package auth owns staff accounts: users, passwords, the login lock and sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"saf/internal/db"
	"saf/internal/form"
)

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"

	// HashCost is the bcrypt cost in production. Tests pass bcrypt.MinCost to stay fast.
	HashCost = 12

	SessionDuration = 12 * time.Hour

	maxFailedLogins  = 5
	lockDuration     = 15 * time.Minute
	minPasswordRunes = 12
	maxPasswordRunes = 64
	maxPasswordBytes = 72 // bcrypt ignores anything after this
	maxNameRunes     = 80
)

// A fresh install gets one administrator, SetupLogin with SetupPassword (both printed in the
// README). Logging in with it only allows turning it into a real administrator; after that no
// account has SetupLogin and none may take it, so it never works again.
const (
	SetupLogin    = "admin"
	SetupPassword = "primeiro-acesso"
)

var (
	// ErrInvalidLogin covers an unknown login, a wrong password and a deactivated user alike,
	// so the login page never reveals which logins exist.
	ErrInvalidLogin = errors.New("invalid login or password")
	ErrLocked       = errors.New("login locked after too many wrong passwords")
	ErrNoSession    = errors.New("no valid session")
	ErrUserNotFound = errors.New("user not found")
)

var loginPattern = regexp.MustCompile(`^[a-z0-9._-]{3,32}$`)

type User struct {
	ID          int64
	Name        string
	Login       string
	Role        string
	Active      bool
	LastLoginAt time.Time // zero if the user never logged in
}

func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

// NeedsSetup is true for the first-run administrator until someone completes the setup.
func (u User) NeedsSetup() bool { return u.Login == SetupLogin }

// Accounts reads and changes staff accounts in the database.
type Accounts struct {
	db       *sql.DB
	hashCost int
	// dummyHash is checked for unknown logins, so they take as long as known ones.
	dummyHash func() []byte
}

// NewAccounts returns Accounts backed by conn.
//
//	accounts := auth.NewAccounts(conn, auth.HashCost)
func NewAccounts(conn *sql.DB, hashCost int) *Accounts {
	return &Accounts{
		db:       conn,
		hashCost: hashCost,
		dummyHash: sync.OnceValue(func() []byte {
			hash, err := bcrypt.GenerateFromPassword([]byte("not anybody's password"), hashCost)
			if err != nil {
				panic(err) // only an invalid cost fails, and that is a programming error
			}
			return hash
		}),
	}
}

// NewUser is what the "Novo usuário" form sends.
type NewUser struct {
	Name     string
	Login    string
	Password string
	Role     string
}

// Create adds a user and returns its id. Invalid input comes back as form.Errors.
func (a *Accounts) Create(ctx context.Context, u NewUser, now time.Time) (int64, error) {
	errs := form.Errors{}
	name, login := identity(u.Name, u.Login, errs)
	if problem := passwordProblem(u.Password); problem != "" {
		errs["password"] = problem
	}
	if u.Role != RoleAdmin && u.Role != RoleViewer {
		errs["role"] = "Escolha o perfil."
	}
	if len(errs) > 0 {
		return 0, errs
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(u.Password), a.hashCost)
	if err != nil {
		return 0, fmt.Errorf("hash password: %w", err)
	}
	res, err := a.db.ExecContext(ctx,
		`INSERT INTO users (name, login, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, login, string(hash), u.Role, db.FormatTime(now))
	if db.IsUniqueViolation(err) {
		return 0, form.Errors{"login": "Este login já existe."}
	}
	if err != nil {
		return 0, fmt.Errorf("insert user %q: %w", login, err)
	}
	return res.LastInsertId()
}

// identity tidies a name and a login (extra spaces, capitals) and adds what is wrong with them
// to errs.
func identity(rawName, rawLogin string, errs form.Errors) (name, login string) {
	name = strings.Join(strings.Fields(rawName), " ")
	login = strings.ToLower(strings.TrimSpace(rawLogin))
	switch {
	case name == "":
		errs["name"] = "Informe o nome."
	case utf8.RuneCountInString(name) > maxNameRunes:
		errs["name"] = fmt.Sprintf("Use no máximo %d caracteres.", maxNameRunes)
	}
	switch {
	case !loginPattern.MatchString(login):
		errs["login"] = "Use de 3 a 32 caracteres: letras minúsculas, números, ponto ou hífen."
	case login == SetupLogin:
		errs["login"] = "Este login é reservado. Escolha outro."
	}
	return name, login
}

// CreateSetupUser gives a fresh install its first administrator (SetupLogin) and reports whether
// it did. Once any user exists it does nothing, so an install in use never gets it back.
func (a *Accounts) CreateSetupUser(ctx context.Context, now time.Time) (bool, error) {
	var anyUser bool
	if err := a.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&anyUser); err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	if anyUser {
		return false, nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(SetupPassword), a.hashCost)
	if err != nil {
		return false, fmt.Errorf("hash password: %w", err)
	}
	// NOT EXISTS again: two SAFs starting at once still make only one.
	res, err := a.db.ExecContext(ctx, `
		INSERT INTO users (name, login, password_hash, role, created_at)
		SELECT 'Administrador', ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		SetupLogin, string(hash), RoleAdmin, db.FormatTime(now))
	if err != nil {
		return false, fmt.Errorf("insert setup user: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Setup is what "Configure o administrador" sends.
type Setup struct {
	Name     string
	Login    string
	Password string
	Confirm  string
}

// CompleteSetup turns the first-run administrator into the real one: their name, login and
// password replace SetupLogin's. It ends the user's sessions; the caller starts a new one.
// ErrUserNotFound means userID is not the first-run administrator (any more).
func (a *Accounts) CompleteSetup(ctx context.Context, userID int64, in Setup) error {
	errs := form.Errors{}
	name, login := identity(in.Name, in.Login, errs)
	if problem := passwordProblem(in.Password); problem != "" {
		errs["password"] = problem
	} else if in.Confirm != in.Password {
		errs["confirm"] = "As senhas não são iguais."
	}
	if len(errs) > 0 {
		return errs
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), a.hashCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return a.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE users SET name = ?, login = ?, password_hash = ?, failed_logins = 0, locked_until = NULL
			WHERE id = ? AND login = ?`, name, login, string(hash), userID, SetupLogin)
		if db.IsUniqueViolation(err) {
			return form.Errors{"login": "Este login já existe."}
		}
		if err := requireOneRow(res, err); err != nil {
			return err
		}
		return endAllSessions(ctx, tx, userID)
	})
}

// passwordProblem says why password is not acceptable, or returns "" if it is.
func passwordProblem(password string) string {
	switch n := utf8.RuneCountInString(password); {
	case n < minPasswordRunes:
		return fmt.Sprintf("A senha precisa de pelo menos %d caracteres.", minPasswordRunes)
	case n > maxPasswordRunes:
		return fmt.Sprintf("Use no máximo %d caracteres.", maxPasswordRunes)
	case len(password) > maxPasswordBytes:
		return "Senha longa demais: use menos acentos ou símbolos."
	}
	return ""
}

// Authenticate checks a login and password at time now. Five wrong passwords in a row lock the
// login for 15 minutes; while it is locked, even the right password gets ErrLocked.
func (a *Accounts) Authenticate(ctx context.Context, login, password string, now time.Time) (User, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	var (
		u           User
		hash        string
		lockedUntil sql.NullString
	)
	err := a.db.QueryRowContext(ctx,
		`SELECT id, name, login, role, active, password_hash, locked_until FROM users WHERE login = ?`, login).
		Scan(&u.ID, &u.Name, &u.Login, &u.Role, &u.Active, &hash, &lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(a.dummyHash(), []byte(password))
		return User{}, ErrInvalidLogin
	}
	if err != nil {
		return User{}, fmt.Errorf("load user %q: %w", login, err)
	}
	if lockedUntil.Valid && lockedUntil.String > db.FormatTime(now) {
		return User{}, ErrLocked
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || !u.Active {
		return User{}, a.recordFailure(ctx, u.ID, now)
	}

	_, err = a.db.ExecContext(ctx,
		`UPDATE users SET failed_logins = 0, locked_until = NULL, last_login_at = ? WHERE id = ?`,
		db.FormatTime(now), u.ID)
	if err != nil {
		return User{}, fmt.Errorf("record login of user %d: %w", u.ID, err)
	}
	u.LastLoginAt = now
	return u, nil
}

// recordFailure counts a wrong password and locks the login on the fifth one in a row.
func (a *Accounts) recordFailure(ctx context.Context, userID int64, now time.Time) error {
	var failed int
	err := a.db.QueryRowContext(ctx,
		`UPDATE users SET failed_logins = failed_logins + 1 WHERE id = ? RETURNING failed_logins`, userID).
		Scan(&failed)
	if err != nil {
		return fmt.Errorf("count failed login of user %d: %w", userID, err)
	}
	if failed < maxFailedLogins {
		return ErrInvalidLogin
	}
	_, err = a.db.ExecContext(ctx, `UPDATE users SET failed_logins = 0, locked_until = ? WHERE id = ?`,
		db.FormatTime(now.Add(lockDuration)), userID)
	if err != nil {
		return fmt.Errorf("lock user %d: %w", userID, err)
	}
	return ErrLocked
}

// StartSession creates a session for userID and returns the token to put in the cookie.
// Only the token's SHA-256 is stored, so a copy of the database holds no usable session.
func (a *Accounts) StartSession(ctx context.Context, userID int64, now time.Time) (string, error) {
	raw := make([]byte, 32)
	rand.Read(raw) // never fails since Go 1.24; it crashes the program instead
	token := base64.RawURLEncoding.EncodeToString(raw)

	if _, err := a.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, db.FormatTime(now)); err != nil {
		return "", fmt.Errorf("delete expired sessions: %w", err)
	}
	_, err := a.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), userID, db.FormatTime(now.Add(SessionDuration)))
	if err != nil {
		return "", fmt.Errorf("insert session for user %d: %w", userID, err)
	}
	return token, nil
}

// SessionUser returns the active user who owns token, or ErrNoSession.
func (a *Accounts) SessionUser(ctx context.Context, token string, now time.Time) (User, error) {
	if token == "" {
		return User{}, ErrNoSession
	}
	row := a.db.QueryRowContext(ctx, `
		SELECT u.id, u.name, u.login, u.role, u.active, u.last_login_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.active = 1`,
		hashToken(token), db.FormatTime(now))
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSession
	}
	return u, err
}

// EndSession deletes the session behind token (logout).
func (a *Accounts) EndSession(ctx context.Context, token string) error {
	if _, err := a.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// List returns every user, active or not, by name.
func (a *Accounts) List(ctx context.Context) ([]User, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, name, login, role, active, last_login_at FROM users ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// Get returns one user, or ErrUserNotFound.
func (a *Accounts) Get(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(a.db.QueryRowContext(ctx,
		`SELECT id, name, login, role, active, last_login_at FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return u, err
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var (
		u         User
		lastLogin sql.NullString
	)
	if err := row.Scan(&u.ID, &u.Name, &u.Login, &u.Role, &u.Active, &lastLogin); err != nil {
		return User{}, err
	}
	if lastLogin.Valid {
		t, err := db.ParseTime(lastLogin.String)
		if err != nil {
			return User{}, fmt.Errorf("user %d last_login_at: %w", u.ID, err)
		}
		u.LastLoginAt = t
	}
	return u, nil
}

// ResetPassword is the admin setting someone's password. It also lifts a login lock and ends
// that user's sessions.
func (a *Accounts) ResetPassword(ctx context.Context, userID int64, password string) error {
	if problem := passwordProblem(password); problem != "" {
		return form.Errors{"password": problem}
	}
	return a.replacePassword(ctx, userID, password)
}

// ChangeOwnPassword is a user changing their own password. It ends all their sessions;
// the caller starts a new one for the browser in use.
func (a *Accounts) ChangeOwnPassword(ctx context.Context, userID int64, current, next, confirm string) error {
	var hash string
	err := a.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("load password of user %d: %w", userID, err)
	}
	errs := form.Errors{}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		errs["current"] = "Senha atual incorreta."
	}
	if problem := passwordProblem(next); problem != "" {
		errs["password"] = problem
	} else if confirm != next {
		errs["confirm"] = "As senhas não são iguais."
	}
	if len(errs) > 0 {
		return errs
	}
	return a.replacePassword(ctx, userID, next)
}

func (a *Accounts) replacePassword(ctx context.Context, userID int64, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), a.hashCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return a.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ?, failed_logins = 0, locked_until = NULL WHERE id = ?`,
			string(hash), userID)
		if err := requireOneRow(res, err); err != nil {
			return err
		}
		return endAllSessions(ctx, tx, userID)
	})
}

// SetActive deactivates or reactivates a user. Deactivating ends their sessions at once.
// Nobody can deactivate themselves, so at least one admin always stays active.
func (a *Accounts) SetActive(ctx context.Context, actorID, userID int64, active bool) error {
	if actorID == userID && !active {
		return form.Errors{"active": "Você não pode desativar o próprio usuário."}
	}
	return a.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET active = ? WHERE id = ?`, active, userID)
		if err := requireOneRow(res, err); err != nil {
			return err
		}
		if active {
			return nil
		}
		return endAllSessions(ctx, tx, userID)
	})
}

func endAllSessions(ctx context.Context, tx *sql.Tx, userID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("end sessions of user %d: %w", userID, err)
	}
	return nil
}

func requireOneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (a *Accounts) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once committed
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
