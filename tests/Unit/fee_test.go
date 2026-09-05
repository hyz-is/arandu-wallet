package unit_test

import (
	"errors"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The arithmetic of a fee, without a database under it.
//
// It is the exchange's arithmetic: integers throughout, one division, truncated
// toward zero, and what truncation left recorded as a fraction rather than
// dropped. What is different is the floor and the ceiling, and they are what
// most of this file is about -- a bound that is off by one minor unit is a bound
// nobody notices until the payment it decides is the one somebody complains
// about.

// paid is an amount at two places, which is what most currencies are counted
// in. The currency is never read here: what a fee is a share of is a number, and
// which currency it is counted in is the wallets' business.
func paid(amount wallet.Amount) wallet.Money {
	return money(amount, "BRL", 2)
}

func TestTheZeroScheduleChargesNothingAndAnythingElseHasToBeComplete(t *testing.T) {
	t.Parallel()

	if (wallet.FeeSchedule{}).Charges() {
		t.Error("the zero schedule reports that it charges")
	}

	// Any field set is somebody meaning to charge, and a half-filled schedule
	// is a mistake rather than a fee that quietly comes out as nothing.
	for name, half := range map[string]wallet.FeeSchedule{
		"only a floor":       {Minimum: 50},
		"only a ceiling":     {Maximum: 500},
		"only a destination": {WalletID: "wallet-1"},
		"only a numerator":   {Numerator: 1},
	} {
		if !half.Charges() {
			t.Errorf("a schedule with %s reports that it charges nothing", name)
		}
		if err := half.Validate(); err == nil {
			t.Errorf("a schedule with %s was accepted", name)
		}
	}
}

func TestAScheduleIsAPositiveFractionSmallerThanOne(t *testing.T) {
	t.Parallel()

	for name, broken := range map[string]struct {
		schedule wallet.FeeSchedule
		want     error
	}{
		"a share of nothing":        {wallet.FeeSchedule{Denominator: 100, WalletID: "w"}, wallet.ErrFeeShare},
		"a negative share":          {wallet.FeeSchedule{Numerator: -1, Denominator: 100, WalletID: "w"}, wallet.ErrFeeShare},
		"a share over nothing":      {wallet.FeeSchedule{Numerator: 1, WalletID: "w"}, wallet.ErrFeeShare},
		"the whole payment":         {wallet.FeeSchedule{Numerator: 1, Denominator: 1, WalletID: "w"}, wallet.ErrFeeShare},
		"more than the payment":     {wallet.FeeSchedule{Numerator: 101, Denominator: 100, WalletID: "w"}, wallet.ErrFeeShare},
		"a negative floor":          {wallet.FeeSchedule{Numerator: 1, Denominator: 100, Minimum: -1, WalletID: "w"}, wallet.ErrFeeBounds},
		"a negative ceiling":        {wallet.FeeSchedule{Numerator: 1, Denominator: 100, Maximum: -1, WalletID: "w"}, wallet.ErrFeeBounds},
		"a floor above the ceiling": {wallet.FeeSchedule{Numerator: 1, Denominator: 100, Minimum: 200, Maximum: 100, WalletID: "w"}, wallet.ErrFeeBounds},
		"nowhere to credit":         {wallet.FeeSchedule{Numerator: 1, Denominator: 100}, wallet.ErrFeeWallet},
	} {
		if err := broken.schedule.Validate(); !errors.Is(err, broken.want) {
			t.Errorf("%s returned %v, want %v", name, err, broken.want)
		}
		if _, err := broken.schedule.Fee(paid(10000)); !errors.Is(err, broken.want) {
			t.Errorf("%s charged anyway: %v", name, err)
		}
	}

	// And the smallest schedule that works: one part in the largest
	// denominator, which is a fee of nothing on a small payment and is why a
	// floor exists.
	whole := wallet.FeeSchedule{Numerator: 1, Denominator: 2, WalletID: "w"}
	if err := whole.Validate(); err != nil {
		t.Errorf("half of the payment was refused: %v", err)
	}
}

func TestTheFloorAndTheCeilingDecideAtTheExtremesOfTheShare(t *testing.T) {
	t.Parallel()

	// One per cent, never under fifty and never over five hundred.
	schedule := wallet.FeeSchedule{Numerator: 1, Denominator: 100, Minimum: 50, Maximum: 500, WalletID: "w"}

	for _, want := range []struct {
		payment wallet.Amount
		fee     wallet.Amount
		reason  string
	}{
		{100, 50, "the share is 1 and the floor is 50"},
		{4999, 50, "the share is 49, one under the floor"},
		{5000, 50, "the share is exactly the floor"},
		{5100, 51, "the share is one over the floor and decides"},
		{49999, 499, "the share is one under the ceiling and decides"},
		{50000, 500, "the share is exactly the ceiling"},
		{50100, 500, "the share is one over the ceiling"},
		{1000000, 500, "the share is far over the ceiling"},
	} {
		charged, err := schedule.Fee(paid(want.payment))
		if err != nil {
			t.Errorf("charging %d: %v", want.payment, err)
			continue
		}
		if charged.Amount != want.fee {
			t.Errorf("%d was charged %d, want %d: %s", want.payment, charged.Amount, want.fee, want.reason)
		}
	}

	// A schedule with no bounds is the share, whatever its size.
	unbounded := wallet.FeeSchedule{Numerator: 1, Denominator: 100, WalletID: "w"}
	for payment, fee := range map[wallet.Amount]wallet.Amount{100: 1, 50: 0, 1000000: 10000} {
		charged, err := unbounded.Fee(paid(payment))
		if err != nil {
			t.Errorf("charging %d: %v", payment, err)
			continue
		}
		if charged.Amount != fee {
			t.Errorf("one per cent of %d was charged as %d, want %d", payment, charged.Amount, fee)
		}
	}
}

func TestAShareIsTruncatedAndWhatIsLeftIsReported(t *testing.T) {
	t.Parallel()

	// A third, which no number of cents divides evenly.
	schedule := wallet.FeeSchedule{Numerator: 1, Denominator: 3, WalletID: "w"}
	charged, err := schedule.Fee(paid(1000))
	if err != nil {
		t.Fatalf("charging: %v", err)
	}
	if charged.Amount != 333 {
		t.Errorf("a third of 1000 was charged as %d, want 333", charged.Amount)
	}
	if charged.Exact() {
		t.Error("a third of 1000 reported that it divided evenly")
	}
	if charged.RemainderNumerator != 1 || charged.RemainderDenominator != 3 {
		t.Errorf("what was left is %d over %d, want 1 over 3",
			charged.RemainderNumerator, charged.RemainderDenominator)
	}

	// The identity that makes it checkable: the payment times the numerator is
	// the fee times the denominator plus what was left.
	if left, right := 1000*int64(1), int64(charged.Amount)*3+charged.RemainderNumerator; left != right {
		t.Errorf("%d does not equal %d, so a third of the payment went somewhere nobody can find", left, right)
	}

	// A share that divides evenly leaves nothing, and says so.
	exact, err := (wallet.FeeSchedule{Numerator: 1, Denominator: 4, WalletID: "w"}).Fee(paid(1000))
	if err != nil {
		t.Fatalf("charging: %v", err)
	}
	if exact.Amount != 250 || !exact.Exact() || exact.RemainderNumerator != 0 {
		t.Errorf("a quarter of 1000 is %d with %d left, want 250 with nothing",
			exact.Amount, exact.RemainderNumerator)
	}

	// A bound decided it, so there was nothing to truncate.
	bounded, err := (wallet.FeeSchedule{Numerator: 1, Denominator: 3, Minimum: 500, WalletID: "w"}).Fee(paid(1000))
	if err != nil {
		t.Fatalf("charging: %v", err)
	}
	if bounded.Amount != 500 || !bounded.Exact() {
		t.Errorf("a fee the floor decided is %d with %d left, want 500 with nothing",
			bounded.Amount, bounded.RemainderNumerator)
	}
}

func TestAFeeIsNotChargedOnAMovementOfNothing(t *testing.T) {
	t.Parallel()

	schedule := wallet.FeeSchedule{Numerator: 1, Denominator: 100, WalletID: "w"}
	for _, payment := range []wallet.Amount{0, -1, -10000} {
		if _, err := schedule.Fee(paid(payment)); !errors.Is(err, wallet.ErrAmountNotPositive) {
			t.Errorf("charging %d returned %v, want ErrAmountNotPositive", payment, err)
		}
	}
	if _, err := schedule.Fee(wallet.Money{Amount: 100, Currency: "BRL", DecimalPlaces: 10}); !errors.Is(err, wallet.ErrDecimalPlaces) {
		t.Errorf("charging at a scale past the maximum returned %v, want ErrDecimalPlaces", err)
	}
}
