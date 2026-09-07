package unit_test

import (
	"errors"
	"math"
	"math/big"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// TestSubAnswersEveryRepresentableDifference walks the borders of the type
// against an exact oracle.
//
// The oracle is a big.Int, so what the test compares against is the arithmetic
// and not a second copy of the implementation. Every pair whose difference fits
// in an int64 has to be answered with that difference; every pair whose
// difference does not has to answer ErrAmountOverflow, tested with errors.Is
// rather than by comparing a message.
//
// It exists because Sub was written as a.Add(-b), and -MinInt64 does not fit in
// an int64 -- so the shorthand refused every subtraction of MinInt64, including
// -1 - MinInt64 = MaxInt64 and MinInt64 - MinInt64 = 0, which are both
// representable. A money primitive that answers "does not fit" about a result
// that does is a primitive an application works around.
func TestSubAnswersEveryRepresentableDifference(t *testing.T) {
	t.Parallel()

	const (
		min = wallet.Amount(math.MinInt64)
		max = wallet.Amount(math.MaxInt64)
	)
	borders := []wallet.Amount{min, min + 1, -2, -1, 0, 1, 2, max - 1, max}

	minimum := big.NewInt(math.MinInt64)
	maximum := big.NewInt(math.MaxInt64)

	for _, a := range borders {
		for _, b := range borders {
			exact := new(big.Int).Sub(big.NewInt(int64(a)), big.NewInt(int64(b)))
			fits := exact.Cmp(minimum) >= 0 && exact.Cmp(maximum) <= 0

			got, err := a.Sub(b)
			switch {
			case fits && err != nil:
				t.Errorf("%d - %d = %s, which fits, and Sub answered %v", a, b, exact, err)
			case fits && int64(got) != exact.Int64():
				t.Errorf("%d - %d = %d, want %s", a, b, got, exact)
			case !fits && !errors.Is(err, wallet.ErrAmountOverflow):
				t.Errorf("%d - %d = %s, which does not fit, and Sub answered (%d, %v)", a, b, exact, got, err)
			case !fits && got != 0:
				t.Errorf("%d - %d overflowed and answered %d beside the error, want 0", a, b, got)
			}
		}
	}
}

// TestSubNamesTheCasesTheDefectWasAbout is the same claim spelled out, so a
// failure says which case broke rather than which pair of a loop.
func TestSubNamesTheCasesTheDefectWasAbout(t *testing.T) {
	t.Parallel()

	const (
		min = wallet.Amount(math.MinInt64)
		max = wallet.Amount(math.MaxInt64)
	)

	for _, c := range []struct {
		name     string
		a, b     wallet.Amount
		want     wallet.Amount
		overflow bool
	}{
		{name: "minus one less the minimum is the maximum", a: -1, b: min, want: max},
		{name: "the minimum less itself is nothing", a: min, b: min, want: 0},
		{name: "the minimum less the maximum does not fit", a: min, b: max, overflow: true},
		{name: "nothing less the minimum does not fit", a: 0, b: min, overflow: true},
		{name: "the maximum less minus one does not fit", a: max, b: -1, overflow: true},
		{name: "the minimum less one does not fit", a: min, b: 1, overflow: true},
		{name: "the maximum less itself is nothing", a: max, b: max, want: 0},
		{name: "an ordinary difference is ordinary", a: 2000, b: 500, want: 1500},
		{name: "a difference below zero is allowed, because a wallet may be", a: 500, b: 2000, want: -1500},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, err := c.a.Sub(c.b)
			if c.overflow {
				if !errors.Is(err, wallet.ErrAmountOverflow) {
					t.Fatalf("%d - %d answered (%d, %v), want ErrAmountOverflow", c.a, c.b, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%d - %d answered %v, and the result fits", c.a, c.b, err)
			}
			if got != c.want {
				t.Fatalf("%d - %d = %d, want %d", c.a, c.b, got, c.want)
			}
		})
	}
}
