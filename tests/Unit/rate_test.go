package unit_test

import (
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	wallet "github.com/hyz-is/arandu-wallet"
)

// quoted is the moment every rate below says it was obtained. A rate with no
// time is refused, and one of the tests is about exactly that.
var quoted = time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)

// rate builds a well formed rate for the pair, so the tests below say what they
// are about rather than repeating five fields.
func rate(from, to wallet.Currency, numerator, denominator int64) wallet.Rate {
	return wallet.Rate{From: from, To: to, Numerator: numerator, Denominator: denominator, QuotedAt: quoted}
}

// money is an amount at a currency and a scale.
func money(amount wallet.Amount, currency wallet.Currency, places int) wallet.Money {
	return wallet.Money{Amount: amount, Currency: currency, DecimalPlaces: places}
}

func TestAConversionIsExactIntegerArithmetic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		rate        wallet.Rate
		from        wallet.Money
		toPlaces    int
		want        wallet.Amount
		remainder   int64
		denominator int64
	}{
		{
			// 10.00 at 5.4321 is 54.321, and hundredths cannot hold the last
			// digit.
			name: "a rate that does not divide evenly",
			rate: rate("USD", "BRL", 54321, 10000), from: money(1000, "USD", 2), toPlaces: 2,
			want: 5432, remainder: 100000, denominator: 1000000,
		},
		{
			name: "a rate that divides evenly leaves nothing",
			rate: rate("USD", "BRL", 5, 1), from: money(1000, "USD", 2), toPlaces: 2,
			want: 5000, remainder: 0, denominator: 100,
		},
		{
			// A third has no decimal spelling, and the fraction converts it
			// exactly anyway.
			name: "a rate that has no decimal spelling",
			rate: rate("USD", "BRL", 1, 3), from: money(1000, "USD", 2), toPlaces: 2,
			want: 333, remainder: 100, denominator: 300,
		},
		{
			name: "a target counted more finely gains digits",
			rate: rate("USD", "BRL", 2, 1), from: money(1000, "USD", 2), toPlaces: 4,
			want: 200000, remainder: 0, denominator: 100,
		},
		{
			// 10.5075 into hundredths: three quarters of a cent is left.
			name: "a target counted more coarsely drops them",
			rate: rate("BRL", "BRL", 1, 1), from: money(105075, "BRL", 4), toPlaces: 2,
			want: 1050, remainder: 7500, denominator: 10000,
		},
		{
			// 10.50 at 150.5 is 1580.25, and the yen has no minor unit to hold
			// the quarter.
			name: "a currency with no minor unit at all",
			rate: rate("USD", "JPY", 301, 2), from: money(1050, "USD", 2), toPlaces: 0,
			want: 1580, remainder: 50, denominator: 200,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			converted, err := test.rate.Convert(test.from, test.rate.To, test.toPlaces)
			if err != nil {
				t.Fatalf("converting %s at %s: %v", test.from, test.rate, err)
			}
			if converted.Money.Amount != test.want {
				t.Errorf("converted to %d, want %d", converted.Money.Amount, test.want)
			}
			if converted.Money.Currency != test.rate.To || converted.Money.DecimalPlaces != test.toPlaces {
				t.Errorf("converted to %s at %d places, want %s at %d",
					converted.Money.Currency, converted.Money.DecimalPlaces, test.rate.To, test.toPlaces)
			}
			if converted.RemainderNumerator != test.remainder {
				t.Errorf("the remainder is %d, want %d", converted.RemainderNumerator, test.remainder)
			}
			if converted.RemainderDenominator != test.denominator {
				t.Errorf("the remainder is over %d, want %d", converted.RemainderDenominator, test.denominator)
			}
			if converted.Exact() != (test.remainder == 0) {
				t.Errorf("Exact reports %v with a remainder of %d", converted.Exact(), converted.RemainderNumerator)
			}
			assertIdentity(t, test.rate, test.from, converted)
		})
	}
}

