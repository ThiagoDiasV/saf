// Command release signs SAF releases. The release job (.github/workflows/release.yml) runs
// `sign`; `keygen` makes the signing key once.
//
//	release keygen <private-key-file>    write a new private key there, print the public key
//	release sign <version> <goos> <dir>  sign the program of goos in dir (SAF.exe, SAF-mac): write
//	                                     its manifest and .sig there, with the key in SAF_SIGNING_KEY
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"saf/internal/update"
)

// downloadURL is where GitHub serves a file of a release.
const downloadURL = "https://github.com/ThiagoDiasV/saf/releases/download/v%s/%s"

func main() {
	var err error
	switch {
	case len(os.Args) == 3 && os.Args[1] == "keygen":
		err = keygen(os.Args[2], os.Stdout)
	case len(os.Args) == 5 && os.Args[1] == "sign":
		err = sign(os.Args[2], os.Args[3], os.Args[4], os.Getenv("SAF_SIGNING_KEY"))
	default:
		err = errors.New("usage: release keygen <private-key-file> | release sign <version> <goos> <dir>")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

// keygen writes a new private key (the base64 seed) to path, readable only by its owner, and
// writes the public key, for update.ReleaseKey, to out. The private key never goes to out.
func keygen(path string, out io.Writer) error {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(private.Seed()) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, base64.StdEncoding.EncodeToString(public))
	return err
}

// sign writes the manifest and its signature for the program of goos (in dir) of version.
func sign(version, goos, dir, encodedKey string) error {
	if _, err := update.ParseVersion(version); err != nil {
		return err
	}
	asset, ok := update.AssetFor(goos)
	if !ok {
		return fmt.Errorf("SAF has no releases for %s", goos)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("SAF_SIGNING_KEY must hold the %d-byte key `release keygen` wrote, in base64", ed25519.SeedSize)
	}
	body, err := os.ReadFile(filepath.Join(dir, asset.Program))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	m := update.Manifest{Version: version, URL: fmt.Sprintf(downloadURL, version, asset.Program), SHA256: hex.EncodeToString(sum[:])}
	manifest, signature, err := update.Sign(m, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, asset.Manifest), manifest, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, asset.Manifest+".sig"), signature, 0o644)
}
