package graph

import (
	"context"
)

func (r *Resolver) TriggerEmergency(ctx context.Context, args struct{ Message *string }) (*emergencyEventResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ev, err := r.rides.TriggerEmergency(ctx, uid, args.Message)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	// Push to active ride members over WS — fire-and-forget.
	if r.emergencyHook != nil && ev.RideID != nil {
		name, _ := r.users.NameFor(ctx, uid)
		r.emergencyHook(*ev.RideID, uid, ev.ID, name, ev.Message)
	}
	return emergencyToResolver(ev), nil
}

func (r *Resolver) ResolveEmergency(ctx context.Context, args struct{ EventID UUID }) (*emergencyEventResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ev, err := r.rides.ResolveEmergency(ctx, args.EventID.V, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return emergencyToResolver(ev), nil
}

func (r *Resolver) MyActiveEmergency(ctx context.Context) (*emergencyEventResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ev, err := r.rides.MyActiveEmergency(ctx, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if ev == nil {
		return nil, nil
	}
	return emergencyToResolver(ev), nil
}
