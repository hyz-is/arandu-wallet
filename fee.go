package wallet

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/arandu-io/framework/security"
)

// The refusals about a fee or a discount. Each is a different thing to fix: a
// malformed schedule is the provider, a destination that cannot take the money
// is the wiring, and an amount that a fee would consume is the request.
var (
	// ErrFeeShare is returned when a fee is not a positive fraction smaller
	// than one. Zero would be no fee written as one, and anything from one
	// upwards would take at least the whole payment, which is not a fee on a
	// payment but the payment itself.
	ErrFeeShare = errors.New("wallet: a fee has to be a fraction of two positive integers, smaller than one")

	// ErrFeeBounds is returned when the floor and the ceiling of a fee cannot
	// both be honored: either is negative, or the floor is above the ceiling.
	ErrFeeBounds = errors.New("wallet: the smallest fee is larger than the largest, or one of them is negative")

	// ErrFeeWallet is returned when a fee has nowhere to go: no wallet named,
	// a wallet that is one of the two in the movement, or one that does not
	// exist. A fee that is taken and not credited is money that leaves the
	// wallets and arrives nowhere, which is a difference nobody can add up
	// afterwards.
	ErrFeeWallet = errors.New("wallet: a fee needs a third wallet to be credited to")

	// ErrFeeCurrencyMismatch is returned when a fee would have to cross a rate:
	// the two wallets are not counted the same way, or the wallet the fee is
	// credited to is not counted like them.
	//
	// It is refused rather than converted. The fee is a share of the payment
	// and is charged in the payment's money; carrying it through a rate would
	// round a number that is already the result of a rounding, and neither side
	// of the movement would add up afterwards.
	ErrFeeCurrencyMismatch = errors.New("wallet: a fee is charged in the money of the payment, and these wallets are not counted the same way")

	// ErrFeeExceedsAmount is returned when the fee would leave nothing to
	// deliver. A payment that arrives as zero is a payment that took money and
	// delivered none.
	ErrFeeExceedsAmount = errors.New("wallet: the fee is not smaller than the payment it is charged on")

	// ErrDiscountNegative is returned when a discount is a negative number. A
	// discount lowers what is paid; one that raised it would be a fee nobody
	// declared, charged through the field that says it is not one.
	ErrDiscountNegative = errors.New("wallet: a discount cannot be negative")
)

// FeeSchedule is what a wallet charges to be paid: a share of the payment, with
// a floor and a ceiling, and the wallet the money goes to.
//
// The share is a fraction of two integers and never a percentage in a float,
// for the reason a rate is: 2.9% has no binary representation, so a schedule
// held as a float already charges something other than what somebody wrote
// down. The bounds are amounts in the money the payment is made in, and the
// destination is a wallet counted the same way -- which together mean the whole
// calculation happens in one money, at one scale, with no rate anywhere in it.
//
// The zero value charges nothing, and that is the ordinary case. Anything else
// has to be complete: a schedule with a floor and no destination, or a share
// and no denominator, is a mistake somebody made rather than a fee somebody
// meant, and Validate says which.
type FeeSchedule struct {
	// Numerator and Denominator are the share of the payment the fee is, as an
	// exact fraction smaller than one.
	Numerator   int64
	Denominator int64

	// Minimum is the smallest fee that may be charged, in the payment's minor
	// units, and zero is no floor. A share that comes out under it is raised to
	// it: a fee that rounds to nothing on a small payment is a fee that costs
	// more to move than it collects.
	Minimum Amount

	// Maximum is the largest fee that may be charged, and zero is no ceiling.
	Maximum Amount

	// Deductible reverses who pays. False is the ordinary payment, where the
	// fee is added to what the payer sends and the receiver is paid in full;
	// true takes the fee out of what arrives, and the payer sends exactly what
	// they were asked for.
	Deductible bool

	// WalletID is the wallet the fee is credited to. It is required, and it is
	// neither of the two wallets in the movement: a fee that goes back to the
	// payer or to the receiver is not a fee, it is a smaller payment.
	WalletID string
}

// Charges reports that this schedule means to charge anything at all.
//
// Any field set is a schedule, and the zero value is not one. A half-filled
// schedule is therefore a mistake Validate reports rather than a fee that
// quietly comes out as nothing.
func (f FeeSchedule) Charges() bool { return f != FeeSchedule{} }

// Validate reports why this schedule cannot be applied, and nil when it can.
func (f FeeSchedule) Validate() error {
	if f.Numerator <= 0 || f.Denominator <= 0 || f.Numerator >= f.Denominator {
		return fmt.Errorf("%w: got %d/%d", ErrFeeShare, f.Numerator, f.Denominator)
	}
	if f.Minimum < 0 || f.Maximum < 0 {
		return fmt.Errorf("%w: got %d and %d", ErrFeeBounds, f.Minimum, f.Maximum)
	}
	if f.Maximum > 0 && f.Minimum > f.Maximum {
		return fmt.Errorf("%w: got %d and %d", ErrFeeBounds, f.Minimum, f.Maximum)
	}
	if f.WalletID == "" {
		return fmt.Errorf("%w: the schedule names none", ErrFeeWallet)
	}
	return nil
}

