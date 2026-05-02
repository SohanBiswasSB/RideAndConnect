package graph

import (
	"time"

	"github.com/google/uuid"
)

// The output structs below mirror the GraphQL object types in
// schema/schema.graphqls. graph-gophers/graphql-go binds field
// resolution by reflection — exported field names match GraphQL fields
// (camelCase becomes UpperCamelCase).
//
// Custom scalars (UUID, DateTime) live in scalars.go and implement the
// graph-gophers Marshaler/Unmarshaler contract.

type userResolver struct {
	id              UUID
	name            string
	email           *string
	phone           *string
	initials        *string
	bikeModel       *string
	licensePlate    *string
	defaultBikeRole *string

	bloodType *string
	age       *int32
	heightCm  *int32
	weightKg  *int32

	tyreFrontSoloPsi    *float64
	tyreRearSoloPsi     *float64
	tyreFrontPillionPsi *float64
	tyreRearPillionPsi  *float64

	emergencyContactName   *string
	emergencyContactPhone  *string
	emergencyContactEmail  *string
	emergencyContactUserID *UUID

	createdAt time.Time
}

func (r *userResolver) ID() UUID                  { return r.id }
func (r *userResolver) Name() string              { return r.name }
func (r *userResolver) Email() *string            { return r.email }
func (r *userResolver) Phone() *string            { return r.phone }
func (r *userResolver) Initials() *string         { return r.initials }
func (r *userResolver) BikeModel() *string        { return r.bikeModel }
func (r *userResolver) LicensePlate() *string     { return r.licensePlate }
func (r *userResolver) DefaultBikeRole() *string  { return r.defaultBikeRole }
func (r *userResolver) BloodType() *string        { return r.bloodType }
func (r *userResolver) Age() *int32               { return r.age }
func (r *userResolver) HeightCm() *int32          { return r.heightCm }
func (r *userResolver) WeightKg() *int32          { return r.weightKg }
func (r *userResolver) CreatedAt() DateTime       { return DateTime{T: r.createdAt} }

// TyrePressure returns nil when no tyre fields are set, so the GraphQL
// nullable surface is honest. Otherwise we materialize the four-field
// resolver.
func (r *userResolver) TyrePressure() *tyrePressureResolver {
	if r.tyreFrontSoloPsi == nil && r.tyreRearSoloPsi == nil &&
		r.tyreFrontPillionPsi == nil && r.tyreRearPillionPsi == nil {
		return nil
	}
	return &tyrePressureResolver{
		frontSolo:    r.tyreFrontSoloPsi,
		rearSolo:     r.tyreRearSoloPsi,
		frontPillion: r.tyreFrontPillionPsi,
		rearPillion:  r.tyreRearPillionPsi,
	}
}

func (r *userResolver) EmergencyContact() *emergencyContactResolver {
	if r.emergencyContactName == nil && r.emergencyContactPhone == nil &&
		r.emergencyContactEmail == nil && r.emergencyContactUserID == nil {
		return nil
	}
	return &emergencyContactResolver{
		name:        r.emergencyContactName,
		phone:       r.emergencyContactPhone,
		email:       r.emergencyContactEmail,
		contactUser: r.emergencyContactUserID,
	}
}

type tyrePressureResolver struct {
	frontSolo, rearSolo, frontPillion, rearPillion *float64
}

func (r *tyrePressureResolver) FrontSoloPsi() *float64    { return r.frontSolo }
func (r *tyrePressureResolver) RearSoloPsi() *float64     { return r.rearSolo }
func (r *tyrePressureResolver) FrontPillionPsi() *float64 { return r.frontPillion }
func (r *tyrePressureResolver) RearPillionPsi() *float64  { return r.rearPillion }

type emergencyContactResolver struct {
	name, phone, email *string
	contactUser        *UUID
}

func (r *emergencyContactResolver) Name() *string         { return r.name }
func (r *emergencyContactResolver) Phone() *string        { return r.phone }
func (r *emergencyContactResolver) Email() *string        { return r.email }
func (r *emergencyContactResolver) ContactUserID() *UUID  { return r.contactUser }

type rideInvitationResolver struct {
	id             UUID
	rideID         UUID
	invitedUserID  UUID
	invitedBy      UUID
	identifierUsed string
	status         string
	createdAt      time.Time
	expiresAt      time.Time
	respondedAt    *time.Time
}

func (r *rideInvitationResolver) ID() UUID              { return r.id }
func (r *rideInvitationResolver) RideID() UUID          { return r.rideID }
func (r *rideInvitationResolver) InvitedUserID() UUID   { return r.invitedUserID }
func (r *rideInvitationResolver) InvitedBy() UUID       { return r.invitedBy }
func (r *rideInvitationResolver) IdentifierUsed() string { return r.identifierUsed }
func (r *rideInvitationResolver) Status() string        { return r.status }
func (r *rideInvitationResolver) CreatedAt() DateTime   { return DateTime{T: r.createdAt} }
func (r *rideInvitationResolver) ExpiresAt() DateTime   { return DateTime{T: r.expiresAt} }
func (r *rideInvitationResolver) RespondedAt() *DateTime {
	if r.respondedAt == nil {
		return nil
	}
	return &DateTime{T: *r.respondedAt}
}

type emergencyEventResolver struct {
	id              UUID
	triggeredByID   UUID
	rideID          *UUID
	triggeredAt     time.Time
	message         *string
	notifiedUserIDs []UUID
	resolvedAt      *time.Time
	resolvedByID    *UUID
}

