package seal

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testBox(t *testing.T) *Box {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(NewKey())
	require.NoError(t, err)
	box, err := New(key)
	require.NoError(t, err)
	return box
}

func TestSealOpenRoundTrip(t *testing.T) {
	box := testBox(t)

	sealed := box.Seal([]byte("Maria"), "movement:42")

	assert.False(t, bytes.Contains(sealed, []byte("Maria")))
	opened, err := box.Open(sealed, "movement:42")
	require.NoError(t, err)
	assert.Equal(t, "Maria", string(opened))
	assert.NotEqual(t, sealed, box.Seal([]byte("Maria"), "movement:42"), "a fresh nonce every time")
}

func TestOpenRefusesWrongContextKeyOrTampering(t *testing.T) {
	box := testBox(t)
	sealed := box.Seal([]byte("Maria"), "movement:42")

	_, err := box.Open(sealed, "movement:43")
	assert.ErrorIs(t, err, ErrOpen, "copied onto another row")

	_, err = testBox(t).Open(sealed, "movement:42")
	assert.ErrorIs(t, err, ErrOpen, "another key")

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	_, err = box.Open(tampered, "movement:42")
	assert.ErrorIs(t, err, ErrOpen)

	_, err = box.Open([]byte("short"), "movement:42")
	assert.ErrorIs(t, err, ErrOpen)
}

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(good, []byte(NewKey()+"\n"), 0o400))

	box, err := LoadKey(good)
	require.NoError(t, err)
	opened, err := box.Open(box.Seal([]byte("ok"), "c"), "c")
	require.NoError(t, err)
	assert.Equal(t, "ok", string(opened))

	_, err = LoadKey("")
	assert.ErrorContains(t, err, "SAF_KEY_FILE is not set")
	_, err = LoadKey(filepath.Join(dir, "missing"))
	assert.ErrorContains(t, err, "read key file")
	short := filepath.Join(dir, "short")
	require.NoError(t, os.WriteFile(short, []byte(base64.StdEncoding.EncodeToString([]byte("too short"))), 0o400))
	_, err = LoadKey(short)
	assert.ErrorContains(t, err, "must hold 32 random bytes in base64")
}
