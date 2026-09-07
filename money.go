package wallet

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// MaxDecimalPlaces is the largest scale a wallet may declare.
//
// Nine, because the amount is an int64 of minor units and the scale is what
// decides how much of that range is left for the major unit: at nine places the
// range still reaches nine billion whole units, and at ten it stops reaching a
// billion. A scale nobody can spend the range of is a scale that trades a real
// limit for digits nothing produces.
const MaxDecimalPlaces = 9

// Amount is a quantity of money in the minor units of the scale it belongs to.
//
// An integer, never a float: 0.1 has no binary representation, so a float sum
// of ten of them is not one, and money that does not add up is a defect that
// only shows on the statement. The scale that says how many minor units make a
// major one is not part of this type -- it belongs to the wallet, which is the
// only place a currency and its scale are decided -- so an Amount alone is a
// count and Money is what a person reads.
//
// Arithmetic is through Add and Sub rather than + and -, because an int64 that
// overflows wraps silently and a balance that wrapped is a balance that changed
// sign.
type Amount int64

// Errors this package returns about an amount. They are distinguishable because
// each one has a different answer: a scale error is a request to fix, an
// overflow is a request to split, and a non-positive amount is a request that
// meant something else.
var (
	// ErrAmountOverflow is returned when an operation would leave the range an
	// int64 of minor units can hold.
	ErrAmountOverflow = errors.New("wallet: the amount does not fit in the range of an int64 of minor units")
	// ErrAmountNotPositive is returned when a movement of money is zero or
	// negative. Direction is the operation's, never the number's: a withdrawal
	// of a negative amount is a deposit nobody authorized.
	ErrAmountNotPositive = errors.New("wallet: the amount has to be greater than zero")
	// ErrAmountScale is returned when a decimal carries more fraction digits
	// than the scale it is being read at.
	ErrAmountScale = errors.New("wallet: the amount carries more fraction digits than the scale allows")
	// ErrAmountSyntax is returned when text is not a decimal number.
	ErrAmountSyntax = errors.New("wallet: the amount is not a decimal number")
	// ErrDecimalPlaces is returned when a scale is outside 0..MaxDecimalPlaces.
	ErrDecimalPlaces = errors.New("wallet: the scale has to be between 0 and 9 places")
)

// Add returns a + b, or ErrAmountOverflow.
func (a Amount) Add(b Amount) (Amount, error) {
	sum := a + b
	// Overflow happened when the operands agree in sign and the result does
	// not. The check is on the result rather than on a comparison against the
	// limit, because the comparison would have to be written differently for
	// each sign.
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrAmountOverflow
	}
	return sum, nil
}

// Sub returns a - b, or ErrAmountOverflow.
//
// It is written out rather than expressed as a.Add(-b), and the reason is the
// one value that has no negative: -MinInt64 does not fit in an int64, so the
// shorthand had to refuse every subtraction of MinInt64 to avoid computing it.
// Two of those are representable and were being refused --
//
//	-1 - MinInt64 = MaxInt64
//	MinInt64 - MinInt64 = 0
//
// -- which is a money primitive answering "does not fit" about results that do.
//
// Overflow is detected the way Add detects it, on the result: it happened when
// the operands differ in sign and the difference does not agree with a. The
// three that really overflow -- 0 - MinInt64, MaxInt64 - (-1) and MinInt64 - 1
// -- still answer ErrAmountOverflow.
func (a Amount) Sub(b Amount) (Amount, error) {
	difference := a - b
	if (a >= 0 && b < 0 && difference < 0) || (a < 0 && b > 0 && difference >= 0) {
		return 0, ErrAmountOverflow
	}
	return difference, nil
}

// Times returns a multiplied by a count, or ErrAmountOverflow.
//
// A count and not another amount: money times money is not money, and the only
// place a quantity multiplies a price is a line of a basket. A count that is
// not positive is refused rather than read as nothing, because a line for none
// of something is a line somebody meant differently.
//
// The product is computed as a wider integer and narrowed once, so a result the
// range cannot hold is reported rather than wrapped -- an int64 that overflows
// wraps silently, and a price that wrapped has changed sign.
func (a Amount) Times(count int) (Amount, error) {
	if count <= 0 {
		return 0, ErrAmountNotPositive
	}
	product := new(big.Int).Mul(big.NewInt(int64(a)), big.NewInt(int64(count)))
	if !product.IsInt64() {
		return 0, ErrAmountOverflow
	}
	return Amount(product.Int64()), nil
}

// Ceiling is the largest balance that can still take a without overflowing.
//
// It is what a guarded credit compares the balance against, inside the
// statement that performs the credit: the check then runs against the value the
// row holds at that moment rather than against one read a moment earlier.
func (a Amount) Ceiling() Amount { return math.MaxInt64 - a }

// ValidDecimalPlaces reports whether a scale can be used.
func ValidDecimalPlaces(places int) bool { return places >= 0 && places <= MaxDecimalPlaces }

// Value writes the amount as the integer the column holds.
//
// Declared rather than left to the driver's reflection so that there is one
// answer to "what reaches the database", and it is an int64.
func (a Amount) Value() (driver.Value, error) { return int64(a), nil }

// Scan reads the amount back, and refuses anything that is not an integer.
//
// A float64 here means the column is not an integer one -- a table built by
// hand, a migration somebody edited, an engine that widened the type. Reading
// it would be the one place a binary float touches money, so it is the one
// place that says no.
func (a *Amount) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*a = 0
		return nil
	case int64:
		*a = Amount(v)
		return nil
	case int:
		*a = Amount(v)
		return nil
	case []byte:
		return a.scanText(string(v))
	case string:
		return a.scanText(v)
	}
	return fmt.Errorf("wallet: the balance column answered with a %T, and money is read as an integer of minor units only", value)
}

