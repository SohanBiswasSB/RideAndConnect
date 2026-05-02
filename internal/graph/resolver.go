// Package graph hosts the GraphQL handler (graph-gophers/graphql-go)
// plus all top-level resolvers. The schema lives in schema/schema.graphqls
// and is embedded at build time.
package graph

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/SohanBiswasSB/rideandconnect/internal/auth"
	"github.com/SohanBiswasSB/rideandconnect/internal/middleware"
	"github.com/SohanBiswasSB/rideandconnect/internal/rides"
	"github.com/SohanBiswasSB/rideandconnect/internal/routes"
	"github.com/SohanBiswasSB/rideandconnect/internal/users"
)

// RouteProviderResolver is the surface routes resolvers need from the
// active route provider; an interface so tests can stub.
type RouteProviderResolver interface {
	Active() routes.Provider
}

// PresenceLookup is the seam over the realtime Hub. main.go wraps the
// hub with an adapter that converts realtime.LastLocation to
// rides.LastLocation so neither package needs to import the other.
type PresenceLookup = rides.PresenceLookup

// EmergencyHook is invoked after a successful triggerEmergency mutation
// to push the WS message. Wired in main.go to hub.BroadcastEmergency.
type EmergencyHook func(rideID, triggeredBy, eventID uuid.UUID, name string, message *string)

// DestinationHook is invoked after setDestination. Pushes destination.updated.
type DestinationHook func(rideID, byUser uuid.UUID, name string, lat, lng float64)

// RideEndedHook is invoked after endRide. Closes the room with ride.ended.
type RideEndedHook func(rideID, endedBy uuid.UUID)

// Resolver is the top-level resolver passed to graph-gophers. Its
// methods are invoked by reflection — exact names (UpperCamelCase of the
// schema fields) and signatures matter.
type Resolver struct {
	auth            *auth.Service
	users           *users.Service
	rides           *rides.Service
	provider        RouteProviderResolver
	presence        PresenceLookup
	emergencyHook   EmergencyHook
	destinationHook DestinationHook
	rideEndedHook   RideEndedHook
}

// NewResolver constructs the root resolver with its dependencies.
func NewResolver(a *auth.Service, u *users.Service, r *rides.Service, p RouteProviderResolver) *Resolver {
	return &Resolver{auth: a, users: u, rides: r, provider: p}
}

// SetPresence wires the realtime Hub as the presence source for peerRoute.
func (r *Resolver) SetPresence(p PresenceLookup) { r.presence = p }

// SetEmergencyHook wires the WS broadcast for triggerEmergency.
func (r *Resolver) SetEmergencyHook(h EmergencyHook) { r.emergencyHook = h }

// SetDestinationHook wires the WS broadcast for setDestination.
func (r *Resolver) SetDestinationHook(h DestinationHook) { r.destinationHook = h }

// SetRideEndedHook wires the WS broadcast for endRide.
func (r *Resolver) SetRideEndedHook(h RideEndedHook) { r.rideEndedHook = h }

// requireUser pulls the authed user ID out of the request context.
// Resolvers that require auth call this first; UNAUTHENTICATED is the
// stable code (see API.md §1.3).
func requireUser(ctx context.Context) (uuid.UUID, error) {
	id, ok := middleware.UserIDFromContext(ctx)
	if !ok {
		return uuid.Nil, &gqlError{Code: "UNAUTHENTICATED", Msg: "missing or invalid bearer token"}
	}
	return id, nil
}

// requireDB is a precondition guard for resolvers that need the auth /
// users / rides services. In dev without a DATABASE_URL configured these
// services are nil; we return a stable UPSTREAM_UNAVAILABLE rather than
// panicking on a nil dereference.
func (r *Resolver) requireDB() error {
	if r == nil || r.auth == nil || r.users == nil || r.rides == nil {
		return &gqlError{Code: "UPSTREAM_UNAVAILABLE", Msg: "database not connected"}
	}
	return nil
}

// gqlError is the wire shape expected in a GraphQL error.
// graph-gophers reads `Extensions()` via reflection on errors so we can
// surface our domain codes through the standard error path.
type gqlError struct {
	Code  string
	Msg   string
	Extra map[string]any
}

func (e *gqlError) Error() string { return e.Msg }

// Extensions is recognized by graph-gophers — values land in
// `errors[].extensions` on the wire.
func (e *gqlError) Extensions() map[string]any {
	out := map[string]any{"code": e.Code}
	for k, v := range e.Extra {
		out[k] = v
	}
	return out
}

