// Package rides implements the ride lifecycle: create, get, join, leave,
// end, members, and the rider/pillion pair feature.
//
// Concurrency invariants enforced via SELECT … FOR UPDATE on the parent
// rides row before any membership mutation.
package rides

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// RideStatus mirrors the SQL enum.
type RideStatus string

const (
	StatusActive RideStatus = "active"
	StatusEnded  RideStatus = "ended"
)

// MemberRole mirrors the SQL enum.
type MemberRole string

const (
	RoleOwner  MemberRole = "owner"
	RoleMember MemberRole = "member"
)

// MaxMembers is the hard cap from the PRD.
const MaxMembers = 3

// Destination matches the columns on rides.
type Destination struct {
	Name      string
	PlaceID   *string
	Latitude  float64
	Longitude float64
	UpdatedAt time.Time
}

// Ride is the aggregate returned by service methods.
type Ride struct {
	ID            uuid.UUID
	OwnerUserID   uuid.UUID
	InviteCode    string
	Status        RideStatus
	Destination   *Destination
	StartedAt     time.Time
	EndedAt       *time.Time
	CreatedAt     time.Time
	Members       []Member
	ActivePairs   []Pair
}

// Member is one row in ride_members surfaced for resolvers.
type Member struct {
	UserID    uuid.UUID
	Name      string
	Initials  *string
	Role      MemberRole
	JoinedAt  time.Time
	LeftAt    *time.Time
}

// Pair is one active row in ride_pairs (detached_at IS NULL).
type Pair struct {
	ID            uuid.UUID
	RideID        uuid.UUID
	RiderUserID   uuid.UUID
	PillionUserID uuid.UUID
	PairedAt      time.Time
	DetachedAt    *time.Time
}

// DestinationInput is what setDestination/createRide receive.
type DestinationInput struct {
	Name      string
	PlaceID   *string
	Latitude  float64
	Longitude float64
}

// Sentinel errors.
var (
	ErrNotFound            = errors.New("rides: not found")
	ErrInviteInvalid       = errors.New("rides: invite invalid")
	ErrRideFull            = errors.New("rides: ride full (max 3 members)")
	ErrAlreadyMember       = errors.New("rides: already a member")
	ErrNotMember           = errors.New("rides: not a member")
	ErrNotOwner            = errors.New("rides: not owner")
	ErrEnded               = errors.New("rides: ride already ended")
	ErrValidation          = errors.New("rides: validation failed")
	ErrPairExists          = errors.New("rides: rider already has an active pillion")
	ErrPillionPaired       = errors.New("rides: pillion already in another pair")
	ErrPairSameUser        = errors.New("rides: cannot pair user with themselves")
	ErrNotPaired           = errors.New("rides: not currently paired")
	ErrThrottled           = errors.New("rides: rate limited")
	ErrUpstream            = errors.New("rides: upstream provider unavailable")
	ErrPeerLocationMissing = errors.New("rides: peer location missing")
	ErrPeerLocationStale   = errors.New("rides: peer location stale")
)