// scanText reads an amount an engine answered as text, which is what a decimal
// column does on some drivers. It is the integer spelling that is accepted, not
// a decimal one: the scale lives on the wallet, and a value that arrived with a
// point does not say which scale it was written at.
func (a *Amount) scanText(text string) error {
	parsed, err := parseInteger(strings.TrimSpace(text))
	if err != nil {
		return fmt.Errorf("wallet: the balance column answered %q: %w", text, err)
	}
	*a = parsed
	return nil
}

// ParseAmount reads a decimal written for a scale of places into minor units.
//
// It is the border, and the only one: everything inside this package is minor
// units. "10.50" at two places is 1050, and "10.5" is 1050 as well -- a missing
// digit is a zero, which is what the notation means.
//
// Nothing is rounded. "10.505" at two places is refused with ErrAmountScale
// rather than turned into 1050 or 1051, because a cent that disappears into a
// rounding rule the caller did not choose is a cent nobody can find again. A
// caller who wants rounding does it before this, where the rule is theirs.
func ParseAmount(text string, places int) (Amount, error) {
	if !ValidDecimalPlaces(places) {
		return 0, fmt.Errorf("%w: got %d", ErrDecimalPlaces, places)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return 0, fmt.Errorf("%w: it is empty", ErrAmountSyntax)
	}

	negative := false
	switch text[0] {
	case '-':
		negative, text = true, text[1:]
	case '+':
		text = text[1:]
	}

	whole, fraction, hasPoint := strings.Cut(text, ".")
	if whole == "" && fraction == "" {
		return 0, fmt.Errorf("%w: %q has no digits", ErrAmountSyntax, text)
	}
	if hasPoint && len(fraction) > places {
		return 0, fmt.Errorf("%w: %q has %d of them and the scale is %d", ErrAmountScale, text, len(fraction), places)
	}

	// The digits are joined and read once, so the scaling is a concatenation
	// rather than a multiplication that could overflow before the check below
	// ever runs. The sign goes down with them rather than being applied to the
	// result: the range is one wider below zero, and negating afterwards cannot
	// reach the last value.
	digits := whole + fraction + strings.Repeat("0", places-len(fraction))
	return parseDigits(digits, negative)
}

// parseInteger reads a run of decimal digits, with an optional leading sign,
// refusing anything else and any value the range cannot hold.
func parseInteger(digits string) (Amount, error) {
	negative := false
	if digits != "" && (digits[0] == '-' || digits[0] == '+') {
		negative, digits = digits[0] == '-', digits[1:]
	}
	return parseDigits(digits, negative)
}

// parseDigits reads a run of decimal digits into an Amount of the given sign.
//
// Written out rather than taken from strconv because the two failures have to
// be told apart: text that is not a number is the caller's mistake, and a
// number too large for the range is a limit of this package that the caller has
// to be told about by name.
//
// The accumulation is negative whatever the sign asked for, and that is not a
// trick: the range of an int64 is one wider below zero than above it, so
// building the magnitude positively and negating it at the end cannot reach the
// minimum -- the negation of it is itself. Building negatively reaches both
// ends, and the one value the positive side cannot hold is refused explicitly.
func parseDigits(digits string, negative bool) (Amount, error) {
	if digits == "" {
		return 0, fmt.Errorf("%w: it has no digits", ErrAmountSyntax)
	}

	var value int64
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%w: %q is not a digit", ErrAmountSyntax, string(c))
		}
		digit := int64(c - '0')
		if value < (math.MinInt64+digit)/10 {
			return 0, ErrAmountOverflow
		}
		value = value*10 - digit
	}

	if negative {
		return Amount(value), nil
	}
	if value == math.MinInt64 {
		return 0, ErrAmountOverflow
	}
	return Amount(-value), nil
}

// Format writes the amount as a decimal at this scale.
//
// The inverse of ParseAmount, digit for digit: what Format writes, ParseAmount
// reads back to the same Amount at the same scale.
func (a Amount) Format(places int) string {
	if !ValidDecimalPlaces(places) {
		places = 0
	}

	sign := ""
	value := int64(a)
	if value < 0 {
		sign = "-"
		// The negation is on the string, not on the int64, because
		// -math.MinInt64 is math.MinInt64 again.
		if value == math.MinInt64 {
			return sign + formatMagnitude("9223372036854775808", places)
		}
		value = -value
	}
	return sign + formatMagnitude(fmt.Sprintf("%d", value), places)
}

// formatMagnitude puts the decimal point into a run of digits.
func formatMagnitude(digits string, places int) string {
	if places == 0 {
		return digits
	}
	if len(digits) <= places {
		digits = strings.Repeat("0", places-len(digits)+1) + digits
	}
	return digits[:len(digits)-places] + "." + digits[len(digits)-places:]
}

// Currency is the unit an amount is counted in, as an ISO 4217 code.
//
// A string rather than an enumeration: the set is not this package's to close,
// and a wallet holding a currency this package has never heard of is a wallet
// that works.
type Currency string

// Money is an amount together with what makes it readable: the currency it is
// counted in and the scale its minor units are at.
//
// It exists for the borders -- what a request carried, what a response says,
// what a rate provider is asked about -- and never for arithmetic inside a
// wallet, where the scale is the wallet's own and repeating it on every value
// would be a second place for it to be wrong.
type Money struct {
	// Amount is the quantity, in minor units of DecimalPlaces.
	Amount Amount
	// Currency is what the quantity counts.
	Currency Currency
	// DecimalPlaces is how many minor units make one major unit.
	DecimalPlaces int
}

// String writes the money as the decimal and the currency, separated by a
// space.
func (m Money) String() string {
	return m.Amount.Format(m.DecimalPlaces) + " " + string(m.Currency)
}