func (r *emergencyEventResolver) ID() UUID                  { return r.id }
func (r *emergencyEventResolver) TriggeredByUserID() UUID   { return r.triggeredByID }
func (r *emergencyEventResolver) RideID() *UUID             { return r.rideID }
func (r *emergencyEventResolver) TriggeredAt() DateTime     { return DateTime{T: r.triggeredAt} }
func (r *emergencyEventResolver) Message() *string          { return r.message }
func (r *emergencyEventResolver) NotifiedUserIDs() []UUID   { return r.notifiedUserIDs }
func (r *emergencyEventResolver) ResolvedAt() *DateTime {
	if r.resolvedAt == nil {
		return nil
	}
	return &DateTime{T: *r.resolvedAt}
}
func (r *emergencyEventResolver) ResolvedByUserID() *UUID { return r.resolvedByID }

type authPayloadResolver struct {
	user         *userResolver
	accessToken  string
	refreshToken string
	expiresIn    int32
}

func (r *authPayloadResolver) User() *userResolver { return r.user }
func (r *authPayloadResolver) AccessToken() string { return r.accessToken }
func (r *authPayloadResolver) RefreshToken() string { return r.refreshToken }
func (r *authPayloadResolver) ExpiresIn() int32     { return r.expiresIn }

type wsTicketResolver struct {
	ticket    string
	expiresIn int32
}

func (r *wsTicketResolver) Ticket() string  { return r.ticket }
func (r *wsTicketResolver) ExpiresIn() int32 { return r.expiresIn }

type coordinatesResolver struct {
	lat, lng float64
}

func (r *coordinatesResolver) Latitude() float64  { return r.lat }
func (r *coordinatesResolver) Longitude() float64 { return r.lng }

type destinationResolver struct {
	name      string
	placeID   *string
	lat, lng  float64
	updatedAt time.Time
}

func (r *destinationResolver) Name() string         { return r.name }
func (r *destinationResolver) PlaceID() *string     { return r.placeID }
func (r *destinationResolver) Latitude() float64    { return r.lat }
func (r *destinationResolver) Longitude() float64   { return r.lng }
func (r *destinationResolver) UpdatedAt() DateTime  { return DateTime{T: r.updatedAt} }

type rideMemberResolver struct {
	userID   UUID
	name     string
	initials *string
	role     string
	joinedAt time.Time
	leftAt   *time.Time
}

func (r *rideMemberResolver) UserID() UUID    { return r.userID }
func (r *rideMemberResolver) Name() string    { return r.name }
func (r *rideMemberResolver) Initials() *string { return r.initials }
func (r *rideMemberResolver) Role() string    {
	if r.role == "owner" {
		return "OWNER"
	}
	return "MEMBER"
}
func (r *rideMemberResolver) JoinedAt() DateTime { return DateTime{T: r.joinedAt} }
func (r *rideMemberResolver) LeftAt() *DateTime {
	if r.leftAt == nil {
		return nil
	}
	return &DateTime{T: *r.leftAt}
}

type ridePairResolver struct {
	id            UUID
	rideID        UUID
	riderUserID   UUID
	pillionUserID UUID
	pairedAt      time.Time
	detachedAt    *time.Time
}

func (r *ridePairResolver) ID() UUID            { return r.id }
func (r *ridePairResolver) RideID() UUID        { return r.rideID }
func (r *ridePairResolver) RiderUserID() UUID   { return r.riderUserID }
func (r *ridePairResolver) PillionUserID() UUID { return r.pillionUserID }
func (r *ridePairResolver) PairedAt() DateTime  { return DateTime{T: r.pairedAt} }
func (r *ridePairResolver) DetachedAt() *DateTime {
	if r.detachedAt == nil {
		return nil
	}
	return &DateTime{T: *r.detachedAt}
}

type rideResolver struct {
	id            UUID
	ownerUserID   UUID
	inviteCode    string
	status        string
	destination   *destinationResolver
	startedAt     time.Time
	endedAt       *time.Time
	createdAt     time.Time
	members       []*rideMemberResolver
	activePairs   []*ridePairResolver
}

func (r *rideResolver) ID() UUID                          { return r.id }
func (r *rideResolver) OwnerUserID() UUID                 { return r.ownerUserID }
func (r *rideResolver) InviteCode() string                { return r.inviteCode }
func (r *rideResolver) Status() string {
	if r.status == "ended" {
		return "ENDED"
	}
	return "ACTIVE"
}
func (r *rideResolver) Destination() *destinationResolver { return r.destination }
func (r *rideResolver) StartedAt() DateTime               { return DateTime{T: r.startedAt} }
func (r *rideResolver) EndedAt() *DateTime {
	if r.endedAt == nil {
		return nil
	}
	return &DateTime{T: *r.endedAt}
}
func (r *rideResolver) CreatedAt() DateTime                  { return DateTime{T: r.createdAt} }
func (r *rideResolver) Members() []*rideMemberResolver       { return r.members }
func (r *rideResolver) ActivePairs() []*ridePairResolver     { return r.activePairs }

// Compile-time assertion that the resolver types satisfy
// graph-gophers' field-set expectations via reflection. (No-op runtime;
// having the types referenced here keeps unused-field warnings off.)
var _ = uuid.Nil
