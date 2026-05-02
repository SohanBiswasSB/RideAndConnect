package rides

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// InvitationStatus mirrors the SQL enum.
type InvitationStatus string

const (
	InviteStatusPending  InvitationStatus = "PENDING"
	InviteStatusAccepted InvitationStatus = "ACCEPTED"
	InviteStatusDeclined InvitationStatus = "DECLINED"
	InviteStatusRevoked  InvitationStatus = "REVOKED"
	InviteStatusExpired  InvitationStatus = "EXPIRED"
)

// Invitation surfaces ride_invitations rows.
type Invitation struct {
	ID             uuid.UUID
	RideID         uuid.UUID
	InvitedUserID  uuid.UUID
	InvitedBy      uuid.UUID
	IdentifierUsed string
	Status         InvitationStatus
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RespondedAt    *time.Time
}

// Sentinel errors for invitations.
var (
	ErrInviteNotFound   = errors.New("rides: invitation not found")
	ErrInviteNotPending = errors.New("rides: invitation no longer pending")
	ErrInviteExpired    = errors.New("rides: invitation expired")
	ErrInviteWrongUser  = errors.New("rides: invitation addressed to another user")
)

// defaultInvitationTTL is how long a pending push-invite stays valid.
const defaultInvitationTTL = 7 * 24 * time.Hour

// Invite creates one or more invitations for the given identifier-resolved
// users. Caller (must already be a member of the ride) inserts a row per
// matched user. Re-inviting a user with an existing PENDING invite is a
// no-op (the existing row is returned).
func (s *Service) Invite(ctx context.Context, rideID, inviterID uuid.UUID, identifier string, matchedUserIDs []uuid.UUID) ([]Invitation, error) {
	if len(matchedUserIDs) == 0 {
		return nil, fmt.Errorf("%w: identifier did not match any user", ErrNotFound)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("rides: begin invite: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := ensureMember(ctx, tx, rideID, inviterID); err != nil {
		return nil, err
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM rides WHERE id = $1`, rideID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("rides: ride status: %w", err)
	}
	if status != string(StatusActive) {
		return nil, ErrEnded
	}

	out := make([]Invitation, 0, len(matchedUserIDs))
	expires := time.Now().UTC().Add(defaultInvitationTTL)
	for _, uid := range matchedUserIDs {
		if uid == inviterID {
			// Inviting yourself is silently skipped.
			continue
		}
		// Skip users already active in the ride.
		var existing int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM ride_members
			 WHERE ride_id = $1 AND user_id = $2 AND left_at IS NULL
		`, rideID, uid).Scan(&existing); err != nil {
			return nil, fmt.Errorf("rides: invite member check: %w", err)
		}
		if existing > 0 {
			continue
		}

		var inv Invitation
		err := tx.QueryRow(ctx, `
			INSERT INTO ride_invitations (ride_id, invited_user_id, invited_by, identifier_used, expires_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (ride_id, invited_user_id) WHERE status = 'PENDING'
			DO UPDATE SET expires_at = EXCLUDED.expires_at, identifier_used = EXCLUDED.identifier_used
			RETURNING id, ride_id, invited_user_id, invited_by, identifier_used, status::text, created_at, expires_at, responded_at
		`, rideID, uid, inviterID, identifier, expires).Scan(
			&inv.ID, &inv.RideID, &inv.InvitedUserID, &inv.InvitedBy, &inv.IdentifierUsed,
			(*string)(&inv.Status), &inv.CreatedAt, &inv.ExpiresAt, &inv.RespondedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("rides: insert invitation: %w", err)
		}
		out = append(out, inv)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rides: commit invite: %w", err)
	}
	return out, nil
}