// mapDomainErr translates internal sentinels into stable GraphQL codes.
func mapDomainErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return &gqlError{Code: "INVALID_CREDENTIALS", Msg: "invalid credentials"}
	case errors.Is(err, auth.ErrIdentifierMissing):
		return &gqlError{Code: "VALIDATION_FAILED", Msg: "email or phone required"}
	case errors.Is(err, auth.ErrEmailTaken):
		return &gqlError{Code: "EMAIL_TAKEN", Msg: "email already registered"}
	case errors.Is(err, auth.ErrPhoneTaken):
		return &gqlError{Code: "EMAIL_TAKEN", Msg: "phone already registered"}
	case errors.Is(err, auth.ErrPasswordWeak):
		return &gqlError{Code: "VALIDATION_FAILED", Msg: "password too weak"}
	case errors.Is(err, auth.ErrRefreshInvalid), errors.Is(err, auth.ErrRefreshRevoked):
		return &gqlError{Code: "UNAUTHENTICATED", Msg: "refresh token invalid"}

	case errors.Is(err, rides.ErrNotFound):
		return &gqlError{Code: "NOT_FOUND", Msg: "ride not found"}
	case errors.Is(err, rides.ErrInviteInvalid):
		return &gqlError{Code: "INVITE_INVALID", Msg: "invite code invalid"}
	case errors.Is(err, rides.ErrRideFull):
		return &gqlError{Code: "RIDE_FULL", Msg: "ride already has the maximum of 3 members"}
	case errors.Is(err, rides.ErrAlreadyMember):
		return &gqlError{Code: "ALREADY_MEMBER", Msg: "already a member of this ride"}
	case errors.Is(err, rides.ErrNotMember):
		return &gqlError{Code: "FORBIDDEN", Msg: "not a member of this ride"}
	case errors.Is(err, rides.ErrNotOwner):
		return &gqlError{Code: "FORBIDDEN", Msg: "owner-only operation"}
	case errors.Is(err, rides.ErrEnded):
		return &gqlError{Code: "CONFLICT", Msg: "ride already ended"}
	case errors.Is(err, rides.ErrPairExists):
		return &gqlError{Code: "CONFLICT", Msg: "rider already has an active pillion"}
	case errors.Is(err, rides.ErrPillionPaired):
		return &gqlError{Code: "CONFLICT", Msg: "pillion already in another pair"}
	case errors.Is(err, rides.ErrPairSameUser):
		return &gqlError{Code: "VALIDATION_FAILED", Msg: "cannot pair user with themselves"}
	case errors.Is(err, rides.ErrNotPaired):
		return &gqlError{Code: "CONFLICT", Msg: "no active pair to detach"}
	case errors.Is(err, rides.ErrValidation):
		return &gqlError{Code: "VALIDATION_FAILED", Msg: err.Error()}

	case errors.Is(err, rides.ErrInviteNotFound):
		return &gqlError{Code: "NOT_FOUND", Msg: "invitation not found"}
	case errors.Is(err, rides.ErrInviteNotPending):
		return &gqlError{Code: "CONFLICT", Msg: "invitation no longer pending"}
	case errors.Is(err, rides.ErrInviteExpired):
		return &gqlError{Code: "CONFLICT", Msg: "invitation expired"}
	case errors.Is(err, rides.ErrInviteWrongUser):
		return &gqlError{Code: "FORBIDDEN", Msg: "invitation addressed to another user"}

	case errors.Is(err, rides.ErrEmergencyNotFound):
		return &gqlError{Code: "NOT_FOUND", Msg: "emergency event not found"}
	case errors.Is(err, rides.ErrEmergencyAlreadyClosed):
		return &gqlError{Code: "CONFLICT", Msg: "emergency already resolved"}
	case errors.Is(err, rides.ErrEmergencyNotAuthorized):
		return &gqlError{Code: "FORBIDDEN", Msg: "not authorized to resolve this emergency"}

	case errors.Is(err, users.ErrNotFound):
		return &gqlError{Code: "NOT_FOUND", Msg: "user not found"}
	}
	// Unknown errors — return the raw cause as INTERNAL. Resolvers also
	// log the original via the request logger before this mapping.
	return &gqlError{Code: "INTERNAL", Msg: "internal error"}
}

