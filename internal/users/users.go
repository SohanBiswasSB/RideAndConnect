// Package users exposes user-profile reads and the small lookup helpers
// used by ride invitations. Writes happen in internal/auth (during
// register) and via UpdateProfile here.
package users

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User mirrors the GraphQL User type minus the password hash.
type User struct {
	ID              uuid.UUID
	Name            string
	Email           *string
	Phone           *string
	Initials        *string
	BikeModel       *string
	LicensePlate    *string
	DefaultBikeRole *string

	BloodType *string
	Age       *int16
	HeightCm  *int16
	WeightKg  *int16

	TyreFrontSoloPsi    *float64
	TyreRearSoloPsi     *float64
	TyreFrontPillionPsi *float64
	TyreRearPillionPsi  *float64

	EmergencyContactName   *string
	EmergencyContactPhone  *string
	EmergencyContactEmail  *string
	EmergencyContactUserID *uuid.UUID

	CreatedAt time.Time
}

// ErrNotFound is returned when no row matches the lookup.
var ErrNotFound = errors.New("users: not found")

// Service is the user-domain entry point.
type Service struct {
	pool *pgxpool.Pool
}

// NewService constructs a Service.
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// ByID returns a User by primary key.
func (s *Service) ByID(ctx context.Context, id uuid.UUID) (*User, error) {
	return s.queryOne(ctx, `WHERE id = $1 LIMIT 1`, id)
}

// NameFor returns just the display name for a user. Used by the WS
// presence broadcast path where a full User load would be wasteful.
func (s *Service) NameFor(ctx context.Context, id uuid.UUID) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// ByPhone returns a User by phone (already normalized by caller).
func (s *Service) ByPhone(ctx context.Context, phone string) (*User, error) {
	return s.queryOne(ctx, `WHERE phone = $1 LIMIT 1`, phone)
}

// ManyByID batch-loads users by ID.
func (s *Service) ManyByID(ctx context.Context, ids []uuid.UUID) ([]*User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, baseSelect+` WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("users: many by id: %w", err)
	}
	defer rows.Close()
	out := make([]*User, 0, len(ids))
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// FindByIdentifier classifies the input string (email / phone / license
// plate) and returns matching users. Email and phone return at most one
// match; plate may return many (rider+pillion sharing one bike).
//
// Heuristic:
//   - contains '@'         → email
//   - has digit-runs ≥ 6   → phone
//   - otherwise            → license plate
func (s *Service) FindByIdentifier(ctx context.Context, identifier string) ([]*User, error) {
	id := strings.TrimSpace(identifier)
	if id == "" {
		return nil, fmt.Errorf("users: empty identifier")
	}
	switch classifyIdentifier(id) {
	case identEmail:
		u, err := s.queryOne(ctx, `WHERE email = $1 LIMIT 1`, strings.ToLower(id))
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return []*User{u}, err
	case identPhone:
		u, err := s.queryOne(ctx, `WHERE phone = $1 LIMIT 1`, normalizePhoneCmp(id))
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return []*User{u}, err
	default: // plate
		rows, err := s.pool.Query(ctx, baseSelect+`
			WHERE license_plate = $1 ORDER BY created_at ASC
		`, strings.ToUpper(id))
		if err != nil {
			return nil, fmt.Errorf("users: plate lookup: %w", err)
		}
		defer rows.Close()
		var out []*User
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, u)
		}
		return out, rows.Err()
	}
}

// UpdateProfile applies a partial update. Only non-nil pointer fields in
// `in` are touched; nil leaves the column unchanged.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, in UpdateProfileInput) (*User, error) {
	// Build a single UPDATE with COALESCE so we don't have to assemble
	// dynamic SQL fragments. NULL inputs preserve existing values.
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET
		    name              = COALESCE($2,  name),
		    initials          = COALESCE($3,  initials),
		    bike_model        = COALESCE($4,  bike_model),
		    license_plate     = COALESCE($5,  license_plate),
		    default_bike_role = COALESCE($6,  default_bike_role),
		    blood_type        = COALESCE($7,  blood_type),
		    age               = COALESCE($8,  age),
		    height_cm         = COALESCE($9,  height_cm),
		    weight_kg         = COALESCE($10, weight_kg),
		    tyre_front_solo_psi    = COALESCE($11, tyre_front_solo_psi),
		    tyre_rear_solo_psi     = COALESCE($12, tyre_rear_solo_psi),
		    tyre_front_pillion_psi = COALESCE($13, tyre_front_pillion_psi),
		    tyre_rear_pillion_psi  = COALESCE($14, tyre_rear_pillion_psi),
		    emergency_contact_name    = COALESCE($15, emergency_contact_name),
		    emergency_contact_phone   = COALESCE($16, emergency_contact_phone),
		    emergency_contact_email   = COALESCE($17, emergency_contact_email),
		    emergency_contact_user_id = COALESCE($18, emergency_contact_user_id)
		WHERE id = $1
	`,
		userID,
		in.Name, in.Initials, in.BikeModel, normalizePlate(in.LicensePlate), in.DefaultBikeRole,
		in.BloodType, in.Age, in.HeightCm, in.WeightKg,
		in.TyreFrontSoloPsi, in.TyreRearSoloPsi, in.TyreFrontPillionPsi, in.TyreRearPillionPsi,
		in.EmergencyContactName, in.EmergencyContactPhone, in.EmergencyContactEmail, in.EmergencyContactUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("users: update profile: %w", err)
	}
	return s.ByID(ctx, userID)
}

