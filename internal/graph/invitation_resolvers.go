package graph

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ─── Invitations ────────────────────────────────────────────────────────

func (r *Resolver) InviteByIdentifier(ctx context.Context, args struct {
	RideID     UUID
	Identifier string
}) ([]*rideInvitationResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}

	matched, err := r.users.FindByIdentifier(ctx, args.Identifier)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	ids := make([]uuid.UUID, 0, len(matched))
	for _, u := range matched {
		ids = append(ids, u.ID)
	}

	invs, err := r.rides.Invite(ctx, args.RideID.V, uid, args.Identifier, ids)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if len(invs) == 0 && len(ids) == 0 {
		return nil, &gqlError{Code: "NOT_FOUND", Msg: fmt.Sprintf("no user matched identifier %q", args.Identifier)}
	}
	out := make([]*rideInvitationResolver, 0, len(invs))
	for i := range invs {
		inv := invs[i]
		out = append(out, invitationToResolver(&inv))
	}
	return out, nil
}

func (r *Resolver) AcceptInvitation(ctx context.Context, args struct{ InvitationID UUID }) (*rideResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ride, err := r.rides.AcceptInvitation(ctx, args.InvitationID.V, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return rideToResolver(ride), nil
}

func (r *Resolver) DeclineInvitation(ctx context.Context, args struct{ InvitationID UUID }) (bool, error) {
	if err := r.requireDB(); err != nil {
		return false, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return false, err
	}
	if err := r.rides.DeclineInvitation(ctx, args.InvitationID.V, uid); err != nil {
		return false, mapDomainErr(err)
	}
	return true, nil
}

func (r *Resolver) RevokeInvitation(ctx context.Context, args struct{ InvitationID UUID }) (bool, error) {
	if err := r.requireDB(); err != nil {
		return false, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return false, err
	}
	if err := r.rides.RevokeInvitation(ctx, args.InvitationID.V, uid); err != nil {
		return false, mapDomainErr(err)
	}
	return true, nil
}

func (r *Resolver) MyInvitations(ctx context.Context) ([]*rideInvitationResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	invs, err := r.rides.MyInvitations(ctx, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	out := make([]*rideInvitationResolver, 0, len(invs))
	for i := range invs {
		inv := invs[i]
		out = append(out, invitationToResolver(&inv))
	}
	return out, nil
}
