package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/auth"
)

func TestPortableDir(t *testing.T) {
	assert.Equal(t, filepath.FromSlash("/SAF"), portableDir(filepath.FromSlash("/SAF/SAF.exe")))
	assert.Equal(t, filepath.FromSlash("/Users/x/dist"), portableDir(filepath.FromSlash("/Users/x/dist/SAF.app/Contents/MacOS/SAF")),
		"next to the bundle, not inside it")
}

func TestOpenPortableFirstStartAndLater(t *testing.T) {
	dir := t.TempDir()

	store, _, keyText, err := openPortable(dir)
	require.NoError(t, err)
	users, err := auth.NewAccounts(store, auth.HashCost).List(context.Background())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	assert.Len(t, users, 1, "the first-run administrator")
	saved, err := os.ReadFile(filepath.Join(dir, "chave.key"))
	require.NoError(t, err)
	assert.Equal(t, string(saved), keyText+"\n")
	assert.FileExists(t, filepath.Join(dir, "dados", "saf.db"))

	store, _, again, err := openPortable(dir)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	assert.Equal(t, keyText, again, "the next start keeps the key")
}

func TestOpenPortableRefusesANewKeyForExistingData(t *testing.T) {
	dir := t.TempDir()
	store, _, _, err := openPortable(dir)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, os.Remove(filepath.Join(dir, "chave.key")))

	_, _, _, err = openPortable(dir)

	assert.ErrorContains(t, err, "não está ao lado do SAF")
	assert.NoFileExists(t, filepath.Join(dir, "chave.key"), "no new key next to old data")
}

func TestOnlyThisWindow(t *testing.T) {
	saf := onlyThisWindow("token-certo", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("SAF"))
	}))
	get := func(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		res := httptest.NewRecorder()
		saf.ServeHTTP(res, r)
		return res
	}

	assert.Equal(t, http.StatusForbidden, get("/estoque").Code, "another program without the cookie")
	assert.Equal(t, http.StatusForbidden, get("/abrir?t=token-errado").Code)
	assert.Equal(t, http.StatusForbidden, get("/estoque", &http.Cookie{Name: windowCookie, Value: "token-errado"}).Code)

	opened := get("/abrir?t=token-certo")
	require.Equal(t, http.StatusSeeOther, opened.Code)
	assert.Equal(t, "/estoque", opened.Header().Get("Location"))
	cookies := opened.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)

	page := get("/estoque", cookies[0])
	assert.Equal(t, http.StatusOK, page.Code)
	assert.Equal(t, "SAF", page.Body.String())
}

func TestRecordVersionSaysWhenAnUpdateTookEffect(t *testing.T) {
	dir := t.TempDir()
	start := func(version string, existingData bool) bool {
		t.Helper()
		updated, err := recordVersion(dir, version, existingData)
		require.NoError(t, err)
		return updated
	}

	assert.False(t, start("1.0.0", false), "a fresh install has nothing to report")
	assert.False(t, start("1.0.0", true), "the next start on the same version")
	assert.True(t, start("1.0.1", true), "an update took effect")
	assert.False(t, start("1.0.1", true))
	assert.Equal(t, "1.0.1\n", readFileString(t, filepath.Join(dir, "versao")))

	older := t.TempDir()
	updated, err := recordVersion(older, "1.0.1", true)
	require.NoError(t, err)
	assert.True(t, updated, "data from a version before the note: an update")

	dev := t.TempDir()
	updated, err = recordVersion(dev, "dev", true)
	require.NoError(t, err)
	assert.False(t, updated)
	assert.NoFileExists(t, filepath.Join(dev, "versao"), "development builds note nothing")
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
