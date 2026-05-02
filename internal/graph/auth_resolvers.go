package graph

import (
	"context"

	"github.com/SohanBiswasSB/rideandconnect/internal/auth"
	"github.com/SohanBiswasSB/rideandconnect/internal/users"
)

// ─── Inputs (graph-gophers binds these by JSON tag at runtime via reflection) ───

type registerInput struct {
	Name             string
	Email            *string
	Phone            *string
	Password         string
	Initials         *string
	BikeModel        *string
	LicensePlate     *string
	DefaultBikeRole  *string
	BloodType        *string
	Age              *int32
	HeightCm         *int32
	WeightKg         *int32
	TyrePressure     *tyrePressureInput
	EmergencyContact *emergencyContactInput
}

type tyrePressureInput struct {
	FrontSoloPsi    *float64
	RearSoloPsi     *float64
	FrontPillionPsi *float64
	RearPillionPsi  *float64
}

type emergencyContactInput struct {
	Name          *string
	Phone         *string
	Email         *string
	ContactUserID *UUID
}

type updateProfileInput struct {
	Name             *string
	Initials         *string
	BikeModel        *string
	LicensePlate     *string
	DefaultBikeRole  *string
	BloodType        *string
	Age              *int32
	HeightCm         *int32
	WeightKg         *int32
	TyrePressure     *tyrePressureInput
	EmergencyContact *emergencyContactInput
}

type loginInput struct {
	Email    *string
	Phone    *string
	Password string
}

// ─── Mutations ──────────────────────────────────────────────────────────

func (r *Resolver) Register(ctx context.Context, args struct{ Input registerInput }) (*authPayloadResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	in := args.Input
	reg := auth.RegisterInput{
		Name:            in.Name,
		Email:           in.Email,
		Phone:           in.Phone,
		Password:        in.Password,
		Initials:        in.Initials,
		BikeModel:       in.BikeModel,
		LicensePlate:    in.LicensePlate,
		DefaultBikeRole: in.DefaultBikeRole,
		BloodType:       in.BloodType,
		Age:             int32To16Ptr(in.Age),
		HeightCm:        int32To16Ptr(in.HeightCm),
		WeightKg:        int32To16Ptr(in.WeightKg),
	}
	if in.TyrePressure != nil {
		reg.TyreFrontSoloPsi = in.TyrePressure.FrontSoloPsi
		reg.TyreRearSoloPsi = in.TyrePressure.RearSoloPsi
		reg.TyreFrontPillionPsi = in.TyrePressure.FrontPillionPsi
		reg.TyreRearPillionPsi = in.TyrePressure.RearPillionPsi
	}
	if in.EmergencyContact != nil {
		reg.EmergencyContactName = in.EmergencyContact.Name
		reg.EmergencyContactPhone = in.EmergencyContact.Phone
		reg.EmergencyContactEmail = in.EmergencyContact.Email
		if in.EmergencyContact.ContactUserID != nil {
			v := in.EmergencyContact.ContactUserID.V
			reg.EmergencyContactUserID = &v
		}
	}
	res, err := r.auth.Register(ctx, reg)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	user, err := r.users.ByID(ctx, res.UserID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return &authPayloadResolver{
		user:         userToResolver(user),
		accessToken:  res.AccessToken,
		refreshToken: res.RefreshToken,
		expiresIn:    int32(res.ExpiresIn),
	}, nil
}

func (r *Resolver) Login(ctx context.Context, args struct{ Input loginInput }) (*authPayloadResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	in := args.Input
	res, err := r.auth.Login(ctx, auth.LoginInput{
		Email:    in.Email,
		Phone:    in.Phone,
		Password: in.Password,
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	user, err := r.users.ByID(ctx, res.UserID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return &authPayloadResolver{
		user:         userToResolver(user),
		accessToken:  res.AccessToken,
		refreshToken: res.RefreshToken,
		expiresIn:    int32(res.ExpiresIn),
	}, nil
}

func (r *Resolver) Refresh(ctx context.Context, args struct{ RefreshToken string }) (*authPayloadResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	res, err := r.auth.Refresh(ctx, args.RefreshToken)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	user, err := r.users.ByID(ctx, res.UserID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return &authPayloadResolver{
		user:         userToResolver(user),
		accessToken:  res.AccessToken,
		refreshToken: res.RefreshToken,
		expiresIn:    int32(res.ExpiresIn),
	}, nil
}

func (r *Resolver) Logout(ctx context.Context, args struct{ RefreshToken string }) (bool, error) {
	if err := r.requireDB(); err != nil {
		return false, err
	}
	if err := r.auth.Logout(ctx, args.RefreshToken); err != nil {
		return false, mapDomainErr(err)
	}
	return true, nil
}

func (r *Resolver) WsTicket(ctx context.Context) (*wsTicketResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	tok, exp, err := r.auth.IssueWSTicket(uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	expiresIn := int32(exp.Sub(timeNow()).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}
	return &wsTicketResolver{ticket: tok, expiresIn: expiresIn}, nil
}

// ─── Query: me ──────────────────────────────────────────────────────────

// int32To16Ptr is a small helper for the GraphQL Int → DB smallint cast.
// Out-of-range values fall back to nil so the DB CHECK is the canonical
// validator (resolvers stay thin).
func int32To16Ptr(p *int32) *int16 {
	if p == nil {
		return nil
	}
	v := int16(*p)
	return &v
}

func (r *Resolver) UpdateProfile(ctx context.Context, args struct{ Input updateProfileInput }) (*userResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	in := args.Input
	upd := users.UpdateProfileInput{
		Name:            in.Name,
		Initials:        in.Initials,
		BikeModel:       in.BikeModel,
		LicensePlate:    in.LicensePlate,
		DefaultBikeRole: in.DefaultBikeRole,
		BloodType:       in.BloodType,
		Age:             int32To16Ptr(in.Age),
		HeightCm:        int32To16Ptr(in.HeightCm),
		WeightKg:        int32To16Ptr(in.WeightKg),
	}
	if in.TyrePressure != nil {
		upd.TyreFrontSoloPsi = in.TyrePressure.FrontSoloPsi
		upd.TyreRearSoloPsi = in.TyrePressure.RearSoloPsi
		upd.TyreFrontPillionPsi = in.TyrePressure.FrontPillionPsi
		upd.TyreRearPillionPsi = in.TyrePressure.RearPillionPsi
	}
	if in.EmergencyContact != nil {
		upd.EmergencyContactName = in.EmergencyContact.Name
		upd.EmergencyContactPhone = in.EmergencyContact.Phone
		upd.EmergencyContactEmail = in.EmergencyContact.Email
		if in.EmergencyContact.ContactUserID != nil {
			v := in.EmergencyContact.ContactUserID.V
			upd.EmergencyContactUserID = &v
		}
	}
	u, err := r.users.UpdateProfile(ctx, uid, upd)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return userToResolver(u), nil
}

func (r *Resolver) Me(ctx context.Context) (*userResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	u, err := r.users.ByID(ctx, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return userToResolver(u), nil
}
