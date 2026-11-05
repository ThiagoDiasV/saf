package update

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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

// releases is a GitHub stand-in: an HTTPS server holding one published release.
type releases struct {
	server    *httptest.Server
	key       ed25519.PrivateKey
	manifest  []byte
	signature []byte
	exe       []byte
	downloads atomic.Int32 // of SAF.exe
}

func newReleases(t *testing.T) *releases {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	r := &releases{key: key}
	r.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/latest.json":
			w.Write(r.manifest)
		case "/latest.json.sig":
			w.Write(r.signature)
		case "/SAF.exe":
			r.downloads.Add(1)
			w.Write(r.exe)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

// publish makes version the newest release, with exe as its SAF.exe.
func (r *releases) publish(t *testing.T, version, exe string) Manifest {
	t.Helper()
	sum := sha256.Sum256([]byte(exe))
	m := Manifest{Version: version, URL: r.server.URL + "/SAF.exe", SHA256: hex.EncodeToString(sum[:])}
	var err error
	r.manifest, r.signature, err = Sign(m, r.key)
	require.NoError(t, err)
	r.exe = []byte(exe)
	return m
}

func (r *releases) source() Source {
	return Source{BaseURL: r.server.URL, Manifest: "latest.json", Key: r.key.Public().(ed25519.PublicKey), Client: r.server.Client()}
}

// runningExe is a stand-in for the SAF.exe that is running, in its own folder.
func runningExe(t *testing.T, contents string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "SAF.exe")
	require.NoError(t, os.WriteFile(exe, []byte(contents), 0o755))
	return exe
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestGitHubHasReleasesForWindowsAndTheMac(t *testing.T) {
	windows, err := GitHub("windows")
	require.NoError(t, err, "a release built with this ReleaseKey could never update")
	assert.Equal(t, "latest.json", windows.Manifest, "the name the first releases use")
	mac, err := GitHub("darwin")
	require.NoError(t, err)
	assert.Equal(t, "latest-mac.json", mac.Manifest)

	_, err = GitHub("linux")
	assert.ErrorContains(t, err, "no releases for linux")
}

func TestParseVersion(t *testing.T) {
	v, err := ParseVersion("v1.12.3")
	require.NoError(t, err)
	assert.Equal(t, Version{1, 12, 3}, v)
	assert.Equal(t, "1.12.3", v.String())

	for _, bad := range []string{"", "dev", "1.2", "1.2.3.4", "1.-2.3", "1.x.3"} {
		_, err := ParseVersion(bad)
		assert.ErrorContains(t, err, "want major.minor.patch", bad)
	}
}

func TestLatestTrustsOnlyReleasesSignedWithTheKey(t *testing.T) {
	r := newReleases(t)
	published := r.publish(t, "1.3.0", "SAF 1.3.0")

	m, err := r.source().Latest(ctx)
	require.NoError(t, err)
	assert.Equal(t, published, m)

	r.manifest = []byte(`{"version":"9.9.9","url":"https://evil.example/SAF.exe","sha256":"00"}`)
	_, err = r.source().Latest(ctx)
	assert.ErrorContains(t, err, "not signed with SAF's release key", "a changed latest.json")

	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	r.manifest, r.signature, err = Sign(published, otherKey)
	require.NoError(t, err)
	_, err = r.source().Latest(ctx)
	assert.ErrorContains(t, err, "not signed with SAF's release key", "signed with another key")

	r.manifest, r.signature, err = Sign(Manifest{Version: "1.3.0", URL: "http://plain.example/SAF.exe"}, r.key)
	require.NoError(t, err)
	_, err = r.source().Latest(ctx)
	assert.ErrorContains(t, err, "is not https")
}

func TestInstallSwapsTheProgramAndKeepsThePrevious(t *testing.T) {
	r := newReleases(t)
	m := r.publish(t, "1.3.0", "SAF 1.3.0")
	exe := runningExe(t, "SAF 1.2.0")
	previous := filepath.Join(filepath.Dir(exe), "SAF.anterior.exe")
	require.NoError(t, os.WriteFile(previous, []byte("SAF 1.1.0"), 0o755))

	require.NoError(t, r.source().Install(ctx, m, exe))

	assert.Equal(t, "SAF 1.3.0", readFile(t, exe))
	assert.Equal(t, "SAF 1.2.0", readFile(t, previous))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(exe), "SAF.novo.exe"))
}

