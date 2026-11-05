package web

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/auth"
	"saf/internal/update"
)

// withUpdates rebuilds app's handler as the Windows desktop app has it: version 1.2.0, and an
// update manager reading a release server that publishes version.
func withUpdates(t *testing.T, app *testApp, version string) (m *update.Manager, restarted chan struct{}) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	exe := []byte("SAF " + version)
	var manifest, signature []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			w.Write(manifest)
		case "/latest.json.sig":
			w.Write(signature)
		case "/SAF.exe":
			w.Write(exe)
		}
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(exe)
	manifest, signature, err = update.Sign(update.Manifest{Version: version, URL: srv.URL + "/SAF.exe", SHA256: hex.EncodeToString(sum[:])}, key)
	require.NoError(t, err)

	running := filepath.Join(t.TempDir(), "SAF.exe")
	require.NoError(t, os.WriteFile(running, []byte("SAF 1.2.0"), 0o755))
	restarted = make(chan struct{}, 1)
	m = &update.Manager{
		Source:  update.Source{BaseURL: srv.URL, Manifest: "latest.json", Key: key.Public().(ed25519.PublicKey), Client: srv.Client()},
		Current: update.Version{Major: 1, Minor: 2},
		Exe:     running,
		Restart: func() { restarted <- struct{}{} },
	}
	require.NoError(t, m.Check(context.Background()))
	app.handler = Handler(Config{Accounts: app.accounts, Inventory: app.inventory, KeyText: testKeyText,
		Version: "1.2.0", Updates: m, Now: func() time.Time { return app.now }})
	return m, restarted
}

func TestMajorVersionWaitsForAnAdministrator(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	m, restarted := withUpdates(t, app, "2.0.0")
	admin := app.login(t, "paulo.mendes", testPassword)
	viewer := app.login(t, "thiago.dias", testPassword)

	assert.Contains(t, app.get("/estoque", admin).Body.String(), "A versão 2.0.0 do SAF está disponível")
	assert.NotContains(t, app.get("/estoque", viewer).Body.String(), "2.0.0")
	assert.Equal(t, http.StatusForbidden, app.post("/atualizar", nil, viewer).Code)

	res := app.post("/atualizar", nil, admin)
	require.Equal(t, http.StatusSeeOther, res.Code)
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("SAF did not restart after installing 2.0.0")
	}
	assert.Equal(t, update.Status{Installed: "2.0.0"}, m.Status())
}

func TestInstalledVersionOffersARestartToAnyone(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	_, restarted := withUpdates(t, app, "1.3.0") // a minor version installs by itself
	viewer := app.login(t, "thiago.dias", testPassword)

	page := app.get("/estoque", viewer).Body.String()
	assert.Contains(t, page, "A versão 1.3.0 do SAF já foi instalada")
	assert.Contains(t, page, "SAF 1.2.0", "the running version, in the user menu")
	assert.Contains(t, app.get("/login").Body.String(), "Versão 1.2.0", "and on the login page")

	require.Equal(t, http.StatusSeeOther, app.post("/reiniciar", nil, viewer).Code)
	assert.Len(t, restarted, 1)
}

func TestWithoutUpdatesThereIsNoBanner(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Paulo Mendes", "paulo.mendes", auth.RoleAdmin)
	admin := app.login(t, "paulo.mendes", testPassword)

	assert.NotContains(t, app.get("/estoque", admin).Body.String(), "update-bar")
	assert.Equal(t, http.StatusNotFound, app.post("/atualizar", nil, admin).Code)
	assert.Equal(t, http.StatusNotFound, app.post("/reiniciar", nil, admin).Code)
}
