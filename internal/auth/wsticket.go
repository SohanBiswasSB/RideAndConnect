package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// wsTicketBytes is the entropy for the opaque single-use ticket. 32
// bytes URL-safe base64-encoded ~= 43 chars on the wire — short enough
// to fit a query string, long enough to be unguessable.
const wsTicketBytes = 32

// ErrWSTicketInvalid is returned when the ticket is unknown, expired, or
// already consumed. Callers should treat all three the same — never tell
// the client which one happened.
var ErrWSTicketInvalid = errors.New("auth: ws ticket invalid")

// IssueWSTicket creates a single-use opaque token bound to userID,
// persists its SHA-256 hash, and returns the raw token plus its expiry.
// The opaque token replaces the previous JWT-based design: a leaked
// ticket cannot be replayed because consumption is recorded server-side.
func (s *Service) IssueWSTicket(userID uuid.UUID) (token string, expiresAt time.Time, err error) {
	b := make([]byte, wsTicketBytes)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: ws ticket rand: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	hash := sha256.Sum256([]byte(token))
	expiresAt = time.Now().UTC().Add(s.cfg.WSTicketTTL)

	_, err = s.pool.Exec(context.Background(), `
		INSERT INTO ws_tickets (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, userID, hash[:], expiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: persist ws ticket: %w", err)
	}
	return token, expiresAt, nil
}

// VerifyWSTicket consumes a single-use ticket. On success returns the
// associated user ID and marks the row as consumed atomically — a second
// presentation returns ErrWSTicketInvalid.
//
// Background context for the DB call so a slow Read upgrade doesn't
// abort the consumption transaction; the call is short and bounded by
// pgx's pool acquire timeout.
func (s *Service) VerifyWSTicket(token string) (uuid.UUID, error) {
	if token == "" {
		return uuid.Nil, ErrWSTicketInvalid
	}
	hash := sha256.Sum256([]byte(token))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return uuid.Nil, fmt.Errorf("auth: begin ws ticket: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		id        uuid.UUID
		userID    uuid.UUID
		expiresAt time.Time
		consumed  *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, expires_at, consumed_at
		  FROM ws_tickets
		 WHERE token_hash = $1
		 LIMIT 1
		   FOR UPDATE
	`, hash[:]).Scan(&id, &userID, &expiresAt, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrWSTicketInvalid
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("auth: ws ticket lookup: %w", err)
	}
	if consumed != nil {
		return uuid.Nil, ErrWSTicketInvalid
	}
	if time.Now().UTC().After(expiresAt) {
		return uuid.Nil, ErrWSTicketInvalid
	}

	if _, err := tx.Exec(ctx, `UPDATE ws_tickets SET consumed_at = now() WHERE id = $1`, id); err != nil {
		return uuid.Nil, fmt.Errorf("auth: ws ticket consume: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("auth: ws ticket commit: %w", err)
	}
	return userID, nil
}
