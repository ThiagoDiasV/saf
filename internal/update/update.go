// Package update keeps the pharmacy's SAF.exe current from this repository's GitHub releases.
//
// Each release carries latest.json (its version, the address of its SAF.exe and that file's
// SHA-256) and latest.json.sig, an ed25519 signature made in CI with a key only the release job
// holds. SAF installs a release only when the signature matches ReleaseKey, built into it, so a
// swapped download or a hijacked address can't put another program on the PC.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ReleaseKey is the public half of the release signing key (base64). The private half is the
// SAF_SIGNING_KEY secret of the "release" environment on GitHub.
const ReleaseKey = "3QDhJWHXIDgKIww3qqkeHC6pL9JKn1egTpWjCVAkRzM="

// LatestURL is where the newest release's latest.json and latest.json.sig are downloaded from.
const LatestURL = "https://github.com/ThiagoDiasV/saf/releases/latest/download"

// GitHub is where SAF's releases are and the key they are signed with.
func GitHub() (Source, error) {
	key, err := base64.StdEncoding.DecodeString(ReleaseKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return Source{}, fmt.Errorf("ReleaseKey must be a %d-byte ed25519 public key in base64, as `release keygen` prints", ed25519.PublicKeySize)
	}
	return Source{BaseURL: LatestURL, Key: key, Client: &http.Client{Timeout: 15 * time.Minute}}, nil
}

// PreviousName is what the replaced SAF.exe is renamed to, kept for a manual rollback.
const PreviousName = "SAF.anterior.exe"

const (
	maxManifestBytes = 16 << 10
	maxExeBytes      = 200 << 20 // a release is about 20 MB
)

// Version is a release number: major.minor.patch. A new major version may change how SAF works,
// so an administrator accepts it; minor and patch versions install by themselves.
type Version struct{ Major, Minor, Patch int }

// ParseVersion reads "1.2.3" or "v1.2.3".
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	var n [3]int
	if len(parts) != len(n) {
		return Version{}, fmt.Errorf("version %q: want major.minor.patch, e.g. 1.4.2", s)
	}
	for i, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 {
			return Version{}, fmt.Errorf("version %q: want major.minor.patch, e.g. 1.4.2", s)
		}
		n[i] = v
	}
	return Version{n[0], n[1], n[2]}, nil
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

func (v Version) before(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// Manifest is latest.json.
type Manifest struct {
	Version string `json:"version"`
	URL     string `json:"url"`    // the release's SAF.exe, https only
	SHA256  string `json:"sha256"` // of that file, hex
}

// Sign returns latest.json for m and its signature, base64. The release job runs it.
//
//	manifest, signature, err := update.Sign(m, privateKey)
func Sign(m Manifest, key ed25519.PrivateKey) (manifest, signature []byte, err error) {
	manifest, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return manifest, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, manifest))), nil
}

// Source is where releases come from and the key they must be signed with.
type Source struct {
	BaseURL string // holds latest.json and latest.json.sig
	Key     ed25519.PublicKey
	Client  *http.Client
}

// Latest returns the newest release's manifest, once its signature is checked.
func (s Source) Latest(ctx context.Context) (Manifest, error) {
	manifest, err := s.get(ctx, s.BaseURL+"/latest.json", maxManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	encoded, err := s.get(ctx, s.BaseURL+"/latest.json.sig", maxManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || !ed25519.Verify(s.Key, manifest, signature) {
		return Manifest{}, errors.New("latest.json is not signed with SAF's release key")
	}
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return Manifest{}, fmt.Errorf("read latest.json: %w", err)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return Manifest{}, err
	}
	if !strings.HasPrefix(m.URL, "https://") {
		return Manifest{}, fmt.Errorf("latest.json: download address %q is not https", m.URL)
	}
	return m, nil
}

// Install downloads m's SAF.exe next to exe, checks its SHA-256, and swaps it in: exe is renamed
// to PreviousName and the new file takes its name. Windows lets a running program be renamed, so
// this works while SAF is open; the new version runs from the next start.
func (s Source) Install(ctx context.Context, m Manifest, exe string) error {
	body, err := s.get(ctx, m.URL, maxExeBytes)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != strings.ToLower(m.SHA256) {
		return fmt.Errorf("SAF.exe %s: SHA-256 %x does not match latest.json's %s", m.Version, sum, m.SHA256)
	}
	dir := filepath.Dir(exe)
	next := filepath.Join(dir, "SAF.novo.exe")
	if err := writeExe(next, body); err != nil {
		return err
	}
	previous := filepath.Join(dir, PreviousName)
	if err := os.Remove(previous); err != nil && !errors.Is(err, fs.ErrNotExist) {
		os.Remove(next)
		return fmt.Errorf("remove the older %s: %w", PreviousName, err)
	}
	if err := os.Rename(exe, previous); err != nil {
		os.Remove(next)
		return fmt.Errorf("set the running SAF aside: %w", err)
	}
	if err := os.Rename(next, exe); err != nil {
		os.Rename(previous, exe) // put the running version back under its name
		return fmt.Errorf("put SAF %s in place: %w", m.Version, err)
	}
	return nil
}

func (s Source) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("GET %s: more than %d bytes", url, limit)
	}
	return body, nil
}

func writeExe(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Manager is the desktop app's side: Check installs newer minor and patch versions by itself and
// keeps a newer major one waiting until an administrator accepts it.
type Manager struct {
	Source  Source
	Current Version
	Exe     string // the running SAF.exe
	// Restart closes SAF and opens it again on the installed version. The desktop app sets it.
	Restart func()

	mu        sync.Mutex
	waiting   *Manifest // a newer major version
	installed string    // installed; runs from the next start
}

// Status is what the banner shows.
type Status struct {
	Waiting   string // a newer major version an administrator can install
	Installed string // installed, runs once SAF restarts
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Status{Installed: m.installed}
	if m.waiting != nil {
		s.Waiting = m.waiting.Version
	}
	return s
}

// Check looks at the newest release once.
func (m *Manager) Check(ctx context.Context) error {
	latest, err := m.Source.Latest(ctx)
	if err != nil {
		return err
	}
	v, err := ParseVersion(latest.Version)
	if err != nil {
		return err
	}
	m.mu.Lock()
	known := m.installed == latest.Version || (m.waiting != nil && m.waiting.Version == latest.Version)
	m.mu.Unlock()
	switch {
	case !m.Current.before(v) || known:
		return nil
	case v.Major > m.Current.Major:
		m.mu.Lock()
		m.waiting = &latest
		m.mu.Unlock()
		return nil
	}
	return m.install(ctx, latest)
}

// Accept installs the waiting major version and restarts SAF on it.
func (m *Manager) Accept(ctx context.Context) error {
	m.mu.Lock()
	waiting := m.waiting
	m.mu.Unlock()
	if waiting == nil {
		return errors.New("no major version is waiting")
	}
	if err := m.install(ctx, *waiting); err != nil {
		return err
	}
	m.Restart()
	return nil
}

func (m *Manager) install(ctx context.Context, release Manifest) error {
	if err := m.Source.Install(ctx, release, m.Exe); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.installed = release.Version
	if m.waiting != nil && m.waiting.Version == release.Version {
		m.waiting = nil
	}
	return nil
}