func TestInstallOnTheMacKeepsThePreviousAsSAFAnterior(t *testing.T) {
	r := newReleases(t)
	m := r.publish(t, "1.3.0", "SAF 1.3.0")
	exe := filepath.Join(t.TempDir(), "SAF") // inside SAF.app/Contents/MacOS
	require.NoError(t, os.WriteFile(exe, []byte("SAF 1.2.0"), 0o755))

	require.NoError(t, r.source().Install(ctx, m, exe))

	assert.Equal(t, "SAF 1.3.0", readFile(t, exe))
	assert.Equal(t, "SAF 1.2.0", readFile(t, filepath.Join(filepath.Dir(exe), "SAF.anterior")))
	assert.Equal(t, filepath.Join(filepath.Dir(exe), "SAF.anterior"), PreviousPath(exe))
}

func TestInstallRefusesADownloadThatDoesNotMatch(t *testing.T) {
	r := newReleases(t)
	m := r.publish(t, "1.3.0", "SAF 1.3.0")
	r.exe = []byte("something else")
	exe := runningExe(t, "SAF 1.2.0")

	err := r.source().Install(ctx, m, exe)

	assert.ErrorContains(t, err, "does not match the manifest")
	assert.Equal(t, "SAF 1.2.0", readFile(t, exe), "the running version stays")
	assert.NoFileExists(t, filepath.Join(filepath.Dir(exe), "SAF.novo.exe"))
}

func TestCheckInstallsMinorAndPatchVersionsByItself(t *testing.T) {
	r := newReleases(t)
	exe := runningExe(t, "SAF 1.2.0")
	m := &Manager{Source: r.source(), Current: Version{1, 2, 0}, Exe: exe}

	r.publish(t, "1.2.0", "SAF 1.2.0")
	require.NoError(t, m.Check(ctx))
	assert.Equal(t, Status{}, m.Status(), "already the newest")

	r.publish(t, "1.3.1", "SAF 1.3.1")
	require.NoError(t, m.Check(ctx))
	assert.Equal(t, Status{Installed: "1.3.1"}, m.Status())
	assert.Equal(t, "SAF 1.3.1", readFile(t, exe))

	require.NoError(t, m.Check(ctx))
	assert.Equal(t, int32(1), r.downloads.Load(), "the next day's check downloads nothing")
}

func TestCheckLeavesAMajorVersionToAnAdministrator(t *testing.T) {
	r := newReleases(t)
	exe := runningExe(t, "SAF 1.2.0")
	restarted := false
	m := &Manager{Source: r.source(), Current: Version{1, 2, 0}, Exe: exe, Restart: func() { restarted = true }}
	r.publish(t, "2.0.0", "SAF 2.0.0")

	require.NoError(t, m.Check(ctx))
	assert.Equal(t, Status{Waiting: "2.0.0"}, m.Status())
	assert.Equal(t, "SAF 1.2.0", readFile(t, exe), "nothing installed before the click")
	assert.Zero(t, r.downloads.Load())

	require.NoError(t, m.Accept(ctx))
	assert.Equal(t, Status{Installed: "2.0.0"}, m.Status())
	assert.Equal(t, "SAF 2.0.0", readFile(t, exe))
	assert.True(t, restarted)
}

func TestCheckNeverGoesBack(t *testing.T) {
	r := newReleases(t)
	exe := runningExe(t, "SAF 1.2.0")
	m := &Manager{Source: r.source(), Current: Version{1, 2, 0}, Exe: exe}
	r.publish(t, "1.1.9", "SAF 1.1.9") // an older release, signed and replayed

	require.NoError(t, m.Check(ctx))

	assert.Equal(t, Status{}, m.Status())
	assert.Equal(t, "SAF 1.2.0", readFile(t, exe))
}

func TestAcceptWithNothingWaiting(t *testing.T) {
	m := &Manager{Current: Version{1, 2, 0}}

	assert.ErrorContains(t, m.Accept(ctx), "no major version is waiting")
}
