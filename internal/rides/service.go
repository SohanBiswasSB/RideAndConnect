package rides

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditRecorder is the small surface rides needs from internal/audit;
// kept as an interface to avoid an import cycle.
type AuditRecorder interface {
	Record(actorUserID *uuid.UUID, rideID *uuid.UUID, eventType string, metadata map[string]any)
}

// Service is the rides domain entry point.
type Service struct {
	pool   *pgxpool.Pool
	engine *RouteEngine
	audit  AuditRecorder
}

// NewService constructs a Service.
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// SetAudit wires an audit recorder. Optional — nil is a safe no-op.
func (s *Service) SetAudit(a AuditRecorder) { s.audit = a }

// Create starts a new ride owned by the caller. Optional destination is
// set atomically with the ride row. Returns the fully hydrated Ride.
func (s *Service) Create(ctx context.Context, ownerID uuid.UUID, dest *DestinationInput) (*Ride, error) {
	if err := validateDestination(dest); err != nil {
		return nil, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("rides: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rideID, err := s.insertRideWithFreshCode(ctx, tx, ownerID, dest)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO ride_members (ride_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, rideID, ownerID); err != nil {
		return nil, fmt.Errorf("rides: insert owner member: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rides: commit: %w", err)
	}

	if s.audit != nil {
		s.audit.Record(&ownerID, &rideID, "ride.created", nil)
	}
	return s.Get(ctx, rideID, ownerID)
}

// insertRideWithFreshCode retries a small number of times on the rare
// invite-code collision. The partial unique index returns 23505 only for
// active rides, so collisions become possible only when many rides are
// alive simultaneously.
func (s *Service) insertRideWithFreshCode(ctx context.Context, tx pgx.Tx, ownerID uuid.UUID, dest *DestinationInput) (uuid.UUID, error) {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := NewInviteCode()
		if err != nil {
			return uuid.Nil, err
		}
		var (
			rideID    uuid.UUID
			destName  *string
			destPlace *string
			destLat   *float64
			destLng   *float64
		)
		if dest != nil {
			n := dest.Name
			destName = &n
			destPlace = dest.PlaceID
			la := dest.Latitude
			lo := dest.Longitude
			destLat = &la
			destLng = &lo
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO rides (owner_user_id, invite_code, status, destination_name, destination_place_id, destination_latitude, destination_longitude)
			VALUES ($1, $2, 'active', $3, $4, $5, $6)
			RETURNING id
		`, ownerID, code, destName, destPlace, destLat, destLng).Scan(&rideID)
		if err == nil {
			return rideID, nil
		}
		if uniqueConstraint(err) != "rides_invite_code_active_uidx" {
			return uuid.Nil, fmt.Errorf("rides: insert ride: %w", err)
		}
	}
	return uuid.Nil, fmt.Errorf("rides: invite code collision after retries")
}

// Get returns the full Ride aggregate. Caller must be a member, otherwise
// ErrNotMember (note: still returns ErrNotFound for nonexistent rides to
// avoid leaking existence to non-members).
func (s *Service) Get(ctx context.Context, rideID, callerID uuid.UUID) (*Ride, error) {
	r, err := s.fetchRide(ctx, rideID)
	if err != nil {
		return nil, err
	}
	members, err := s.fetchActiveMembers(ctx, rideID)
	if err != nil {
		return nil, err
	}
	if !memberContains(members, callerID) && !endedMemberContains(ctx, s.pool, rideID, callerID) {
		return nil, ErrNotFound
	}
	pairs, err := s.fetchActivePairs(ctx, rideID)
	if err != nil {
		return nil, err
	}
	r.Members = members
	r.ActivePairs = pairs
	return r, nil
}

// Join admits a user to a ride by invite code. Enforces the 3-member cap
// transactionally. Returns the freshly hydrated Ride.
func (s *Service) Join(ctx context.Context, rideID uuid.UUID, callerID uuid.UUID, inviteCode string) (*Ride, error) {
	inviteCode = strings.ToUpper(strings.TrimSpace(inviteCode))
	if err := ValidateInviteCode(inviteCode); err != nil {
		return nil, ErrInviteInvalid
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("rides: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the ride row, validate status + invite code in one go.
	var (
		dbInvite string
		status   string
		count    int
	)
	err = tx.QueryRow(ctx, `
		SELECT invite_code, status::text,
		       (SELECT count(*) FROM ride_members WHERE ride_id = rides.id AND left_at IS NULL)
		  FROM rides
		 WHERE id = $1
		 FOR UPDATE
	`, rideID).Scan(&dbInvite, &status, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rides: lock ride: %w", err)
	}
	if status != string(StatusActive) {
		return nil, ErrInviteInvalid
	}
	if !strings.EqualFold(dbInvite, inviteCode) {
		return nil, ErrInviteInvalid
	}
	if count >= MaxMembers {
		return nil, ErrRideFull
	}

	// Already-member check.
	var existing int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM ride_members
		 WHERE ride_id = $1 AND user_id = $2 AND left_at IS NULL
	`, rideID, callerID).Scan(&existing); err != nil {
		return nil, fmt.Errorf("rides: existing member check: %w", err)
	}
	if existing > 0 {
		return nil, ErrAlreadyMember
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO ride_members (ride_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, rideID, callerID); err != nil {
		return nil, fmt.Errorf("rides: insert member: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rides: commit: %w", err)
	}
	if s.audit != nil {
		s.audit.Record(&callerID, &rideID, "ride.member_joined", nil)
	}
	return s.Get(ctx, rideID, callerID)
}

// Leave removes the caller from the ride. Owner-leave triggers ownership
// transfer to the earliest joiner; last-member leave auto-ends the ride
// and invalidates the invite code (the partial unique index lets us reuse
// codes only across ended rides).
func (s *Service) Leave(ctx context.Context, rideID, callerID uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("rides: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT 1 FROM rides WHERE id = $1 FOR UPDATE`, rideID); err != nil {
		return fmt.Errorf("rides: lock: %w", err)
	}

	var (
		role     MemberRole
		ridStatus string
	)
	err = tx.QueryRow(ctx, `
		SELECT m.role::text, r.status::text
		  FROM ride_members m
		  JOIN rides r ON r.id = m.ride_id
		 WHERE m.ride_id = $1 AND m.user_id = $2 AND m.left_at IS NULL
		 LIMIT 1
	`, rideID, callerID).Scan(&role, &ridStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotMember
	}
	if err != nil {
		return fmt.Errorf("rides: read membership: %w", err)
	}
	if ridStatus != string(StatusActive) {
		return ErrEnded
	}

	if _, err := tx.Exec(ctx, `
		UPDATE ride_members SET left_at = now()
		 WHERE ride_id = $1 AND user_id = $2 AND left_at IS NULL
	`, rideID, callerID); err != nil {
		return fmt.Errorf("rides: mark left: %w", err)
	}

	// Detach the caller from any active pair so the partial unique
	// indexes don't block subsequent rejoin/pair operations.
	if _, err := tx.Exec(ctx, `
		UPDATE ride_pairs
		   SET detached_at = now(), detached_by = $2
		 WHERE ride_id = $1
		   AND (rider_user_id = $2 OR pillion_user_id = $2)
		   AND detached_at IS NULL
	`, rideID, callerID); err != nil {
		return fmt.Errorf("rides: auto-detach: %w", err)
	}

	// Count remaining active members.
	var remaining int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM ride_members WHERE ride_id = $1 AND left_at IS NULL
	`, rideID).Scan(&remaining); err != nil {
		return fmt.Errorf("rides: count remaining: %w", err)
	}

	if remaining == 0 {
		// Last member left → auto-end.
		if _, err := tx.Exec(ctx, `
			UPDATE rides SET status = 'ended', ended_at = now() WHERE id = $1
		`, rideID); err != nil {
			return fmt.Errorf("rides: auto-end: %w", err)
		}
	} else if role == RoleOwner {
		// Transfer ownership to earliest joiner among remaining members.
		if _, err := tx.Exec(ctx, `
			UPDATE ride_members SET role = 'owner'
			 WHERE id = (
				SELECT id FROM ride_members
				 WHERE ride_id = $1 AND left_at IS NULL
				 ORDER BY joined_at ASC, id ASC
				 LIMIT 1
			 )
		`, rideID); err != nil {
			return fmt.Errorf("rides: transfer ownership: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// End transitions a ride to ended (owner-only). Closes any active pairs
// and marks all remaining active memberships as left. The invite code
// becomes unaddressable (partial unique index releases it).
func (s *Service) End(ctx context.Context, rideID, callerID uuid.UUID) (*Ride, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("rides: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		ownerID  uuid.UUID
		status   string
	)
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id, status::text FROM rides WHERE id = $1 FOR UPDATE
	`, rideID).Scan(&ownerID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rides: lock: %w", err)
	}
	if ownerID != callerID {
		return nil, ErrNotOwner
	}
	if status != string(StatusActive) {
		return nil, ErrEnded
	}

	if _, err := tx.Exec(ctx, `
		UPDATE rides SET status = 'ended', ended_at = now() WHERE id = $1
	`, rideID); err != nil {
		return nil, fmt.Errorf("rides: end: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ride_members SET left_at = now()
		 WHERE ride_id = $1 AND left_at IS NULL
	`, rideID); err != nil {
		return nil, fmt.Errorf("rides: close memberships: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ride_pairs SET detached_at = now(), detached_by = $2
		 WHERE ride_id = $1 AND detached_at IS NULL
	`, rideID, callerID); err != nil {
		return nil, fmt.Errorf("rides: close pairs: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rides: commit: %w", err)
	}
	if s.audit != nil {
		s.audit.Record(&callerID, &rideID, "ride.ended", nil)
	}
	return s.Get(ctx, rideID, callerID)
}

// SetDestination updates the shared destination. Member-only. Server
// emits destination.updated on the WS (Phase 7) and route cache for this
// ride is invalidated (Phase 6).
func (s *Service) SetDestination(ctx context.Context, rideID, callerID uuid.UUID, in DestinationInput) (*Destination, error) {
	if err := validateDestination(&in); err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("rides: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := ensureMember(ctx, tx, rideID, callerID); err != nil {
		return nil, err
	}

	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM rides WHERE id = $1 FOR UPDATE`, rideID).Scan(&status); err != nil {
		return nil, fmt.Errorf("rides: lock: %w", err)
	}
	if status != string(StatusActive) {
		return nil, ErrEnded
	}

	var updated time.Time
	err = tx.QueryRow(ctx, `
		UPDATE rides
		   SET destination_name = $2,
		       destination_place_id = $3,
		       destination_latitude = $4,
		       destination_longitude = $5,
		       updated_at = now()
		 WHERE id = $1
		 RETURNING updated_at
	`, rideID, in.Name, in.PlaceID, in.Latitude, in.Longitude).Scan(&updated)
	if err != nil {
		return nil, fmt.Errorf("rides: update destination: %w", err)
	}

	// Invalidate cached destination routes for this ride.
	if _, err := tx.Exec(ctx, `
		DELETE FROM ride_routes WHERE ride_id = $1 AND route_kind = 'DESTINATION'
	`, rideID); err != nil {
		return nil, fmt.Errorf("rides: clear route cache: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rides: commit: %w", err)
	}
	return &Destination{
		Name:      in.Name,
		PlaceID:   in.PlaceID,
		Latitude:  in.Latitude,
		Longitude: in.Longitude,
		UpdatedAt: updated,
	}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────

func (s *Service) fetchRide(ctx context.Context, rideID uuid.UUID) (*Ride, error) {
	var (
		r        Ride
		destName *string
		destPID  *string
		destLat  *float64
		destLng  *float64
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, invite_code, status::text,
		       destination_name, destination_place_id, destination_latitude, destination_longitude,
		       started_at, ended_at, created_at, updated_at
		  FROM rides
		 WHERE id = $1
	`, rideID).Scan(
		&r.ID, &r.OwnerUserID, &r.InviteCode, (*string)(&r.Status),
		&destName, &destPID, &destLat, &destLng,
		&r.StartedAt, &r.EndedAt, &r.CreatedAt, new(time.Time),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rides: fetch: %w", err)
	}
	if destName != nil && destLat != nil && destLng != nil {
		r.Destination = &Destination{
			Name:      *destName,
			PlaceID:   destPID,
			Latitude:  *destLat,
			Longitude: *destLng,
		}
	}
	return &r, nil
}

func (s *Service) fetchActiveMembers(ctx context.Context, rideID uuid.UUID) ([]Member, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.user_id, u.name, u.initials, m.role::text, m.joined_at, m.left_at
		  FROM ride_members m
		  JOIN users u ON u.id = m.user_id
		 WHERE m.ride_id = $1 AND m.left_at IS NULL
		 ORDER BY m.joined_at ASC
	`, rideID)
	if err != nil {
		return nil, fmt.Errorf("rides: fetch members: %w", err)
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Name, &m.Initials, &m.Role, &m.JoinedAt, &m.LeftAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) fetchActivePairs(ctx context.Context, rideID uuid.UUID) ([]Pair, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, ride_id, rider_user_id, pillion_user_id, paired_at, detached_at
		  FROM ride_pairs
		 WHERE ride_id = $1 AND detached_at IS NULL
		 ORDER BY paired_at ASC
	`, rideID)
	if err != nil {
		return nil, fmt.Errorf("rides: fetch pairs: %w", err)
	}
	defer rows.Close()
	var out []Pair
	for rows.Next() {
		var p Pair
		if err := rows.Scan(&p.ID, &p.RideID, &p.RiderUserID, &p.PillionUserID, &p.PairedAt, &p.DetachedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func ensureMember(ctx context.Context, tx pgx.Tx, rideID, userID uuid.UUID) error {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM ride_members
		 WHERE ride_id = $1 AND user_id = $2 AND left_at IS NULL
	`, rideID, userID).Scan(&n)
	if err != nil {
		return fmt.Errorf("rides: member check: %w", err)
	}
	if n == 0 {
		return ErrNotMember
	}
	return nil
}

func memberContains(ms []Member, id uuid.UUID) bool {
	for _, m := range ms {
		if m.UserID == id {
			return true
		}
	}
	return false
}

// endedMemberContains lets callers see ended rides they were part of.
// We tolerate a non-fatal error here — failing the lookup just means the
// caller will see ErrNotFound, which is the safe default.
func endedMemberContains(ctx context.Context, pool *pgxpool.Pool, rideID, userID uuid.UUID) bool {
	var n int
	_ = pool.QueryRow(ctx, `
		SELECT count(*) FROM ride_members
		 WHERE ride_id = $1 AND user_id = $2
	`, rideID, userID).Scan(&n)
	return n > 0
}

func validateDestination(d *DestinationInput) error {
	if d == nil {
		return nil
	}
	if d.Name == "" || len([]rune(d.Name)) > 120 {
		return fmt.Errorf("%w: destination name", ErrValidation)
	}
	if math.IsNaN(d.Latitude) || math.IsInf(d.Latitude, 0) || d.Latitude < -90 || d.Latitude > 90 {
		return fmt.Errorf("%w: latitude", ErrValidation)
	}
	if math.IsNaN(d.Longitude) || math.IsInf(d.Longitude, 0) || d.Longitude < -180 || d.Longitude > 180 {
		return fmt.Errorf("%w: longitude", ErrValidation)
	}
	return nil
}

// uniqueConstraint returns the constraint name on a unique-violation
// (SQLSTATE 23505), or "" otherwise. See identical helper in
// internal/auth/service.go — kept package-local so each domain owns its
// constraint vocabulary.
func uniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	if pgErr.Code != "23505" {
		return ""
	}
	return pgErr.ConstraintName
}
