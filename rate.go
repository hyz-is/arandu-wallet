package wallet

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/arandu-io/framework/security"
)

// MaxRateDenominator is the largest denominator a rate may be quoted with.
//
// A billion, and the number is not arbitrary: the leftover of a conversion is
// recorded over a denominator of Denominator multiplied by ten to the source's
// scale, and both of those are bounded here and by MaxDecimalPlaces so that the
// product still fits in the int64 the column holds. Ten to the ninth times ten
// to the ninth is ten to the eighteenth, and an int64 reaches past nine of
// those.
//
// It bounds the denominator and never the numerator, because bounding the
// numerator would bound the rate: a currency worth a hundred million of another
// is a rate somebody quotes, and refusing it would be refusing the pair.
const MaxRateDenominator = 1_000_000_000

// Rounding names the rule that turns an exact conversion into whole minor
// units.
//
// It is a named value on the record rather than a fact a reader has to know,
// because a row that says how it was rounded is a row somebody can reproduce
// without reading the code that wrote it.
type Rounding string

// RoundDown is the rule this package applies, and the only one.
//
// The exact value is truncated toward zero, so a conversion never credits more
// than the rate justifies -- rounding up would credit money that no rate
// produced and that somebody would have to fund. What truncation leaves behind
// is smaller than one minor unit of the target and cannot be credited, because
// there is no smaller unit to credit it in; it is written on the conversion as
// an exact fraction instead, so the part that could not move is a number
// somebody can find rather than a difference nobody can explain.
//
// One rule and no setting. A rounding mode an application chooses is two
// answers to "what is this amount worth", and the one a statement was written
// under would be whichever the configuration said that day.
const RoundDown Rounding = "down"

// The refusals about a rate. They are separate values because each one is a
// different thing to fix: a pair that does not match is wiring, a fraction that
// is not positive is the provider, and an amount that vanishes is the request.
var (
	// ErrRateNotPositive is returned when a rate is not a positive fraction.
	// Zero would convert every amount to nothing and a negative one would turn
	// a credit into a debit.
	ErrRateNotPositive = errors.New("wallet: a rate has to be a fraction of two positive integers")

	// ErrRateDenominator is returned when a rate is quoted over a denominator
	// larger than MaxRateDenominator.
	ErrRateDenominator = errors.New("wallet: the rate denominator is larger than a remainder can be recorded against")

	// ErrRateNotQuoted is returned when a rate does not say when it was
	// obtained. A rate with no time cannot be reproduced, and reproducing it is
	// the whole reason it is written down.
	ErrRateNotQuoted = errors.New("wallet: the rate does not say when it was quoted")

	// ErrRatePair is returned when a rate is quoted for currencies other than
	// the two being converted between. Applying it would be applying a number
	// that means something else.
	ErrRatePair = errors.New("wallet: the rate is quoted for another pair of currencies")

	// ErrConversionUnderflow is returned when an amount is worth less than one
	// minor unit of the target. Crediting nothing while debiting something is a
	// movement that takes money and delivers none.
	ErrConversionUnderflow = errors.New("wallet: the amount is worth less than one minor unit of the target")
)

