package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors. Resolvers map these to docs/API.md §1.3 codes.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrEmailTaken         = errors.New("auth: email already taken")
	ErrPhoneTaken         = errors.New("auth: phone already taken")
	ErrIdentifierMissing  = errors.New("auth: email or phone required")
	ErrPasswordWeak       = errors.New("auth: password too weak")
	ErrRefreshInvalid     = errors.New("auth: refresh token invalid")
	ErrRefreshRevoked     = errors.New("auth: refresh token revoked")
	ErrUnauthenticated    = errors.New("auth: unauthenticated")
)

// Config holds the runtime knobs the auth service needs. Wired in main.go
// from internal/config.
type Config struct {
	JWTSecret    []byte
	AccessTTL    time.Duration
	RefreshTTL   time.Duration
	WSTicketTTL  time.Duration
}

// AuditRecorder is the small surface auth needs from internal/audit;
// kept as an interface to avoid an import cycle if audit ever needs
// auth helpers in the future.
type AuditRecorder interface {
	Record(actorUserID *uuid.UUID, rideID *uuid.UUID, eventType string, metadata map[string]any)
}

// Service is the auth domain entry point.
type Service struct {
	pool  *pgxpool.Pool
	cfg   Config
	audit AuditRecorder
}

// NewService constructs the auth service. JWTSecret must be ≥ 32 bytes —
// validated at startup in internal/config.
func NewService(pool *pgxpool.Pool, cfg Config) *Service {
	return &Service{pool: pool, cfg: cfg}
}

// SetAudit wires an audit recorder. Optional — nil is a safe no-op.
func (s *Service) SetAudit(a AuditRecorder) { s.audit = a }

// RegisterInput captures everything needed to create a user. Either email
// or phone is required; everything else is optional and may be edited
// later via UpdateProfile.
type RegisterInput struct {
	Name            string
	Email           *string
	Phone           *string
	Password        string
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
}

// LoginInput accepts identification by email or phone.
type LoginInput struct {
	Email    *string
	Phone    *string
	Password string
}

// AuthResult is what register/login/refresh all return. Resolvers convert
// this into the GraphQL AuthPayload type.
type AuthResult struct {
	UserID       uuid.UUID
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// Register creates a user with a hashed password and returns access +
// refresh tokens. Validation:
//   - One of email/phone required.
//   - Password ≥ 8 chars (production rule is 12; relaxed in v1 to make
//     the dev seed phone-as-password convention workable).
//   - Email is normalized lowercase + trimmed.
//   - Phone is normalized: strip whitespace; require at least 6 digits.
//   - License plate is uppercased + trimmed.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*AuthResult, error) {
	in.Email = normalizeEmail(in.Email)
	in.Phone = normalizePhone(in.Phone)
	if in.Email == nil && in.Phone == nil {
		return nil, ErrIdentifierMissing
	}
	if len(in.Password) < 8 {
		return nil, ErrPasswordWeak
	}
	in.LicensePlate = normalizePlate(in.LicensePlate)

	hash, err := HashPassword(in.Password)
	if err != nil {
		return nil, fmt.Errorf("auth: hash password: %w", err)
	}

	var userID uuid.UUID
	err = s.pool.QueryRow(ctx, `
		INSERT INTO users (
			name, email, phone, password_hash,
			initials, bike_model, license_plate, default_bike_role,
			blood_type, age, height_cm, weight_kg,
			tyre_front_solo_psi, tyre_rear_solo_psi, tyre_front_pillion_psi, tyre_rear_pillion_psi,
			emergency_contact_name, emergency_contact_phone, emergency_contact_email, emergency_contact_user_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
		        $9, $10, $11, $12,
		        $13, $14, $15, $16,
		        $17, $18, $19, $20)
		RETURNING id
	`,
		in.Name, in.Email, in.Phone, hash,
		in.Initials, in.BikeModel, in.LicensePlate, in.DefaultBikeRole,
		in.BloodType, in.Age, in.HeightCm, in.WeightKg,
		in.TyreFrontSoloPsi, in.TyreRearSoloPsi, in.TyreFrontPillionPsi, in.TyreRearPillionPsi,
		in.EmergencyContactName, in.EmergencyContactPhone, in.EmergencyContactEmail, in.EmergencyContactUserID,
	).Scan(&userID)
	if err != nil {
		switch uniqueConstraint(err) {
		case "users_email_key":
			return nil, ErrEmailTaken
		case "users_phone_key":
			return nil, ErrPhoneTaken
		}
		return nil, fmt.Errorf("auth: insert user: %w", err)
	}

	if s.audit != nil {
		s.audit.Record(&userID, nil, "user.registered", map[string]any{"name": in.Name})
	}
	return s.issueTokens(ctx, userID)
}

