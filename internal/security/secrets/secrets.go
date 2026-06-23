// Package secrets resolves secret references so that plaintext credentials
// never live in the database, configuration exports, logs or HTML (T13, T21).
//
// Reference forms:
//
//	env:NAME          read from the environment at use time
//	file:/path        read from a file (trailing newline trimmed)
//	enc:<base64>      AES-256-GCM ciphertext under the master key
package secrets

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

// Resolver turns references into values.
type Resolver struct {
	key []byte // 32 bytes or nil when no master key is configured
}

// ErrNoMasterKey is returned when enc: references are used without a key.
var ErrNoMasterKey = errors.New("secrets: NOEMA_MASTER_KEY is not configured")

// New creates a resolver. masterKey is base64 (standard or URL, padded or
// not) of 32 bytes, or empty to disable encrypted references.
func New(masterKey string) (*Resolver, error) {
	r := &Resolver{}
	masterKey = strings.TrimSpace(masterKey)
	if masterKey == "" {
		return r, nil
	}
	var key []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err = enc.DecodeString(masterKey); err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: master key is not valid base64")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets: master key must be 32 bytes, got %d", len(key))
	}
	r.key = key
	return r, nil
}

// CanEncrypt reports whether enc: references can be produced and resolved.
func (r *Resolver) CanEncrypt() bool { return r != nil && r.key != nil }

// IsReference reports whether s has a recognised prefix.
func IsReference(s string) bool {
	return strings.HasPrefix(s, "env:") || strings.HasPrefix(s, "file:") || strings.HasPrefix(s, "enc:")
}

// Resolve returns the secret value for a reference. Empty references resolve
// to empty strings so optional secrets need no special casing.
func (r *Resolver) Resolve(ref string) (string, error) {
	switch {
	case ref == "":
		return "", nil
	case strings.HasPrefix(ref, "env:"):
		name := strings.TrimPrefix(ref, "env:")
		v, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("secrets: environment variable %s is not set", name)
		}
		return v, nil
	case strings.HasPrefix(ref, "file:"):
		b, err := os.ReadFile(strings.TrimPrefix(ref, "file:"))
		if err != nil {
			return "", fmt.Errorf("secrets: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	case strings.HasPrefix(ref, "enc:"):
		if !r.CanEncrypt() {
			return "", ErrNoMasterKey
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, "enc:"))
		if err != nil {
			return "", fmt.Errorf("secrets: bad ciphertext encoding")
		}
		block, _ := aes.NewCipher(r.key)
		gcm, _ := cipher.NewGCM(block)
		if len(raw) < gcm.NonceSize() {
			return "", fmt.Errorf("secrets: ciphertext too short")
		}
		plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
		if err != nil {
			return "", fmt.Errorf("secrets: decryption failed (wrong master key?)")
		}
		return string(plain), nil
	default:
		return "", fmt.Errorf("secrets: unrecognised reference form (use env:, file: or enc:)")
	}
}

// Encrypt produces an enc: reference for a plaintext typed into the UI.
func (r *Resolver) Encrypt(plain string) (string, error) {
	if !r.CanEncrypt() {
		return "", ErrNoMasterKey
	}
	block, _ := aes.NewCipher(r.key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return "enc:" + base64.RawURLEncoding.EncodeToString(ct), nil
}

// Describe returns a display string for a reference that reveals nothing.
func Describe(ref string) string {
	switch {
	case ref == "":
		return "not set"
	case strings.HasPrefix(ref, "env:"):
		return "environment variable " + strings.TrimPrefix(ref, "env:")
	case strings.HasPrefix(ref, "file:"):
		return "file " + strings.TrimPrefix(ref, "file:")
	case strings.HasPrefix(ref, "enc:"):
		return "encrypted in database"
	default:
		return "invalid reference"
	}
}