// TestRoundingOnlyEverGoesDown is the property behind every row above.
//
// It is stated once and checked over a range rather than at the six points the
// table happens to name, because the failure it guards against is a rule that
// is right at the values somebody wrote down and wrong in between: rounding to
// the nearest is correct half the time by construction, and the half where it
// is not is the half that credits money nobody funded.
func TestRoundingOnlyEverGoesDown(t *testing.T) {
	t.Parallel()

	converting := rate("USD", "BRL", 54321, 10000)
	for amount := wallet.Amount(1); amount <= 500; amount++ {
		converted, err := converting.Convert(money(amount, "USD", 2), "BRL", 2)
		if err != nil {
			t.Fatalf("converting %d: %v", amount, err)
		}

		// The credit, brought back through the rate, is never more than what
		// left. Anything else would be money the rate did not produce.
		credited := new(big.Int).Mul(big.NewInt(int64(converted.Money.Amount)), big.NewInt(converting.Denominator))
		credited.Mul(credited, big.NewInt(100))
		left := new(big.Int).Mul(big.NewInt(int64(amount)), big.NewInt(converting.Numerator))
		left.Mul(left, big.NewInt(100))
		if credited.Cmp(left) > 0 {
			t.Fatalf("converting %d credited %d, which is worth more than what left", amount, converted.Money.Amount)
		}

		// And crediting one more unit would have been too much, so nothing
		// larger was available to credit.
		next := new(big.Int).Add(big.NewInt(int64(converted.Money.Amount)), big.NewInt(1))
		next.Mul(next, big.NewInt(converting.Denominator))
		next.Mul(next, big.NewInt(100))
		if next.Cmp(left) <= 0 {
			t.Fatalf("converting %d credited %d and one more unit still fits, so a whole unit was dropped",
				amount, converted.Money.Amount)
		}
		assertIdentity(t, converting, money(amount, "USD", 2), converted)
	}
}

func TestARateThatCannotBeAppliedIsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		rate wallet.Rate
		from wallet.Money
		to   wallet.Currency
		want error
	}{
		{
			name: "a rate of zero would convert everything to nothing",
			rate: rate("USD", "BRL", 0, 1), from: money(1000, "USD", 2), to: "BRL",
			want: wallet.ErrRateNotPositive,
		},
		{
			name: "a negative rate would turn a credit into a debit",
			rate: rate("USD", "BRL", -2, 1), from: money(1000, "USD", 2), to: "BRL",
			want: wallet.ErrRateNotPositive,
		},
		{
			name: "a denominator of zero is not a fraction",
			rate: rate("USD", "BRL", 2, 0), from: money(1000, "USD", 2), to: "BRL",
			want: wallet.ErrRateNotPositive,
		},
		{
			name: "a denominator past the bound cannot carry a remainder",
			rate: rate("USD", "BRL", 2, wallet.MaxRateDenominator+1), from: money(1000, "USD", 2), to: "BRL",
			want: wallet.ErrRateDenominator,
		},
		{
			name: "a rate with no time cannot be reproduced",
			rate: wallet.Rate{From: "USD", To: "BRL", Numerator: 2, Denominator: 1},
			from: money(1000, "USD", 2), to: "BRL",
			want: wallet.ErrRateNotQuoted,
		},
		{
			name: "a rate that names no pair",
			rate: wallet.Rate{Numerator: 2, Denominator: 1, QuotedAt: quoted},
			from: money(1000, "USD", 2), to: "BRL",
			want: wallet.ErrRatePair,
		},
		{
			name: "a rate quoted out of another currency",
			rate: rate("JPY", "BRL", 2, 1), from: money(1000, "USD", 2), to: "BRL",
			want: wallet.ErrRatePair,
		},
		{
			name: "a rate quoted into another currency",
			rate: rate("USD", "JPY", 2, 1), from: money(1000, "USD", 2), to: "BRL",
			want: wallet.ErrRatePair,
		},
		{
			name: "an amount of nothing is not a movement",
			rate: rate("USD", "BRL", 2, 1), from: money(0, "USD", 2), to: "BRL",
			want: wallet.ErrAmountNotPositive,
		},
		{
			name: "a negative amount is a movement in the other direction",
			rate: rate("USD", "BRL", 2, 1), from: money(-1000, "USD", 2), to: "BRL",
			want: wallet.ErrAmountNotPositive,
		},
		{
			name: "a scale this package cannot hold",
			rate: rate("USD", "BRL", 2, 1), from: money(1000, "USD", wallet.MaxDecimalPlaces+1), to: "BRL",
			want: wallet.ErrDecimalPlaces,
		},
		{
			name: "an amount worth less than one minor unit",
			rate: rate("USD", "BRL", 1, 10000), from: money(1, "USD", 2), to: "BRL",
			want: wallet.ErrConversionUnderflow,
		},
		{
			name: "a result past the range of an int64",
			rate: rate("USD", "BRL", 1000000, 1), from: money(math.MaxInt64, "USD", 0), to: "BRL",
			want: wallet.ErrAmountOverflow,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			converted, err := test.rate.Convert(test.from, test.to, 2)
			if !errors.Is(err, test.want) {
				t.Fatalf("converting returned %v, want %v", err, test.want)
			}
			if converted.Money.Amount != 0 {
				t.Errorf("a refused conversion still answered with %d", converted.Money.Amount)
			}
		})
	}
}

