// Package auth implements password hashing, JWT issuance/verification,
// refresh-token rotation, and short-lived WebSocket tickets.
//
// Hashing: argon2id with parameters tuned for ~250ms server CPU. The
// encoded format is the PHC string ($argon2id$v=19$m=...,t=...,p=...$salt$hash)
// so we can rotate parameters per-user transparently.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2Params are the canonical hashing parameters for new passwords.
// Existing hashes encode their own params and verify against those — this
// struct is only consulted when hashing fresh.
type argon2Params struct {
	memory      uint32 // KiB
	iterations  uint32
	parallelism uint8
	saltLength  uint32
	keyLength   uint32
}

var defaultParams = argon2Params{
	memory:      64 * 1024, // 64 MiB
	iterations:  3,
	parallelism: 1,
	saltLength:  16,
	keyLength:   32,
}

// ErrInvalidHash is returned when the encoded hash string is malformed or
// uses an unsupported variant. Callers should treat this as
// "credentials invalid" without leaking the parse failure to clients.
var ErrInvalidHash = errors.New("auth: invalid hash format")

// HashPassword computes an argon2id hash with random salt and returns
// the encoded PHC string suitable for storage in users.password_hash.
func HashPassword(plain string) (string, error) {
	if plain == "" {
		return "", errors.New("auth: empty password")
	}
	salt := make([]byte, defaultParams.saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, defaultParams.iterations, defaultParams.memory, defaultParams.parallelism, defaultParams.keyLength)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Key := base64.RawStdEncoding.EncodeToString(key)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, defaultParams.memory, defaultParams.iterations, defaultParams.parallelism,
		b64Salt, b64Key,
	), nil
}

// VerifyPassword checks a plain password against an encoded PHC hash.
// Returns (true, nil) on match, (false, nil) on mismatch, and a non-nil
// error only when the hash is malformed. Comparison is constant-time.
func VerifyPassword(encoded, plain string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return false, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrInvalidHash
	}
	if version != argon2.Version {
		return false, ErrInvalidHash
	}

	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}

	got := argon2.IDKey([]byte(plain), salt, iterations, memory, parallelism, uint32(len(expected)))
	if subtle.ConstantTimeCompare(got, expected) == 1 {
		return true, nil
	}
	return false, nil
}