// What a rate provider could not do.
//
// A provider lives outside this process and fails in ways that are not this
// package's: a pair nobody quotes, a service that is down, a moment it has no
// figures for. Those used to travel out as whatever the provider wrote, so an
// application had to match on a sentence to tell "this pair does not exist"
// from "try again in a minute" -- and the two are opposite instructions to
// whoever is waiting.
//
// The values are declared here and not where a provider is written, and that is
// the whole point of them: a caller tests them with errors.Is against this
// package, which it already imports, and never has to import the provider it
// happens to be wired to. A provider wraps the one that fits and adds its own
// sentence; one that wraps none is not wrong, and what it returns travels out
// unclassified rather than being guessed at.
//
// There are five because there are five different things to do about them.
// Their shape is the reference's, which distinguishes the same failures; what is
// not carried over is its split between a failure and the runtime wrapper around
// the same failure, which is one distinction with no different answer.
var (
	// ErrRatePairUnknown is returned when the provider does not quote this pair
	// at all. Nothing about waiting or retrying helps: either the pair is wrong
	// or the provider is the wrong one to ask.
	//
	// It is not ErrRatePair, which is about a rate this package was handed for
	// two other currencies -- that one is wiring inside the process.
	ErrRatePairUnknown = errors.New("wallet: the rate provider does not quote this pair of currencies")

	// ErrRateProviderUnavailable is returned when the provider could not be
	// reached or answered with a failure of its own. It is the one that is worth
	// retrying, and the one that should not be turned into a refusal a customer
	// reads as final.
	ErrRateProviderUnavailable = errors.New("wallet: the rate provider could not be reached")

	// ErrRateMomentUnsupported is returned when the provider cannot quote for
	// the moment it was asked about -- a date before its history, or one it does
	// not publish. The pair exists and the provider is up.
	ErrRateMomentUnsupported = errors.New("wallet: the rate provider has no figures for that moment")

	// ErrRateCacheFailed is returned when a provider that caches its quotes
	// could not read or write its cache. The quote may still be obtainable, so
	// it is told apart from the provider being down: what it names is the
	// provider's own storage rather than the service it fronts.
	ErrRateCacheFailed = errors.New("wallet: the rate provider could not use its cache")

	// ErrRateRequestRefused is returned when the provider refused the request
	// itself: a currency code it cannot parse, a query it does not accept.
	// It is a defect in what was asked rather than in what was answered, so it
	// is fixed in the caller and not waited out.
	ErrRateRequestRefused = errors.New("wallet: the rate provider refused the request")
)

// Rate is one exchange rate, as the exact fraction Numerator/Denominator.
//
// A fraction of two integers and never a float: 5.4321 has no binary
// representation, so a rate held as a float is already a different rate than
// the one somebody quoted, and every amount converted through it inherits the
// difference. Two integers are exactly what was quoted, they multiply and
// divide without loss, and they are what the record holds -- so a conversion
// can be recomputed from the row long after the provider that answered it is
// gone.
//
// The direction is part of the value. A rate that did not name its two
// currencies would be a number that could be applied backwards, and applying a
// rate backwards is a conversion that is wrong by the square of itself.
type Rate struct {
	// From is the currency the rate converts out of.
	From Currency
	// To is the currency it converts into.
	To Currency
	// Numerator is the top of the fraction: how much of To one unit of From is
	// worth, over Denominator.
	Numerator int64
	// Denominator is the bottom of the fraction, and it is positive and at most
	// MaxRateDenominator.
	Denominator int64
	// QuotedAt is when the rate was obtained, in UTC.
	//
	// It is required. A rate is only true of a moment, and a record that does
	// not say which moment is a record nobody can check against anything.
	QuotedAt time.Time
}

// Validate reports why this rate cannot be applied, and nil when it can.
func (r Rate) Validate() error {
	if r.From == "" || r.To == "" {
		return fmt.Errorf("%w: it converts %q into %q", ErrRatePair, r.From, r.To)
	}
	if r.Numerator <= 0 || r.Denominator <= 0 {
		return fmt.Errorf("%w: got %d/%d", ErrRateNotPositive, r.Numerator, r.Denominator)
	}
	if r.Denominator > MaxRateDenominator {
		return fmt.Errorf("%w: got %d, and the largest is %d", ErrRateDenominator, r.Denominator, MaxRateDenominator)
	}
	if r.QuotedAt.IsZero() {
		return fmt.Errorf("%w: %s", ErrRateNotQuoted, r)
	}
	return nil
}

// String writes the rate as the pair and the fraction.
func (r Rate) String() string {
	return fmt.Sprintf("%s/%s %d/%d", r.From, r.To, r.Numerator, r.Denominator)
}

// Converted is what a rate makes of an amount: the money that arrives, and the
// part of it that no minor unit could carry.
//
// The remainder is a fraction of one minor unit of the target, and it is what
// makes the arithmetic checkable from the outside: the money that arrived,
// multiplied back through the denominator, plus the remainder, is exactly the
// money that left multiplied through the numerator. Nothing is lost that the
// value does not say the size of.
type Converted struct {
	// Money is what arrives, already at the target's currency and scale.
	Money Money
	// RemainderNumerator is the leftover, over RemainderDenominator, and it is
	// always smaller than it.
	RemainderNumerator int64
	// RemainderDenominator is what the leftover is a fraction of: one minor
	// unit of the target.
	RemainderDenominator int64
}

// Exact reports that the conversion divided evenly and nothing was left over.
func (c Converted) Exact() bool { return c.RemainderNumerator == 0 }