func TestARateReadsAsThePairAndTheFraction(t *testing.T) {
	t.Parallel()

	if got, want := rate("USD", "BRL", 54321, 10000).String(), "USD/BRL 54321/10000"; got != want {
		t.Fatalf("the rate reads as %q, want %q", got, want)
	}
}

func TestAWellFormedRateValidates(t *testing.T) {
	t.Parallel()

	if err := rate("USD", "BRL", 1, wallet.MaxRateDenominator).Validate(); err != nil {
		t.Fatalf("a rate at the largest denominator was refused: %v", err)
	}
}

// TestTheRecordedRateIsWhatTheConversionWasMadeAt holds the round trip through
// the entity: what a Conversion answers about itself is what it was written
// with, so a reader of the row and a reader of the receipt see one rate.
func TestTheRecordedRateIsWhatTheConversionWasMadeAt(t *testing.T) {
	t.Parallel()

	record := wallet.Conversion{
		FromCurrency: "USD", FromDecimalPlaces: 2, FromAmount: 1000,
		ToCurrency: "BRL", ToDecimalPlaces: 2, ToAmount: 5432,
		RateNumerator: 54321, RateDenominator: 10000, QuotedAt: quoted,
		Rounding: wallet.RoundDown, RemainderNumerator: 100000, RemainderDenominator: 1000000,
	}

	if got, want := record.Rate(), rate("USD", "BRL", 54321, 10000); got != want {
		t.Errorf("the recorded rate is %s, want %s", got, want)
	}
	if got, want := record.From().String(), "10.00 USD"; got != want {
		t.Errorf("what left reads as %q, want %q", got, want)
	}
	if got, want := record.To().String(), "54.32 BRL"; got != want {
		t.Errorf("what arrived reads as %q, want %q", got, want)
	}
	if record.Exact() {
		t.Error("a conversion with a remainder reports itself exact")
	}

	record.RemainderNumerator = 0
	if !record.Exact() {
		t.Error("a conversion with no remainder does not report itself exact")
	}
}

// assertIdentity holds the equation that makes a conversion checkable from the
// outside: what left, through the numerator and up to the target's scale,
// equals what arrived, through the denominator and up to the source's scale,
// plus the remainder. Every term is an integer, so it closes exactly or it does
// not close.
func assertIdentity(t *testing.T, applied wallet.Rate, from wallet.Money, converted wallet.Converted) {
	t.Helper()

	left := new(big.Int).Mul(big.NewInt(int64(from.Amount)), big.NewInt(applied.Numerator))
	left.Mul(left, exp10(converted.Money.DecimalPlaces))

	right := new(big.Int).Mul(big.NewInt(int64(converted.Money.Amount)), big.NewInt(applied.Denominator))
	right.Mul(right, exp10(from.DecimalPlaces))
	right.Add(right, big.NewInt(converted.RemainderNumerator))

	if left.Cmp(right) != 0 {
		t.Errorf("%s at %s gives %s: %s does not equal %s", from, applied, converted.Money, left, right)
	}
	if converted.RemainderNumerator < 0 || converted.RemainderNumerator >= converted.RemainderDenominator {
		t.Errorf("the remainder is %d of %d, and it has to be a part of one minor unit",
			converted.RemainderNumerator, converted.RemainderDenominator)
	}
}

// exp10 is ten raised to places, exactly.
func exp10(places int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
}
