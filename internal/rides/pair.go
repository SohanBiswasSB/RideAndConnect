package rides

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Pair declares the caller (pillion) is on rider's bike. Both must be
// active members of the same ride; neither can already be in another
// active pair on this ride.
//
// Returns the freshly inserted Pair. Resolvers map sentinel errors to
// docs/API.md §1.3 codes.
func (s *Service) Pair(ctx context.Context, rideID, callerID, riderID uuid.UUID) (*Pair, error) {
	if callerID == riderID {
		return nil, ErrPairSameUser
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("rides: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the ride to serialize pair changes alongside membership ones.
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM rides WHERE id = $1 FOR UPDATE`, rideID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("rides: lock: %w", err)
	}
	if status != string(StatusActive) {
		return nil, ErrEnded
	}

	// Both parties must be active members.
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM ride_members
		 WHERE ride_id = $1 AND user_id = ANY($2) AND left_at IS NULL
	`, rideID, []uuid.UUID{callerID, riderID}).Scan(&n); err != nil {
		return nil, fmt.Errorf("rides: pair member check: %w", err)
	}
	if n != 2 {
		return nil, ErrNotMember
	}

	var p Pair
	err = tx.QueryRow(ctx, `
		INSERT INTO ride_pairs (ride_id, rider_user_id, pillion_user_id)
		VALUES ($1, $2, $3)
		RETURNING id, ride_id, rider_user_id, pillion_user_id, paired_at, detached_at
	`, rideID, riderID, callerID).Scan(
		&p.ID, &p.RideID, &p.RiderUserID, &p.PillionUserID, &p.PairedAt, &p.DetachedAt,
	)
	if err != nil {
		// Translate the partial-unique-index violations into specific
		// user-facing sentinels.
		if isUniqueViolation(err, "ride_pairs_active_rider_uidx") {
			return nil, ErrPairExists
		}
		if isUniqueViolation(err, "ride_pairs_active_pillion_uidx") {
			return nil, ErrPillionPaired
		}
		return nil, fmt.Errorf("rides: insert pair: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rides: commit: %w", err)
	}
	return &p, nil
}

// Detach ends the caller's active pair. The caller may be either the
// rider OR the pillion in the pair (the PRD describes the pillion-tap UX,
// but allowing the rider to end the pair is harmless and useful).
//
// Returns ErrNotPaired when the caller has no active pair on this ride.
func (s *Service) Detach(ctx context.Context, rideID, callerID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE ride_pairs
		   SET detached_at = now(), detached_by = $2
		 WHERE ride_id = $1
		   AND (rider_user_id = $2 OR pillion_user_id = $2)
		   AND detached_at IS NULL
	`, rideID, callerID)
	if err != nil {
		return fmt.Errorf("rides: detach: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotPaired
	}
	return nil
}

// IsPillionOf reports whether `userID` is currently the pillion in an
// active pair on the given ride; if so, returns the rider's UUID. This
// is the read used by the routes service to short-circuit peerRoute
// calls between paired riders and to substitute the rider's location for
// the pillion.
func (s *Service) IsPillionOf(ctx context.Context, rideID, userID uuid.UUID) (uuid.UUID, bool, error) {
	var rider uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT rider_user_id FROM ride_pairs
		 WHERE ride_id = $1 AND pillion_user_id = $2 AND detached_at IS NULL
		 LIMIT 1
	`, rideID, userID).Scan(&rider)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("rides: pair lookup: %w", err)
	}
	return rider, true, nil
}

// IsRiderWithPillion is the symmetric lookup: is `userID` the RIDER in
// an active pair, and if so who is the pillion? Used by the WS upgrade
// path so the rider's location.broadcast carries the pillion's user_id.
func (s *Service) IsRiderWithPillion(ctx context.Context, rideID, userID uuid.UUID) (uuid.UUID, bool, error) {
	var pillion uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT pillion_user_id FROM ride_pairs
		 WHERE ride_id = $1 AND rider_user_id = $2 AND detached_at IS NULL
		 LIMIT 1
	`, rideID, userID).Scan(&pillion)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("rides: pair lookup: %w", err)
	}
	return pillion, true, nil
}

// IsActiveMember is a thin DB read used by the WS upgrade handler.
func (s *Service) IsActiveMember(ctx context.Context, rideID, userID uuid.UUID) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM ride_members
		 WHERE ride_id = $1 AND user_id = $2 AND left_at IS NULL
	`, rideID, userID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("rides: is active member: %w", err)
	}
	return n > 0, nil
}

// ArePaired reports whether the two users are in an active pair together
// on the ride (either direction).
func (s *Service) ArePaired(ctx context.Context, rideID, a, b uuid.UUID) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM ride_pairs
		 WHERE ride_id = $1 AND detached_at IS NULL
		   AND ((rider_user_id = $2 AND pillion_user_id = $3)
		        OR (rider_user_id = $3 AND pillion_user_id = $2))
	`, rideID, a, b).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("rides: pair pair check: %w", err)
	}
	return n > 0, nil
}
