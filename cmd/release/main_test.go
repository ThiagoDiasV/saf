package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/update"
)

func TestKeygenThenSignMakesAReleaseSAFTrusts(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "signing.key")
	var stdout bytes.Buffer
	require.NoError(t, keygen(keyFile, &stdout))
	public, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout.String()))
	require.NoError(t, err)
	info, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "only its owner reads the private key")
	private, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	assert.NotContains(t, stdout.String(), strings.TrimSpace(string(private)), "the private key is never printed")
	exe := filepath.Join(dir, "SAF.exe")
	require.NoError(t, os.WriteFile(exe, []byte("SAF 1.4.0"), 0o755))

	require.NoError(t, sign("1.4.0", exe, dir, string(private)))

	manifest, err := os.ReadFile(filepath.Join(dir, "latest.json"))
	require.NoError(t, err)
	encoded, err := os.ReadFile(filepath.Join(dir, "latest.json.sig"))
	require.NoError(t, err)
	signature, err := base64.StdEncoding.DecodeString(string(encoded))
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(public, manifest, signature))
	var m update.Manifest
	require.NoError(t, json.Unmarshal(manifest, &m))
	assert.Equal(t, "1.4.0", m.Version)
	assert.Equal(t, "https://github.com/ThiagoDiasV/saf/releases/download/v1.4.0/SAF.exe", m.URL)
	assert.Len(t, m.SHA256, 64)
}

func TestSignRefusesABadVersionOrKey(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "SAF.exe")
	require.NoError(t, os.WriteFile(exe, []byte("SAF"), 0o755))

	assert.ErrorContains(t, sign("v1.4", exe, t.TempDir(), ""), "want major.minor.patch")
	assert.ErrorContains(t, sign("1.4.0", exe, t.TempDir(), ""), "SAF_SIGNING_KEY must hold")
}

func TestKeygenNeverOverwritesAKey(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "signing.key")
	require.NoError(t, os.WriteFile(keyFile, []byte("existing"), 0o600))

	assert.Error(t, keygen(keyFile, &bytes.Buffer{}))
	assert.Equal(t, "existing", readString(t, keyFile))
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