// AcceptInvitation transitions a PENDING row to ACCEPTED and admits the
// caller as a regular member. Enforces the 3-cap, ride status, and that
// the caller is the addressee.
func (s *Service) AcceptInvitation(ctx context.Context, invitationID, callerID uuid.UUID) (*Ride, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("rides: begin accept: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		rideID    uuid.UUID
		invited   uuid.UUID
		status    string
		expires   time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT ride_id, invited_user_id, status::text, expires_at
		  FROM ride_invitations WHERE id = $1 FOR UPDATE
	`, invitationID).Scan(&rideID, &invited, &status, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInviteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rides: read invitation: %w", err)
	}
	if invited != callerID {
		return nil, ErrInviteWrongUser
	}
	if status != string(InviteStatusPending) {
		return nil, ErrInviteNotPending
	}
	if time.Now().UTC().After(expires) {
		// Lazily mark expired so subsequent reads see the truth.
		_, _ = tx.Exec(ctx, `UPDATE ride_invitations SET status = 'EXPIRED', responded_at = now() WHERE id = $1`, invitationID)
		_ = tx.Commit(ctx)
		return nil, ErrInviteExpired
	}

	// Lock ride + check cap (mirrors Join).
	var rideStatus string
	var memberCount int
	err = tx.QueryRow(ctx, `
		SELECT status::text,
		       (SELECT count(*) FROM ride_members WHERE ride_id = rides.id AND left_at IS NULL)
		  FROM rides WHERE id = $1 FOR UPDATE
	`, rideID).Scan(&rideStatus, &memberCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rides: lock ride: %w", err)
	}
	if rideStatus != string(StatusActive) {
		return nil, ErrEnded
	}
	if memberCount >= MaxMembers {
		return nil, ErrRideFull
	}

	// Already-member check (e.g. user joined via invite code in parallel).
	var existing int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM ride_members
		 WHERE ride_id = $1 AND user_id = $2 AND left_at IS NULL
	`, rideID, callerID).Scan(&existing); err != nil {
		return nil, fmt.Errorf("rides: existing member check: %w", err)
	}
	if existing == 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ride_members (ride_id, user_id, role) VALUES ($1, $2, 'member')
		`, rideID, callerID); err != nil {
			return nil, fmt.Errorf("rides: insert member: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE ride_invitations SET status = 'ACCEPTED', responded_at = now() WHERE id = $1
	`, invitationID); err != nil {
		return nil, fmt.Errorf("rides: mark accepted: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rides: commit accept: %w", err)
	}
	return s.Get(ctx, rideID, callerID)
}

// DeclineInvitation transitions a PENDING row to DECLINED. Only the
// addressee may decline.
func (s *Service) DeclineInvitation(ctx context.Context, invitationID, callerID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE ride_invitations
		   SET status = 'DECLINED', responded_at = now()
		 WHERE id = $1 AND invited_user_id = $2 AND status = 'PENDING'
	`, invitationID, callerID)
	if err != nil {
		return fmt.Errorf("rides: decline: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInviteNotPending
	}
	return nil
}

// RevokeInvitation transitions a PENDING row to REVOKED. Only the inviter
// (or the ride owner, who is typically the same person) may revoke.
func (s *Service) RevokeInvitation(ctx context.Context, invitationID, callerID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE ride_invitations
		   SET status = 'REVOKED', responded_at = now()
		 WHERE id = $1 AND status = 'PENDING'
		   AND (invited_by = $2
		        OR EXISTS (SELECT 1 FROM rides r WHERE r.id = ride_invitations.ride_id AND r.owner_user_id = $2))
	`, invitationID, callerID)
	if err != nil {
		return fmt.Errorf("rides: revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInviteNotPending
	}
	return nil
}

// MyInvitations returns this user's PENDING (and recently-resolved)
// invitations addressed to them, newest first.
func (s *Service) MyInvitations(ctx context.Context, userID uuid.UUID) ([]Invitation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, ride_id, invited_user_id, invited_by, identifier_used, status::text, created_at, expires_at, responded_at
		  FROM ride_invitations
		 WHERE invited_user_id = $1
		 ORDER BY created_at DESC
		 LIMIT 50
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("rides: list invitations: %w", err)
	}
	defer rows.Close()
	var out []Invitation
	for rows.Next() {
		var inv Invitation
		if err := rows.Scan(
			&inv.ID, &inv.RideID, &inv.InvitedUserID, &inv.InvitedBy, &inv.IdentifierUsed,
			(*string)(&inv.Status), &inv.CreatedAt, &inv.ExpiresAt, &inv.RespondedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}
