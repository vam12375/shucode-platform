package media

import "context"

type billingIdentityContextKey struct{}

// BillingIdentity is an opaque server-owned session binding copied into media
// project and reservation facts. Empty BindingID keeps legacy clients working.
type BillingIdentity struct {
	BindingID string
	DeviceID  string
}

func WithBillingIdentity(ctx context.Context, identity BillingIdentity) context.Context {
	return context.WithValue(ctx, billingIdentityContextKey{}, identity)
}

func billingIdentityFromContext(ctx context.Context) BillingIdentity {
	identity, _ := ctx.Value(billingIdentityContextKey{}).(BillingIdentity)
	return identity
}

func bindProjectIdentity(ctx context.Context, p *Project) {
	identity := billingIdentityFromContext(ctx)
	if p != nil {
		if identity.BindingID != "" {
			p.BillingBindingID = identity.BindingID
		}
		if identity.DeviceID != "" {
			p.BillingDeviceID = identity.DeviceID
		}
	}
}

func bindFixedFeeIdentity(ctx context.Context, fee *FixedFee) {
	identity := billingIdentityFromContext(ctx)
	if fee != nil {
		if identity.BindingID != "" {
			fee.BillingBindingID = identity.BindingID
		}
		if identity.DeviceID != "" {
			fee.DeviceTokenID = identity.DeviceID
		}
	}
}