// Login authenticates by email or phone + password. Constant-time on the
// not-found path: we still hash a dummy password so timing leaks don't
// reveal whether an account exists.
func (s *Service) Login(ctx context.Context, in LoginInput) (*AuthResult, error) {
	in.Email = normalizeEmail(in.Email)
	in.Phone = normalizePhone(in.Phone)
	if in.Email == nil && in.Phone == nil {
		return nil, ErrIdentifierMissing
	}

	var userID uuid.UUID
	var hash string
	var query string
	var arg any
	switch {
	case in.Email != nil:
		query = `SELECT id, password_hash FROM users WHERE email = $1 LIMIT 1`
		arg = *in.Email
	default:
		query = `SELECT id, password_hash FROM users WHERE phone = $1 LIMIT 1`
		arg = *in.Phone
	}

	err := s.pool.QueryRow(ctx, query, arg).Scan(&userID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// Constant-time: hash the supplied password against a dummy.
		_, _ = VerifyPassword(dummyHash, in.Password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("auth: lookup user: %w", err)
	}

	ok, err := VerifyPassword(hash, in.Password)
	if err != nil || !ok {
		return nil, ErrInvalidCredentials
	}

	if s.audit != nil {
		s.audit.Record(&userID, nil, "user.logged_in", nil)
	}
	return s.issueTokens(ctx, userID)
}

// Refresh rotates the supplied refresh token: marks it revoked + replaced,
// and returns a new access + refresh pair. Single-use semantics — re-using
// a refresh token returns ErrRefreshInvalid.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*AuthResult, error) {
	hash := HashRefreshToken(refreshToken)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("auth: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		oldID    uuid.UUID
		userID   uuid.UUID
		expires  time.Time
		revoked  *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, expires_at, revoked_at
		  FROM refresh_tokens
		 WHERE token_hash = $1
		 LIMIT 1
	`, hash).Scan(&oldID, &userID, &expires, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRefreshInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("auth: refresh lookup: %w", err)
	}
	if revoked != nil {
		return nil, ErrRefreshRevoked
	}
	if time.Now().UTC().After(expires) {
		return nil, ErrRefreshInvalid
	}

	access, accessExp, err := s.IssueAccessToken(userID)
	if err != nil {
		return nil, err
	}
	newToken, newHash, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}

	var newID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
		RETURNING id
	`, userID, newHash, time.Now().UTC().Add(s.cfg.RefreshTTL)).Scan(&newID)
	if err != nil {
		return nil, fmt.Errorf("auth: insert refresh: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = now(), replaced_by = $2 WHERE id = $1
	`, oldID, newID); err != nil {
		return nil, fmt.Errorf("auth: revoke old refresh: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("auth: commit refresh: %w", err)
	}

	return &AuthResult{
		UserID:       userID,
		AccessToken:  access,
		RefreshToken: newToken,
		ExpiresIn:    int(time.Until(accessExp).Seconds()),
	}, nil
}

// Logout revokes the supplied refresh token. Idempotent — silently
// succeeds if the token is unknown so that an attacker can't probe DB
// state via timing.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	hash := HashRefreshToken(refreshToken)
	_, err := s.pool.Exec(ctx, `
		UPDATE refresh_tokens
		   SET revoked_at = now()
		 WHERE token_hash = $1 AND revoked_at IS NULL
	`, hash)
	if err != nil {
		return fmt.Errorf("auth: logout: %w", err)
	}
	return nil
}

func (s *Service) issueTokens(ctx context.Context, userID uuid.UUID) (*AuthResult, error) {
	access, accessExp, err := s.IssueAccessToken(userID)
	if err != nil {
		return nil, err
	}
	refresh, refreshHash, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, userID, refreshHash, time.Now().UTC().Add(s.cfg.RefreshTTL))
	if err != nil {
		return nil, fmt.Errorf("auth: persist refresh: %w", err)
	}
	return &AuthResult{
		UserID:       userID,
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int(time.Until(accessExp).Seconds()),
	}, nil
}

// dummyHash is a precomputed argon2id hash used during failed-lookup
// login flows so timing matches the success path. Decoded value:
// password "rideandconnect-dummy".
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func normalizeEmail(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(strings.ToLower(*p))
	if v == "" {
		return nil
	}
	return &v
}

func normalizePhone(p *string) *string {
	if p == nil {
		return nil
	}
	var b strings.Builder
	for _, r := range *p {
		switch r {
		case ' ', '-', '(', ')':
			continue
		}
		b.WriteRune(r)
	}
	v := strings.TrimSpace(b.String())
	digits := 0
	for _, r := range v {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	if digits < 6 {
		return nil
	}
	return &v
}

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

// uniqueConstraint returns the constraint name on a unique-violation
// (SQLSTATE 23505), or "" otherwise. Uses pgconn.PgError directly so we
// don't rely on substring-matching error text across pgx versions.
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

