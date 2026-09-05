package unit_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The amount type is where this package decides that money is an integer, and
// these tests are what hold that decision. Every one of them is a rule a
// float64 would break: exact scaling, exact round trips, and an overflow that
// is reported rather than wrapped.

func TestParseAmountScalesExactly(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		text   string
		places int
		want   wallet.Amount
	}{
		{"10.50", 2, 1050},
		{"10.5", 2, 1050},
		{"10", 2, 1000},
		{".5", 2, 50},
		{"0.01", 2, 1},
		{"-0.01", 2, -1},
		{"+7", 0, 7},
		{"7", 0, 7},
		{"0.000000001", 9, 1},
		{"92233720368.54775807", 8, math.MaxInt64},
		{" 1.23 ", 2, 123},
	} {
		got, err := wallet.ParseAmount(c.text, c.places)
		if err != nil {
			t.Errorf("ParseAmount(%q, %d): %v", c.text, c.places, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseAmount(%q, %d) = %d, want %d", c.text, c.places, got, c.want)
		}
	}
}

func TestParseAmountRefusesRatherThanRounds(t *testing.T) {
	t.Parallel()

	// The cent that would disappear here is the whole argument. Rounding is a
	// rule the caller owns, and a package that applied its own would be
	// applying it to somebody else's money.
	for _, text := range []string{"10.505", "0.001", "1.999"} {
		_, err := wallet.ParseAmount(text, 2)
		if !errors.Is(err, wallet.ErrAmountScale) {
			t.Errorf("ParseAmount(%q, 2) = %v, want ErrAmountScale", text, err)
		}
	}
}

func TestParseAmountRefusesWhatIsNotADecimal(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"", "   ", "abc", "1,50", "1.2.3", "1e3", "-", ".", "0x10"} {
		if _, err := wallet.ParseAmount(text, 2); err == nil {
			t.Errorf("ParseAmount(%q, 2) was accepted", text)
		}
	}
	if _, err := wallet.ParseAmount("1.00", 10); !errors.Is(err, wallet.ErrDecimalPlaces) {
		t.Error("a scale past the maximum was accepted")
	}
	if _, err := wallet.ParseAmount("1.00", -1); !errors.Is(err, wallet.ErrDecimalPlaces) {
		t.Error("a negative scale was accepted")
	}
}

func TestParseAmountReportsOverflowRatherThanWrapping(t *testing.T) {
	t.Parallel()

	// One minor unit past the range. A float64 would answer with a number, and
	// the number would be wrong.
	if _, err := wallet.ParseAmount("92233720368.54775808", 8); !errors.Is(err, wallet.ErrAmountOverflow) {
		t.Error("an amount past the range of an int64 was accepted")
	}
	if _, err := wallet.ParseAmount(strings.Repeat("9", 30), 2); !errors.Is(err, wallet.ErrAmountOverflow) {
		t.Error("thirty digits were accepted")
	}
}

func TestFormatIsTheInverseOfParse(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		amount wallet.Amount
		places int
		want   string
	}{
		{1050, 2, "10.50"},
		{1, 2, "0.01"},
		{0, 2, "0.00"},
		{-1, 2, "-0.01"},
		{7, 0, "7"},
		{math.MaxInt64, 8, "92233720368.54775807"},
		{math.MinInt64, 8, "-92233720368.54775808"},
	} {
		got := c.amount.Format(c.places)
		if got != c.want {
			t.Errorf("Amount(%d).Format(%d) = %q, want %q", c.amount, c.places, got, c.want)
			continue
		}
		back, err := wallet.ParseAmount(got, c.places)
		if err != nil {
			t.Errorf("ParseAmount(%q, %d): %v", got, c.places, err)
			continue
		}
		if back != c.amount {
			t.Errorf("%q read back as %d, want %d", got, back, c.amount)
		}
	}
}

