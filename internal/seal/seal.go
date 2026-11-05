// Package seal encrypts personal data at rest (patients' first names, photos) with AES-256-GCM.
// A copy of the database or of a backup is useless without the key, which lives outside both.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

const keySize = 32 // AES-256

// ErrOpen means a sealed value did not decrypt: wrong key, wrong context, or tampered data.
var ErrOpen = errors.New("sealed value does not open with this key and context")

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

// NewKey returns a fresh random key, base64-encoded, ready to save in the key file.
//
//	saf keygen > /etc/saf/key && chmod 400 /etc/saf/key
func NewKey() string {
	key := make([]byte, keySize)
	rand.Read(key) // never fails since Go 1.24; it crashes the program instead
	return base64.StdEncoding.EncodeToString(key)
}

// LoadKey reads a key file written from NewKey and returns its Box.
func LoadKey(path string) (*Box, error) {
	if path == "" {
		return nil, errors.New("SAF_KEY_FILE is not set: create a key with `saf keygen` and point SAF_KEY_FILE at it")
	}
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(text)))
	if err != nil || len(key) != keySize {
		return nil, fmt.Errorf("key file %s must hold %d random bytes in base64, as `saf keygen` prints", path, keySize)
	}
	return New(key)
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext. context names where the value lives ("movement:42"): the value only
// opens with the same context, so a ciphertext copied onto another row is refused.
// The result is a random nonce followed by the ciphertext.
func (b *Box) Seal(plaintext []byte, context string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, plaintext, []byte(context))
}

// Open decrypts a value written by Seal with the same context.
func (b *Box) Open(sealed []byte, context string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrOpen
	}
	plaintext, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(context))
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}