// userToResolver adapts a users.User into the GraphQL resolver type.
func userToResolver(u *users.User) *userResolver {
	if u == nil {
		return nil
	}
	out := &userResolver{
		id:              UUID{V: u.ID},
		name:            u.Name,
		email:           u.Email,
		phone:           u.Phone,
		initials:        u.Initials,
		bikeModel:       u.BikeModel,
		licensePlate:    u.LicensePlate,
		defaultBikeRole: u.DefaultBikeRole,
		bloodType:       u.BloodType,

		tyreFrontSoloPsi:    u.TyreFrontSoloPsi,
		tyreRearSoloPsi:     u.TyreRearSoloPsi,
		tyreFrontPillionPsi: u.TyreFrontPillionPsi,
		tyreRearPillionPsi:  u.TyreRearPillionPsi,

		emergencyContactName:  u.EmergencyContactName,
		emergencyContactPhone: u.EmergencyContactPhone,
		emergencyContactEmail: u.EmergencyContactEmail,

		createdAt: u.CreatedAt,
	}
	if u.Age != nil {
		v := int32(*u.Age)
		out.age = &v
	}
	if u.HeightCm != nil {
		v := int32(*u.HeightCm)
		out.heightCm = &v
	}
	if u.WeightKg != nil {
		v := int32(*u.WeightKg)
		out.weightKg = &v
	}
	if u.EmergencyContactUserID != nil {
		v := UUID{V: *u.EmergencyContactUserID}
		out.emergencyContactUserID = &v
	}
	return out
}

func invitationToResolver(inv *rides.Invitation) *rideInvitationResolver {
	return &rideInvitationResolver{
		id:             UUID{V: inv.ID},
		rideID:         UUID{V: inv.RideID},
		invitedUserID:  UUID{V: inv.InvitedUserID},
		invitedBy:      UUID{V: inv.InvitedBy},
		identifierUsed: inv.IdentifierUsed,
		status:         string(inv.Status),
		createdAt:      inv.CreatedAt,
		expiresAt:      inv.ExpiresAt,
		respondedAt:    inv.RespondedAt,
	}
}

func emergencyToResolver(ev *rides.EmergencyEvent) *emergencyEventResolver {
	if ev == nil {
		return nil
	}
	out := &emergencyEventResolver{
		id:            UUID{V: ev.ID},
		triggeredByID: UUID{V: ev.TriggeredByUserID},
		triggeredAt:   ev.TriggeredAt,
		message:       ev.Message,
		resolvedAt:    ev.ResolvedAt,
	}
	if ev.RideID != nil {
		v := UUID{V: *ev.RideID}
		out.rideID = &v
	}
	if ev.ResolvedByUserID != nil {
		v := UUID{V: *ev.ResolvedByUserID}
		out.resolvedByID = &v
	}
	out.notifiedUserIDs = make([]UUID, 0, len(ev.NotifiedUserIDs))
	for _, id := range ev.NotifiedUserIDs {
		out.notifiedUserIDs = append(out.notifiedUserIDs, UUID{V: id})
	}
	return out
}

func rideToResolver(r *rides.Ride) *rideResolver {
	out := &rideResolver{
		id:          UUID{V: r.ID},
		ownerUserID: UUID{V: r.OwnerUserID},
		inviteCode:  r.InviteCode,
		status:      string(r.Status),
		startedAt:   r.StartedAt,
		endedAt:     r.EndedAt,
		createdAt:   r.CreatedAt,
	}
	if r.Destination != nil {
		out.destination = &destinationResolver{
			name:      r.Destination.Name,
			placeID:   r.Destination.PlaceID,
			lat:       r.Destination.Latitude,
			lng:       r.Destination.Longitude,
			updatedAt: r.Destination.UpdatedAt,
		}
	}
	for _, m := range r.Members {
		mm := m
		out.members = append(out.members, &rideMemberResolver{
			userID:   UUID{V: mm.UserID},
			name:     mm.Name,
			initials: mm.Initials,
			role:     string(mm.Role),
			joinedAt: mm.JoinedAt,
			leftAt:   mm.LeftAt,
		})
	}
	for _, p := range r.ActivePairs {
		pp := p
		out.activePairs = append(out.activePairs, pairToResolver(&pp))
	}
	return out
}

func pairToResolver(p *rides.Pair) *ridePairResolver {
	return &ridePairResolver{
		id:            UUID{V: p.ID},
		rideID:        UUID{V: p.RideID},
		riderUserID:   UUID{V: p.RiderUserID},
		pillionUserID: UUID{V: p.PillionUserID},
		pairedAt:      p.PairedAt,
		detachedAt:    p.DetachedAt,
	}
}