// UpdateProfileInput captures partial-update fields. nil = leave alone.
type UpdateProfileInput struct {
	Name             *string
	Initials         *string
	BikeModel        *string
	LicensePlate     *string
	DefaultBikeRole  *string
	BloodType        *string
	Age              *int16
	HeightCm         *int16
	WeightKg         *int16
	TyreFrontSoloPsi    *float64
	TyreRearSoloPsi     *float64
	TyreFrontPillionPsi *float64
	TyreRearPillionPsi  *float64
	EmergencyContactName   *string
	EmergencyContactPhone  *string
	EmergencyContactEmail  *string
	EmergencyContactUserID *uuid.UUID
}

const baseSelect = `
SELECT id, name, email, phone, initials, bike_model, license_plate, default_bike_role,
       blood_type::text,
       age, height_cm, weight_kg,
       tyre_front_solo_psi, tyre_rear_solo_psi, tyre_front_pillion_psi, tyre_rear_pillion_psi,
       emergency_contact_name, emergency_contact_phone, emergency_contact_email, emergency_contact_user_id,
       created_at
FROM users
`

func (s *Service) queryOne(ctx context.Context, where string, args ...any) (*User, error) {
	row := s.pool.QueryRow(ctx, baseSelect+where, args...)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(s scanner) (*User, error) {
	var u User
	if err := s.Scan(
		&u.ID, &u.Name, &u.Email, &u.Phone, &u.Initials, &u.BikeModel, &u.LicensePlate, &u.DefaultBikeRole,
		&u.BloodType,
		&u.Age, &u.HeightCm, &u.WeightKg,
		&u.TyreFrontSoloPsi, &u.TyreRearSoloPsi, &u.TyreFrontPillionPsi, &u.TyreRearPillionPsi,
		&u.EmergencyContactName, &u.EmergencyContactPhone, &u.EmergencyContactEmail, &u.EmergencyContactUserID,
		&u.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &u, nil
}

// ─── identifier classification ─────────────────────────────────────────

type identKind int

const (
	identPlate identKind = iota
	identEmail
	identPhone
)

func classifyIdentifier(s string) identKind {
	if strings.Contains(s, "@") {
		return identEmail
	}
	// Plate vs phone: a phone string contains only digits and
	// phone-formatting punctuation; anything with an alphabetic letter
	// is treated as a license plate.
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == ' ' || r == '+' || r == '-' || r == '(' || r == ')':
			// allowed phone separators
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			return identPlate
		default:
			// Any other char (e.g. punctuation we don't recognize) →
			// treat as plate; the lookup will simply return no matches.
			return identPlate
		}
	}
	if digits >= 6 {
		return identPhone
	}
	return identPlate
}

// normalizePhoneCmp is the canonical form used for phone equality lookups.
// Strips whitespace and common separators; preserves leading '+'.
func normalizePhoneCmp(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case ' ', '-', '(', ')':
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// normalizePlate uppercases + trims; nil pointer stays nil so COALESCE
// in UpdateProfile leaves the column untouched.
func normalizePlate(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.ToUpper(strings.TrimSpace(*p))
	if v == "" {
		return nil
	}
	return &v
}
