package graph

import (
	"context"

	"github.com/SohanBiswasSB/rideandconnect/internal/rides"
)

type destinationInput struct {
	Name      string
	PlaceID   *string
	Latitude  float64
	Longitude float64
}

func (d *destinationInput) toDomain() rides.DestinationInput {
	return rides.DestinationInput{
		Name:      d.Name,
		PlaceID:   d.PlaceID,
		Latitude:  d.Latitude,
		Longitude: d.Longitude,
	}
}

// ─── Queries ────────────────────────────────────────────────────────────

func (r *Resolver) Ride(ctx context.Context, args struct{ RideID UUID }) (*rideResolver, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ride, err := r.rides.Get(ctx, args.RideID.V, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return rideToResolver(ride), nil
}

func (r *Resolver) RideMembers(ctx context.Context, args struct{ RideID UUID }) ([]*rideMemberResolver, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ride, err := r.rides.Get(ctx, args.RideID.V, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return rideToResolver(ride).Members(), nil
}

func (r *Resolver) Destination(ctx context.Context, args struct{ RideID UUID }) (*destinationResolver, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ride, err := r.rides.Get(ctx, args.RideID.V, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if ride.Destination == nil {
		return nil, nil
	}
	return rideToResolver(ride).Destination(), nil
}

// ─── Mutations: lifecycle ───────────────────────────────────────────────

func (r *Resolver) CreateRide(ctx context.Context, args struct{ Destination *destinationInput }) (*rideResolver, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	var dest *rides.DestinationInput
	if args.Destination != nil {
		d := args.Destination.toDomain()
		dest = &d
	}
	ride, err := r.rides.Create(ctx, uid, dest)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return rideToResolver(ride), nil
}

func (r *Resolver) JoinRide(ctx context.Context, args struct {
	RideID     UUID
	InviteCode string
}) (*rideResolver, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ride, err := r.rides.Join(ctx, args.RideID.V, uid, args.InviteCode)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return rideToResolver(ride), nil
}

func (r *Resolver) LeaveRide(ctx context.Context, args struct{ RideID UUID }) (bool, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return false, err
	}
	if err := r.rides.Leave(ctx, args.RideID.V, uid); err != nil {
		return false, mapDomainErr(err)
	}
	return true, nil
}

func (r *Resolver) EndRide(ctx context.Context, args struct{ RideID UUID }) (*rideResolver, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ride, err := r.rides.End(ctx, args.RideID.V, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if r.rideEndedHook != nil {
		hook := r.rideEndedHook
		rideID := args.RideID.V
		asyncFire("ride.ended.broadcast", func() { hook(rideID, uid) })
	}
	return rideToResolver(ride), nil
}

// ─── Mutations: destination ─────────────────────────────────────────────

func (r *Resolver) SetDestination(ctx context.Context, args struct {
	RideID UUID
	Input  destinationInput
}) (*destinationResolver, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	d, err := r.rides.SetDestination(ctx, args.RideID.V, uid, args.Input.toDomain())
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if r.destinationHook != nil {
		hook := r.destinationHook
		rideID := args.RideID.V
		name, lat, lng := d.Name, d.Latitude, d.Longitude
		asyncFire("destination.updated.broadcast", func() { hook(rideID, uid, name, lat, lng) })
	}
	return &destinationResolver{
		name:      d.Name,
		placeID:   d.PlaceID,
		lat:       d.Latitude,
		lng:       d.Longitude,
		updatedAt: d.UpdatedAt,
	}, nil
}

// ─── Mutations: rider/pillion pairing ───────────────────────────────────

func (r *Resolver) PairWithRider(ctx context.Context, args struct {
	RideID      UUID
	RiderUserID UUID
}) (*ridePairResolver, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	pair, err := r.rides.Pair(ctx, args.RideID.V, uid, args.RiderUserID.V)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return pairToResolver(pair), nil
}

func (r *Resolver) DetachFromRider(ctx context.Context, args struct{ RideID UUID }) (bool, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return false, err
	}
	if err := r.rides.Detach(ctx, args.RideID.V, uid); err != nil {
		return false, mapDomainErr(err)
	}
	return true, nil
}