// Convert applies the rate to an amount and reports what arrives.
//
// The whole calculation is integer arithmetic on exact values. The money that
// leaves is an integer of the source's minor units, the rate is a fraction of
// two integers, and the scales are powers of ten, so what arrives is one
// division: the amount times the numerator times ten to the target's scale,
// over the denominator times ten to the source's scale. The intermediate is
// wider than an int64 and is computed as a big integer, which is exact; only
// the result is narrowed, and a result that does not fit is reported rather
// than wrapped.
//
// The quotient is truncated, which with every operand positive is a truncation
// downward: what arrives is never more than the rate justifies. The division's
// remainder comes back beside it, so the part that could not be credited is a
// number the caller has and not a difference it has to reconstruct.
func (r Rate) Convert(from Money, to Currency, toDecimalPlaces int) (Converted, error) {
	if err := r.Validate(); err != nil {
		return Converted{}, err
	}
	if r.From != from.Currency || r.To != to {
		return Converted{}, fmt.Errorf("%w: the rate converts %s into %s and the money is %s going to %s",
			ErrRatePair, r.From, r.To, from.Currency, to)
	}
	if !ValidDecimalPlaces(from.DecimalPlaces) || !ValidDecimalPlaces(toDecimalPlaces) {
		return Converted{}, fmt.Errorf("%w: got %d and %d", ErrDecimalPlaces, from.DecimalPlaces, toDecimalPlaces)
	}
	if from.Amount <= 0 {
		return Converted{}, ErrAmountNotPositive
	}

	numerator := big.NewInt(int64(from.Amount))
	numerator.Mul(numerator, big.NewInt(r.Numerator))
	numerator.Mul(numerator, powerOfTen(toDecimalPlaces))

	denominator := big.NewInt(r.Denominator)
	denominator.Mul(denominator, powerOfTen(from.DecimalPlaces))
	// The bounds on the scale and on the denominator are what keep this in
	// range, and the check is here rather than left to them so that widening
	// either one fails loudly instead of writing a remainder that wrapped.
	if !denominator.IsInt64() {
		return Converted{}, fmt.Errorf("%w: %d over ten to the %d", ErrRateDenominator, r.Denominator, from.DecimalPlaces)
	}

	remainder := new(big.Int)
	numerator.QuoRem(numerator, denominator, remainder)
	if !numerator.IsInt64() {
		return Converted{}, ErrAmountOverflow
	}

	amount := Amount(numerator.Int64())
	if amount <= 0 {
		return Converted{}, fmt.Errorf("%w: %s at %s", ErrConversionUnderflow, from, r)
	}
	return Converted{
		Money:                Money{Amount: amount, Currency: to, DecimalPlaces: toDecimalPlaces},
		RemainderNumerator:   remainder.Int64(),
		RemainderDenominator: denominator.Int64(),
	}, nil
}

// powerOfTen is ten raised to places, exactly.
func powerOfTen(places int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
}

// RateProvider answers with the rate between two currencies.
//
// It is the seam and not an implementation, and this package ships no
// implementation of it: a rate comes from somewhere outside the process, and a
// package that declares network = false has nowhere to get one. An application
// that moves money between currencies writes the provider it trusts and hands
// it to Config, which is the one place the choice is visible.
//
// It answers with a rate rather than with a converted amount, and that is the
// difference between a conversion somebody can audit and one they cannot. A
// provider that returned the amount would be a provider that did the
// multiplication and the rounding privately: the rate would be gone by the time
// the money moved, the rounding rule would be whatever that provider chose, and
// a statement would say what arrived without saying why. Here the rate is a
// value this package holds, records and applies under one rule, so the row can
// be recomputed from the numbers on it.
//
// The Grant is first because a rate can be a tenant's own -- a negotiated
// corporate rate, a rate table an application sells -- and a provider that
// cannot tell whose rate it is asked for is a provider that answers with
// somebody else's.
type RateProvider interface {
	// Rate returns the rate that converts from into to.
	//
	// It reports an error rather than an approximation when the pair has no
	// rate: money that moved at a rate nobody had is money that has to be
	// unwound by hand.
	//
	// What it reports with should wrap one of the five values above where one
	// fits, so that a caller can tell a pair nobody quotes from a service that
	// is down without reading a sentence. An error that wraps none of them is
	// carried out unchanged rather than guessed at.
	Rate(ctx context.Context, g security.Grant, from, to Currency) (Rate, error)
}
