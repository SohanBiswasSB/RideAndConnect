package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const refreshTokenBytes = 32

// NewRefreshToken returns a freshly minted opaque refresh token plus its
// SHA-256 hash. We store only the hash in DB so a leaked database row
// can't be used to mint access tokens.
func NewRefreshToken() (token string, hash []byte, err error) {
	b := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("auth: refresh token rand: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	return token, h[:], nil
}

// HashRefreshToken returns the SHA-256 hash of an opaque refresh token,
// used for DB lookups by hash.
func HashRefreshToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
