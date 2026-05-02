package rides

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EmergencyEvent surfaces emergency_events rows. notified_user_ids is the
// snapshot of active ride membership at trigger time.
type EmergencyEvent struct {
	ID                uuid.UUID
	TriggeredByUserID uuid.UUID
	RideID            *uuid.UUID
	TriggeredAt       time.Time
	Message           *string
	NotifiedUserIDs   []uuid.UUID
	ResolvedAt        *time.Time
	ResolvedByUserID  *uuid.UUID
}

// Sentinel errors for emergencies.
var (
	ErrEmergencyNotFound      = errors.New("rides: emergency event not found")
	ErrEmergencyAlreadyActive = errors.New("rides: an active emergency already exists")
	ErrEmergencyAlreadyClosed = errors.New("rides: emergency already resolved")
	ErrEmergencyNotAuthorized = errors.New("rides: not authorized to resolve this emergency")
)

// TriggerEmergency records an emergency for the caller. If the caller is
// in an active ride, the snapshot of active ride members at this instant
// is stored as notified_user_ids; otherwise the event is recorded with an
// empty snapshot (still useful as a "user is in distress" signal).
//
// Idempotent on a per-user basis: if the caller already has an unresolved
// emergency, that event is returned (we don't open a duplicate).
func (s *Service) TriggerEmergency(ctx context.Context, callerID uuid.UUID, message *string) (*EmergencyEvent, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("rides: begin emergency: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// If an active emergency exists, return it.
	if existing, err := readActiveEmergency(ctx, tx, callerID); err != nil {
		return nil, err
	} else if existing != nil {
		_ = tx.Rollback(ctx)
		return existing, nil
	}

	// Find the caller's active ride (if any) and snapshot members.
	var rideID *uuid.UUID
	var notified []uuid.UUID
	{
		var rid uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT m.ride_id
			  FROM ride_members m
			  JOIN rides r ON r.id = m.ride_id
			 WHERE m.user_id = $1 AND m.left_at IS NULL AND r.status = 'active'
			 LIMIT 1
		`, callerID).Scan(&rid)
		if err == nil {
			rideID = &rid
			rows, err := tx.Query(ctx, `
				SELECT user_id FROM ride_members
				 WHERE ride_id = $1 AND left_at IS NULL AND user_id <> $2
			`, rid, callerID)
			if err != nil {
				return nil, fmt.Errorf("rides: snapshot members: %w", err)
			}
			for rows.Next() {
				var u uuid.UUID
				if err := rows.Scan(&u); err != nil {
					rows.Close()
					return nil, err
				}
				notified = append(notified, u)
			}
			rows.Close()
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("rides: lookup active ride: %w", err)
		}
	}
	if notified == nil {
		notified = []uuid.UUID{}
	}

	var ev EmergencyEvent
	err = tx.QueryRow(ctx, `
		INSERT INTO emergency_events (triggered_by, ride_id, message, notified_user_ids)
		VALUES ($1, $2, $3, $4)
		RETURNING id, triggered_by, ride_id, triggered_at, message, notified_user_ids, resolved_at, resolved_by
	`, callerID, rideID, message, notified).Scan(
		&ev.ID, &ev.TriggeredByUserID, &ev.RideID, &ev.TriggeredAt, &ev.Message,
		&ev.NotifiedUserIDs, &ev.ResolvedAt, &ev.ResolvedByUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("rides: insert emergency: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rides: commit emergency: %w", err)
	}
	return &ev, nil
}

// ResolveEmergency closes an emergency event. The triggering user OR any
// of the originally-notified members may resolve it (so a friend can
// clear it on the user's behalf if they're incapacitated).
func (s *Service) ResolveEmergency(ctx context.Context, eventID, callerID uuid.UUID) (*EmergencyEvent, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("rides: begin resolve: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		triggeredBy uuid.UUID
		notified    []uuid.UUID
		resolvedAt  *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT triggered_by, notified_user_ids, resolved_at
		  FROM emergency_events WHERE id = $1 FOR UPDATE
	`, eventID).Scan(&triggeredBy, &notified, &resolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEmergencyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rides: read emergency: %w", err)
	}
	if resolvedAt != nil {
		return nil, ErrEmergencyAlreadyClosed
	}
	if triggeredBy != callerID && !containsUUID(notified, callerID) {
		return nil, ErrEmergencyNotAuthorized
	}

	if _, err := tx.Exec(ctx, `
		UPDATE emergency_events SET resolved_at = now(), resolved_by = $2 WHERE id = $1
	`, eventID, callerID); err != nil {
		return nil, fmt.Errorf("rides: resolve update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rides: commit resolve: %w", err)
	}
	return s.readEmergency(ctx, eventID)
}

// MyActiveEmergency returns the caller's currently unresolved emergency,
// if any. Useful for client-side polling until the WS push lands.
func (s *Service) MyActiveEmergency(ctx context.Context, callerID uuid.UUID) (*EmergencyEvent, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("rides: acquire: %w", err)
	}
	defer conn.Release()
	return readActiveEmergency(ctx, conn, callerID)
}

// emergencyQuerier is the small surface readActiveEmergency needs from
// either a pgxpool conn or a transaction.
type emergencyQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func readActiveEmergency(ctx context.Context, q emergencyQuerier, userID uuid.UUID) (*EmergencyEvent, error) {
	var ev EmergencyEvent
	err := q.QueryRow(ctx, `
		SELECT id, triggered_by, ride_id, triggered_at, message, notified_user_ids, resolved_at, resolved_by
		  FROM emergency_events
		 WHERE triggered_by = $1 AND resolved_at IS NULL
		 ORDER BY triggered_at DESC
		 LIMIT 1
	`, userID).Scan(
		&ev.ID, &ev.TriggeredByUserID, &ev.RideID, &ev.TriggeredAt, &ev.Message,
		&ev.NotifiedUserIDs, &ev.ResolvedAt, &ev.ResolvedByUserID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rides: read active emergency: %w", err)
	}
	return &ev, nil
}

func (s *Service) readEmergency(ctx context.Context, id uuid.UUID) (*EmergencyEvent, error) {
	var ev EmergencyEvent
	err := s.pool.QueryRow(ctx, `
		SELECT id, triggered_by, ride_id, triggered_at, message, notified_user_ids, resolved_at, resolved_by
		  FROM emergency_events WHERE id = $1
	`, id).Scan(
		&ev.ID, &ev.TriggeredByUserID, &ev.RideID, &ev.TriggeredAt, &ev.Message,
		&ev.NotifiedUserIDs, &ev.ResolvedAt, &ev.ResolvedByUserID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEmergencyNotFound
	}
	return &ev, err
}

func containsUUID(s []uuid.UUID, x uuid.UUID) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}
