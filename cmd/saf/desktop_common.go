package main

// The parts of the desktop app that need no window, so they build and get tested everywhere:
// where its files live, opening them, and the guard that lets only its own window in.

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"saf/internal/seal"
)

const (
	keyFileName     = "chave.key" // next to the program, outside dados/: Drive never copies it
	dataDirName     = "dados"
	versionFileName = "versao" // in dados/: the version that ran last
	windowCookie    = "saf_janela"
)

// portableDir is the folder SAF keeps its files in: the one holding SAF.exe, or on a Mac the one
// holding SAF.app (the program itself sits inside the bundle).
func portableDir(exe string) string {
	dir := filepath.Dir(exe)
	bundle := filepath.Dir(filepath.Dir(dir))
	if filepath.Base(dir) == "MacOS" && strings.HasSuffix(bundle, ".app") {
		return filepath.Dir(bundle)
	}
	return dir
}

// openPortable opens SAF's files in dir: the key (made on the first start), the database in
// dados/, and the first-run administrator. keyText is the key as saved, for the setup page.
func openPortable(dir string) (store *sql.DB, box *seal.Box, keyText string, err error) {
	keyFile := filepath.Join(dir, keyFileName)
	if err := createKey(keyFile, filepath.Join(dir, dataDirName, "saf.db")); err != nil {
		return nil, nil, "", err
	}
	box, keyText, err = seal.LoadKey(keyFile)
	if err != nil {
		return nil, nil, "", err
	}
	store, err = openStore(filepath.Join(dir, dataDirName))
	if err != nil {
		return nil, nil, "", err
	}
	if err := firstRun(store); err != nil {
		store.Close()
		return nil, nil, "", err
	}
	return store, box, keyText, nil
}

// createKey writes a new key at keyFile on the first start. If the database already exists, the
// key is missing rather than new: making another would leave the names and photos unreadable,
// so SAF stops and asks for the printed one.
func createKey(keyFile, database string) error {
	if _, err := os.Stat(keyFile); err == nil {
		return nil
	}
	if _, err := os.Stat(database); err == nil {
		return fmt.Errorf("a chave %s não está ao lado do SAF. Crie esse arquivo com o texto da chave impressa", keyFile)
	}
	f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create key %s: %w", keyFile, err)
	}
	_, err = f.WriteString(seal.NewKey() + "\n")
	return errors.Join(err, f.Close())
}

// recordVersion notes version as the one running in dataDir and reports whether another one ran
// before, that is, an update just took effect. existingData says the database was there before
// this start: without a note it means the earlier version predates the note; with neither,
// this is a fresh install, with nothing to report. Development builds report nothing.
func recordVersion(dataDir, version string, existingData bool) (bool, error) {
	if version == "dev" {
		return false, nil
	}
	file := filepath.Join(dataDir, versionFileName)
	previous, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	last := strings.TrimSpace(string(previous))
	if err := os.WriteFile(file, []byte(version+"\n"), 0o600); err != nil {
		return false, err
	}
	if last == "" {
		return existingData, nil
	}
	return last != version, nil
}

// onlyThisWindow answers only the window SAF opened. The window starts at /abrir?t=<token>,
// which trades the launch token for a cookie; every other request must carry it, so other
// programs on the PC that find the port get nothing.
func onlyThisWindow(token string, next http.Handler) http.Handler {
	matches := func(s string) bool { return subtle.ConstantTimeCompare([]byte(s), []byte(token)) == 1 }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/abrir" && matches(r.URL.Query().Get("t")) {
			// Lax, not Strict: the window arrives here from Wails' own page, another site, and
			// browsers withhold Strict cookies for the rest of that navigation, the redirect included.
			http.SetCookie(w, &http.Cookie{Name: windowCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, "/estoque", http.StatusSeeOther)
			return
		}
		if c, err := r.Cookie(windowCookie); err != nil || !matches(c.Value) {
			http.Error(w, "Abra o SAF pelo ícone.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
