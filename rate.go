package wallet

import (
	"context"

	"github.com/arandu-io/framework/security"
)

// RateProvider converts money between currencies.
//
// It is the seam and not an implementation, and this package ships no
// implementation of it: a rate comes from somewhere outside the process, and a
// package that declares network = false has nowhere to get one. An application
// that moves money between currencies writes the provider it trusts and hands
// it to Config, which is the one place the choice is visible.
//
// The signature takes and returns Money rather than a rate, and that is what
// keeps a float out of this package: a rate is a fraction, applying it is a
// multiplication and a rounding, and both belong to whoever knows which
// direction the rounding is allowed to go. What comes back is already an
// integer of minor units at the target scale, and this package moves it without
// reinterpreting it.
//
// The Grant is first because a rate can be a tenant's own -- a negotiated
// corporate rate, a rate table an application sells -- and a provider that
// cannot tell whose rate it is asked for is a provider that answers with
// somebody else's.
type RateProvider interface {
	// ConvertTo returns from, expressed in the currency to at the scale
	// toDecimalPlaces.
	//
	// It reports an error rather than an approximation when the pair has no
	// rate: money that moved at a rate nobody had is money that has to be
	// unwound by hand.
	ConvertTo(ctx context.Context, g security.Grant, from Money, to Currency, toDecimalPlaces int) (Money, error)
}