func TestArithmeticReportsOverflowRatherThanChangingSign(t *testing.T) {
	t.Parallel()

	if _, err := wallet.Amount(math.MaxInt64).Add(1); !errors.Is(err, wallet.ErrAmountOverflow) {
		t.Error("adding past the maximum was allowed to wrap")
	}
	if _, err := wallet.Amount(math.MinInt64).Add(-1); !errors.Is(err, wallet.ErrAmountOverflow) {
		t.Error("subtracting past the minimum was allowed to wrap")
	}
	if _, err := wallet.Amount(0).Sub(math.MinInt64); !errors.Is(err, wallet.ErrAmountOverflow) {
		t.Error("negating the minimum was allowed to answer itself")
	}

	sum, err := wallet.Amount(1050).Add(950)
	if err != nil || sum != 2000 {
		t.Fatalf("1050 + 950 = %d, %v; want 2000", sum, err)
	}
	difference, err := wallet.Amount(1050).Sub(950)
	if err != nil || difference != 100 {
		t.Fatalf("1050 - 950 = %d, %v; want 100", difference, err)
	}
}

func TestTheCeilingIsWhatStillFits(t *testing.T) {
	t.Parallel()

	amount := wallet.Amount(1050)
	if got := amount.Ceiling(); got != math.MaxInt64-1050 {
		t.Fatalf("Amount(1050).Ceiling() = %d, want %d", got, wallet.Amount(math.MaxInt64-1050))
	}
	// The predicate the credit carries: a balance at the ceiling still takes
	// the amount, and one above it does not.
	if _, err := amount.Ceiling().Add(amount); err != nil {
		t.Fatalf("a balance at the ceiling could not take the amount: %v", err)
	}
	if _, err := (amount.Ceiling() + 1).Add(amount); !errors.Is(err, wallet.ErrAmountOverflow) {
		t.Fatal("a balance above the ceiling took the amount")
	}
}

func TestAnAmountRefusesToBeReadOutOfAFloatColumn(t *testing.T) {
	t.Parallel()

	// The one place a binary float could reach the money, and the one place
	// that says no. A column that answers with a float is a column that is not
	// an integer, whatever the migration in this package created.
	var amount wallet.Amount
	if err := amount.Scan(float64(10.5)); err == nil {
		t.Fatal("an amount was read out of a float64")
	}
	if err := amount.Scan(int64(1050)); err != nil || amount != 1050 {
		t.Fatalf("an amount was not read out of an int64: %d, %v", amount, err)
	}
	if err := amount.Scan([]byte("1050")); err != nil || amount != 1050 {
		t.Fatalf("an amount was not read out of the text an engine may answer with: %d, %v", amount, err)
	}
	if err := amount.Scan("1050.00"); err == nil {
		t.Fatal("a decimal spelling was accepted, and it does not say which scale it was written at")
	}
	if err := amount.Scan(nil); err != nil || amount != 0 {
		t.Fatalf("a null column was not read as zero: %d, %v", amount, err)
	}
}

func TestAnAmountIsWrittenAsAnInteger(t *testing.T) {
	t.Parallel()

	value, err := wallet.Amount(1050).Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if written, ok := value.(int64); !ok || written != 1050 {
		t.Fatalf("Value() = %#v, want int64(1050)", value)
	}
}

func TestMoneyReadsAsTheDecimalAndTheCurrency(t *testing.T) {
	t.Parallel()

	money := wallet.Money{Amount: 1050, Currency: "BRL", DecimalPlaces: 2}
	if got := money.String(); got != "10.50 BRL" {
		t.Fatalf("Money.String() = %q, want %q", got, "10.50 BRL")
	}
}

func TestAnEntrySignsItselfByDirection(t *testing.T) {
	t.Parallel()

	if got := (wallet.Entry{Kind: wallet.EntryDeposit, Amount: 1050}).Signed(); got != 1050 {
		t.Fatalf("a deposit signed itself %d, want 1050", got)
	}
	if got := (wallet.Entry{Kind: wallet.EntryWithdraw, Amount: 1050}).Signed(); got != -1050 {
		t.Fatalf("a withdrawal signed itself %d, want -1050", got)
	}
}

func TestAnOperationNamesWhatItReverses(t *testing.T) {
	t.Parallel()

	// Everything that undoes nothing settles itself, which is what lets one
	// unique index carry the rule that an operation is reversed at most once.
	own := wallet.Operation{ID: "operation-1", Kind: wallet.OperationDeposit, ReversesID: "operation-1"}
	if got := own.Reverses(); got != "" {
		t.Fatalf("a deposit reported that it reverses %q", got)
	}

	undo := wallet.Operation{ID: "operation-2", Kind: wallet.OperationReversal, ReversesID: "operation-1"}
	if got := undo.Reverses(); got != "operation-1" {
		t.Fatalf("a reversal reported that it reverses %q, want operation-1", got)
	}
}
