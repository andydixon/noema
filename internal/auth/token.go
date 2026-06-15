package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// newToken returns a 256-bit random token in URL-safe base64 and its SHA-256,
// which is what gets stored. A database leak therefore does not leak sessions.
func newToken() (token string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand unavailable: " + err.Error())
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
