package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// jwtScope discriminates token kinds carried in JWTs. v1 only issues
// `access` JWTs; the WS ticket is now a DB-backed opaque single-use
// token (see internal/auth/wsticket.go) so it has no JWT representation.
type jwtScope string

const (
	scopeAccess jwtScope = "access"
)

// Claims is the canonical claim set. We avoid embedding PII (name/email);
// resolvers fetch the user record from the DB on each request.
type Claims struct {
	Scope  string `json:"scope"`
	jwt.RegisteredClaims
}

// IssueAccessToken signs an access JWT for the given user. Returns the
// signed string and its absolute expiry time.
func (s *Service) IssueAccessToken(userID uuid.UUID) (string, time.Time, error) {
	return s.issueScoped(userID, scopeAccess, s.cfg.AccessTTL)
}

// VerifyAccessToken parses and validates an access JWT. Returns the user
// ID on success.
func (s *Service) VerifyAccessToken(token string) (uuid.UUID, error) {
	return s.verifyScoped(token, scopeAccess)
}

func (s *Service) issueScoped(userID uuid.UUID, scope jwtScope, ttl time.Duration) (string, time.Time, error) {
	now := time.Now().UTC()
	exp := now.Add(ttl)
	claims := Claims{
		Scope: string(scope),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			NotBefore: jwt.NewNumericDate(now),
			ID:        uuid.NewString(),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.cfg.JWTSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign jwt: %w", err)
	}
	return signed, exp, nil
}

func (s *Service) verifyScoped(token string, want jwtScope) (uuid.UUID, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("auth: unexpected signing method")
		}
		return s.cfg.JWTSecret, nil
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("auth: parse jwt: %w", err)
	}
	if !parsed.Valid {
		return uuid.Nil, errors.New("auth: invalid jwt")
	}
	if claims.Scope != string(want) {
		return uuid.Nil, fmt.Errorf("auth: wrong token scope: want %s, got %s", want, claims.Scope)
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, fmt.Errorf("auth: parse subject: %w", err)
	}
	return id, nil
}