// Fee is what a schedule takes out of one payment.
//
// The remainder is what truncating the share left behind, over the schedule's
// denominator, and it is what makes the arithmetic checkable from outside: the
// payment multiplied by the numerator is the fee multiplied by the denominator
// plus the remainder, exactly. Where a bound decided the fee instead of the
// share, nothing was truncated and the remainder is zero -- the row carries the
// bounds as well, so which of the two happened is readable rather than guessed.
type Fee struct {
	// Amount is the fee, in the payment's minor units.
	Amount Amount
	// RemainderNumerator is what truncating the share left, over
	// RemainderDenominator, and it is always smaller than it.
	RemainderNumerator int64
	// RemainderDenominator is what the remainder is a fraction of: one minor
	// unit of the payment.
	RemainderDenominator int64
}

// Exact reports that the share divided evenly, or that a bound decided the fee
// and there was nothing to divide.
func (f Fee) Exact() bool { return f.RemainderNumerator == 0 }

// Fee is what this schedule takes out of the payment.
//
// The arithmetic is the exchange's, exactly: integers throughout, one division,
// truncated toward zero, and what truncation left is a value rather than a
// difference. The share of a payment is never rounded up, because a fee rounded
// up is money charged that no schedule justifies.
//
// The floor and the ceiling are applied after the share and in that order, so a
// schedule whose ceiling is under its floor cannot be built -- Validate refuses
// it -- and the two can never disagree about one payment.
func (f FeeSchedule) Fee(payment Money) (Fee, error) {
	if err := f.Validate(); err != nil {
		return Fee{}, err
	}
	if !ValidDecimalPlaces(payment.DecimalPlaces) {
		return Fee{}, fmt.Errorf("%w: got %d", ErrDecimalPlaces, payment.DecimalPlaces)
	}
	if payment.Amount <= 0 {
		return Fee{}, ErrAmountNotPositive
	}

	numerator := new(big.Int).Mul(big.NewInt(int64(payment.Amount)), big.NewInt(f.Numerator))
	remainder := new(big.Int)
	numerator.QuoRem(numerator, big.NewInt(f.Denominator), remainder)
	// The share is smaller than the payment because the fraction is smaller
	// than one, so this fits wherever the payment did. The check is here rather
	// than left to that argument, so that widening the fraction fails loudly
	// instead of writing a fee that wrapped.
	if !numerator.IsInt64() {
		return Fee{}, ErrAmountOverflow
	}

	charged := Fee{
		Amount:               Amount(numerator.Int64()),
		RemainderNumerator:   remainder.Int64(),
		RemainderDenominator: f.Denominator,
	}
	// A bound replaces the share rather than adjusting it, so nothing was
	// truncated and there is no remainder to report.
	switch {
	case charged.Amount < f.Minimum:
		charged = Fee{Amount: f.Minimum, RemainderDenominator: f.Denominator}
	case f.Maximum > 0 && charged.Amount > f.Maximum:
		charged = Fee{Amount: f.Maximum, RemainderDenominator: f.Denominator}
	}
	return charged, nil
}

// FeeProvider answers with what a wallet charges to be paid.
//
// It is the seam and not an implementation, for the reason RateProvider is one:
// what a merchant charges is the application's business, it changes without
// this package being rebuilt, and a package that decided it would be deciding
// somebody's pricing. The schedule is asked for once per payment and written
// down as it was answered, so a provider that answers differently a moment
// later does not change what a receipt already said.
//
// The wallet it is asked about is the one being paid, because a fee is charged
// by whoever receives the money. The payment is what would arrive before the
// fee, so a schedule can be decided by size -- which is what a floor and a
// ceiling are for.
//
// The Grant is first because a schedule can be a tenant's own, and a provider
// that cannot tell whose fee it is asked for is a provider that answers with
// somebody else's.
type FeeProvider interface {
	// Fee returns what the receiver charges to be paid this amount.
	//
	// The zero FeeSchedule is no fee, and is the ordinary answer. An error is
	// a provider that could not decide, and refuses the payment rather than
	// letting it through free.
	Fee(ctx context.Context, g security.Grant, receiver Wallet, payment Money) (FeeSchedule, error)
}

// DiscountProvider answers with what one payer is charged less.
//
// It is the seam for the part of pricing that is about who is paying rather
// than about what is being paid for: a negotiated rate, a first payment, a
// loyalty that an application tracks and this package has never heard of. Both
// wallets are named because a discount belongs to the pair, and the payment is
// there because a discount can depend on the size of it.
//
// What comes back lowers the payment: the payer is debited less and the
// receiver is credited less, which is what a discount is. It is recorded on the
// operation, so a receipt that says a smaller number than the request asked for
// says why.
type DiscountProvider interface {
	// Discount returns how much to take off this payment, in the payer's minor
	// units. Zero is the ordinary answer, and a negative one is refused.
	Discount(ctx context.Context, g security.Grant, payer, receiver Wallet, payment Money) (Amount, error)
}
