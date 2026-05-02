package graph

import (
	"context"
	"time"
)

func (r *Resolver) TriggerEmergency(ctx context.Context, args struct{ Message *string }) (*emergencyEventResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ev, created, err := r.rides.TriggerEmergency(ctx, uid, args.Message)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	// Push to active ride members over WS only on the first trigger;
	// subsequent panic-button taps return the existing event without
	// re-broadcasting.
	if created && r.emergencyHook != nil && ev.RideID != nil {
		rideID := *ev.RideID
		eventID := ev.ID
		message := ev.Message
		users := r.users
		hook := r.emergencyHook
		asyncFire("emergency.broadcast", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			name, _ := users.NameFor(ctx, uid)
			hook(rideID, uid, eventID, name, message)
		})
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
