package feature_test

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/arandu-io/framework/data"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What an exchange has to be true of, proved against a database.
//
// Four properties, and each one was written because its absence would be
// invisible. A rate read twice still produces a receipt and a ledger that look
// right on their own. A rounding that credits upward still balances every
// wallet. An exchange recorded as a transfer still moves the right money. An
// idempotency key that converts twice still leaves a statement that adds up.
// None of the four is visible in an amount; all four are visible in the row
// the conversion writes, which is why the row exists.

// TestTheRateIsQuotedOnceAndTheWholeOperationUsesThatOne is the first of them.
//
// The provider answers a different rate on every call, which is what a real
// one does: a quote is true of a moment. So an operation that consults it twice
// multiplies by one number and records another, and the two are both plausible
// -- there is no amount you could look at and say which is wrong. Counting the
// calls is the only way to see it, and the recorded rate reproducing the
// credited amount is the only way to prove the two halves agreed.
func TestTheRateIsQuotedOnceAndTheWholeOperationUsesThatOne(t *testing.T) {
	t.Parallel()

	rates := &driftingRate{}
	service := wallet.NewWalletService(database(t), rates)

	source := openIn(t, service, "user-1", "main", "USD", 2)
	target := openIn(t, service, "user-2", "main", "BRL", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	receipt, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	})
	if err != nil {
		t.Fatalf("the exchange: %v", err)
	}

	if got := rates.Calls(); got != 1 {
		t.Fatalf("the provider was asked %d times, want exactly 1: a second quote is a second rate in one operation", got)
	}

	if receipt.Conversion == nil {
		t.Fatal("an exchange settled with no rate recorded, so nothing says what it was worth")
	}
	if got := receipt.Conversion.RateNumerator; got != rates.First() {
		t.Fatalf("the recorded numerator is %d, want the first quote %d", got, rates.First())
	}

	// The money that moved has to be the money that rate produces. If the
	// arithmetic used one quote and the record kept another, this is where the
	// two stop agreeing.
	if got := balanceOf(t, service, target.ID); got != wallet.Amount(400*rates.First()) {
		t.Fatalf("the target holds %d, want %d at the first quote", got, wallet.Amount(400*rates.First()))
	}
	assertConversionReproduces(t, *receipt.Conversion)

	// And the rate on the record is the rate on the wallets, read back from
	// storage rather than from the value the call returned.
	statement := statementOf(t, service, target.ID)
	stored, ok := statement.Conversions[receipt.Operation.ID]
	if !ok {
		t.Fatal("the statement carries no rate for the exchange that credited it")
	}
	if stored.RateNumerator != receipt.Conversion.RateNumerator ||
		stored.RateDenominator != receipt.Conversion.RateDenominator {
		t.Fatalf("the stored rate is %s and the receipt said %s", stored.Rate(), receipt.Conversion.Rate())
	}
}

// TestRoundingCreditsNoMoreThanTheRateAndRecordsWhatIsLeft is the second.
//
// The amount is chosen so the division does not come out whole: ten dollars at
// 5.4321 is 54.321, and a wallet counted in hundredths cannot hold the last
// digit. Something has to happen to it, and the two failures are opposite --
// rounding up credits money no rate produced, and dropping it silently leaves a
// tenth of a cent that no row in the database accounts for.
//
// What this holds is that neither happened: the credit is the truncated value,
// each wallet's ledger still adds up to its balance, and the part that could
// not be credited is on the conversion as an exact fraction, so the identity
// between the two sides closes to the last unit.
func TestRoundingCreditsNoMoreThanTheRateAndRecordsWhatIsLeft(t *testing.T) {
	t.Parallel()

	// 5.4321, as the fraction it is.
	service := wallet.NewWalletService(database(t), &fixedRate{numerator: 54321, denominator: 10000})

	source := openIn(t, service, "user-1", "main", "USD", 2)
	target := openIn(t, service, "user-2", "main", "BRL", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	receipt, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "10.00",
	})
	if err != nil {
		t.Fatalf("the exchange: %v", err)
	}
	if receipt.Conversion == nil {
		t.Fatal("an exchange settled with no rate recorded")
	}

	conversion := *receipt.Conversion
	if conversion.Rounding != wallet.RoundDown {
		t.Fatalf("the conversion was rounded %q, want %q", conversion.Rounding, wallet.RoundDown)
	}
	// 10.00 at 5.4321 is 54.321, and the wallet counts hundredths.
	if conversion.ToAmount != 5432 {
		t.Fatalf("the credit is %d, want 5432: truncated, never rounded up", conversion.ToAmount)
	}
	if conversion.Exact() {
		t.Fatal("the conversion reports itself exact, and 54.321 does not fit in hundredths")
	}
	if conversion.RemainderNumerator != 100000 || conversion.RemainderDenominator != 1000000 {
		t.Fatalf("the remainder is %d/%d, want 100000/1000000",
			conversion.RemainderNumerator, conversion.RemainderDenominator)
	}

	// Nothing was created: the credit multiplied back is not more than what
	// left, and nothing was lost: the difference is exactly the recorded
	// remainder.
	assertConversionReproduces(t, conversion)

	// And each ledger still explains its own balance. The two are in different
	// currencies and cannot be added to each other -- there is no unit they
	// would be added in -- so this is the sum that has to close, and it closes
	// on both sides.
	for _, held := range []struct {
		name   string
		id     string
		amount wallet.Amount
	}{
		{"the source", source.ID, 0},
		{"the target", target.ID, 5432},
	} {
		if got := balanceOf(t, service, held.id); got != held.amount {
			t.Errorf("%s holds %d, want %d", held.name, got, held.amount)
		}
		if got := ledgerOf(t, service, held.id); got != held.amount {
			t.Errorf("%s ledger sums to %d and its balance is %d", held.name, got, held.amount)
		}
	}
}

// TestAnExchangeIsDistinguishableFromATransferInTheStatement is the third.
//
// Two movements of the same shape, on the same wallet, an instant apart: one
// moved money between wallets counted the same way and one went through a rate.
// A ledger where those read identically is a ledger where somebody was charged
// a conversion and the statement does not say so, which is the thing that
// cannot be reconstructed afterwards from the amounts.
func TestAnExchangeIsDistinguishableFromATransferInTheStatement(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), &fixedRate{numerator: 2, denominator: 1})

	source := openIn(t, service, "user-1", "main", "USD", 2)
	sibling := openIn(t, service, "user-2", "main", "USD", 2)
	foreign := openIn(t, service, "user-3", "main", "BRL", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	plain, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: sibling.ID, Amount: "1.00",
	})
	if err != nil {
		t.Fatalf("the transfer: %v", err)
	}
	crossed, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-3", FromWalletID: source.ID, ToWalletID: foreign.ID, Amount: "1.00",
	})
	if err != nil {
		t.Fatalf("the exchange: %v", err)
	}

	if plain.Operation.Kind != wallet.OperationTransfer {
		t.Fatalf("a same-currency move was recorded as %q, want %q", plain.Operation.Kind, wallet.OperationTransfer)
	}
	if crossed.Operation.Kind != wallet.OperationExchange {
		t.Fatalf("a cross-currency move was recorded as %q, want %q", crossed.Operation.Kind, wallet.OperationExchange)
	}
	if plain.Conversion != nil {
		t.Fatal("a same-currency transfer recorded a rate, and no rate was applied to it")
	}
	if crossed.Conversion == nil {
		t.Fatal("an exchange recorded no rate")
	}

	// Both wrote the same pair of entries on the same wallet for the same
	// amount. What tells them apart is the operation each was written under,
	// and the statement carries it.
	statement := statementOf(t, service, source.ID)
	kinds := map[string]wallet.OperationKind{}
	for _, entry := range statement.Entries {
		kinds[entry.OperationID] = statement.Operations[entry.OperationID].Kind
	}
	if got := kinds[plain.Operation.ID]; got != wallet.OperationTransfer {
		t.Errorf("the statement reads the transfer as %q", got)
	}
	if got := kinds[crossed.Operation.ID]; got != wallet.OperationExchange {
		t.Errorf("the statement reads the exchange as %q", got)
	}

	if _, found := statement.Conversions[plain.Operation.ID]; found {
		t.Error("the statement carries a rate for a transfer that used none")
	}
	rate, found := statement.Conversions[crossed.Operation.ID]
	if !found {
		t.Fatal("the statement carries no rate for the exchange")
	}
	if rate.FromCurrency != "USD" || rate.ToCurrency != "BRL" {
		t.Errorf("the recorded pair is %s/%s, want USD/BRL", rate.FromCurrency, rate.ToCurrency)
	}
	assertConversionReproduces(t, rate)
}

// TestTheSameKeyExchangesOnceAtOneRate is the fourth.
//
// Idempotency has an extra edge on an exchange. A retried deposit that ran
// twice would show up as a doubled balance; a retried exchange that ran twice
// under a rate that moved would settle at whatever the second quote said, and
// the receipt would be well formed either way. So the property is not only that
// the money moves once: it is that the second answer is the first answer,
// including the rate.
func TestTheSameKeyExchangesOnceAtOneRate(t *testing.T) {
	t.Parallel()

	rates := &driftingRate{}
	service := wallet.NewWalletService(database(t), rates)

	source := openIn(t, service, "user-1", "main", "USD", 2)
	target := openIn(t, service, "user-2", "main", "BRL", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	request := wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	}
	first, err := service.Transfer(context.Background(), staff(), request)
	if err != nil {
		t.Fatalf("the first exchange: %v", err)
	}
	second, err := service.Transfer(context.Background(), staff(), request)
	if err != nil {
		t.Fatalf("the retry: %v", err)
	}

	if !second.Replayed {
		t.Fatal("the retry did not report itself as a replay, so it ran a second exchange")
	}
	if first.Operation.ID != second.Operation.ID {
		t.Fatalf("the retry settled as operation %s and the first was %s", second.Operation.ID, first.Operation.ID)
	}
	if got := rates.Calls(); got != 1 {
		t.Fatalf("the provider was asked %d times across two calls, want 1: a replay quotes nothing", got)
	}

	if second.Conversion == nil {
		t.Fatal("the replayed receipt carries no rate, so a retry cannot be reconciled with the first answer")
	}
	if second.Conversion.RateNumerator != first.Conversion.RateNumerator ||
		second.Conversion.RateDenominator != first.Conversion.RateDenominator ||
		!second.Conversion.QuotedAt.Equal(first.Conversion.QuotedAt) {
		t.Fatalf("the replay answered %s and the first call was %s", second.Conversion.Rate(), first.Conversion.Rate())
	}
	if second.Conversion.ToAmount != first.Conversion.ToAmount {
		t.Fatalf("the replay credited %d and the first call credited %d", second.Conversion.ToAmount, first.Conversion.ToAmount)
	}

	// And the money moved exactly once.
	if got := balanceOf(t, service, source.ID); got != 600 {
		t.Fatalf("the source holds %d, want 600", got)
	}
	if got := balanceOf(t, service, target.ID); got != wallet.Amount(400*rates.First()) {
		t.Fatalf("the target holds %d, want %d", got, wallet.Amount(400*rates.First()))
	}
	if got := ledgerOf(t, service, target.ID); got != balanceOf(t, service, target.ID) {
		t.Fatalf("the target ledger sums to %d and its balance is %d", got, balanceOf(t, service, target.ID))
	}
	// One conversion, and only one, for one operation.
	statement := statementOf(t, service, target.ID)
	if got := len(statement.Conversions); got != 1 {
		t.Fatalf("the target's statement carries %d rates, want 1", got)
	}
}

// TestUndoingAnExchangeMovesBackWhatMovedAndQuotesNothing holds the other half
// of a conversion: what happens when it has to be taken back.
//
// A reversal that asked for a rate would convert again, at a rate that is not
// the one the exchange was made at, and one of the two wallets would end up
// short by the difference. The amounts on the entries are what moved, so they
// are what moves back.
func TestUndoingAnExchangeMovesBackWhatMovedAndQuotesNothing(t *testing.T) {
	t.Parallel()

	rates := &driftingRate{}
	service := wallet.NewWalletService(database(t), rates)

	source := openIn(t, service, "user-1", "main", "USD", 2)
	target := openIn(t, service, "user-2", "main", "BRL", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	exchanged, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	})
	if err != nil {
		t.Fatalf("the exchange: %v", err)
	}

	undone, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "key-3", OperationID: exchanged.Operation.ID, Reason: "charged in error",
	})
	if err != nil {
		t.Fatalf("undoing the exchange: %v", err)
	}

	if undone.Operation.Kind != wallet.OperationReversal {
		t.Fatalf("the reversal was recorded as %q", undone.Operation.Kind)
	}
	if undone.Conversion != nil {
		t.Fatal("a reversal recorded a rate, and it converted nothing")
	}
	if got := rates.Calls(); got != 1 {
		t.Fatalf("the provider was asked %d times, want 1: undoing an exchange quotes nothing", got)
	}

	if got := balanceOf(t, service, source.ID); got != 1000 {
		t.Fatalf("the source holds %d after the reversal, want the 1000 it started with", got)
	}
	if got := balanceOf(t, service, target.ID); got != 0 {
		t.Fatalf("the target holds %d after the reversal, want 0", got)
	}
	for _, id := range []string{source.ID, target.ID} {
		if got, want := ledgerOf(t, service, id), balanceOf(t, service, id); got != want {
			t.Errorf("the ledger of %s sums to %d and its balance is %d", id, got, want)
		}
	}
}

// TestTheSameCurrencyAtAnotherScaleIsAlsoAnExchange holds the boundary of what
// counts as a conversion.
//
// Nothing about the currency changed, but the digits moved and the division
// need not come out whole. Calling it a plain transfer would be calling a
// rounding a transfer, and there would be no row saying what was dropped.
func TestTheSameCurrencyAtAnotherScaleIsAlsoAnExchange(t *testing.T) {
	t.Parallel()

	// One to one: the currency is the same, only the scale moves.
	service := wallet.NewWalletService(database(t), &fixedRate{numerator: 1, denominator: 1})

	fine := openIn(t, service, "user-1", "fine", "BRL", 4)
	coarse := openIn(t, service, "user-1", "coarse", "BRL", 2)
	deposit(t, service, fine.ID, "key-1", "10.0000")

	receipt, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: fine.ID, ToWalletID: coarse.ID, Amount: "1.0575",
	})
	if err != nil {
		t.Fatalf("the rescale: %v", err)
	}
	if receipt.Operation.Kind != wallet.OperationExchange {
		t.Fatalf("a rescale was recorded as %q, want %q", receipt.Operation.Kind, wallet.OperationExchange)
	}
	if receipt.Conversion == nil {
		t.Fatal("a rescale recorded no conversion, so nothing says what the rounding dropped")
	}
	// 1.0575 at four places is 1.05 at two, and three quarters of a cent is
	// left over.
	if got := receipt.Conversion.ToAmount; got != 105 {
		t.Fatalf("the credit is %d, want 105", got)
	}
	if receipt.Conversion.Exact() {
		t.Fatal("the rescale reports itself exact, and 1.0575 does not fit in hundredths")
	}
	assertConversionReproduces(t, *receipt.Conversion)
}

// TestAnAmountWorthLessThanOneMinorUnitIsRefused holds the floor of the
// rounding rule.
//
// Truncation takes a small enough amount to nothing, and a movement that debits
// something and credits nothing is a payment that takes money and delivers
// none. It is refused rather than settled, so there is no operation to explain
// afterwards.
func TestAnAmountWorthLessThanOneMinorUnitIsRefused(t *testing.T) {
	t.Parallel()

	// A hundredth of a cent per cent.
	service := wallet.NewWalletService(database(t), &fixedRate{numerator: 1, denominator: 10000})

	source := openIn(t, service, "user-1", "main", "USD", 2)
	target := openIn(t, service, "user-2", "main", "BRL", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	_, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "0.01",
	})
	if !errors.Is(err, wallet.ErrConversionUnderflow) {
		t.Fatalf("an amount worth nothing returned %v, want ErrConversionUnderflow", err)
	}
	if got := balanceOf(t, service, source.ID); got != 1000 {
		t.Fatalf("the source holds %d, want the untouched 1000", got)
	}
	if got := balanceOf(t, service, target.ID); got != 0 {
		t.Fatalf("the target holds %d, want 0", got)
	}
}

// assertConversionReproduces checks the identity that makes a recorded rate
// worth recording.
//
// The money that left, multiplied through the numerator and up to the target's
// scale, has to equal the money that arrived multiplied through the denominator
// and up to the source's scale, plus the remainder. Everything in it is an
// integer and the arithmetic is exact, so it closes to the last unit or it does
// not close at all -- which is the difference between a conversion somebody can
// audit and a number they have to trust.
//
// It also holds the direction of the rounding: the remainder is never negative
// and never reaches a whole minor unit, which together say the credit was
// truncated rather than rounded up and that a whole unit was not dropped.
func assertConversionReproduces(t *testing.T, conversion wallet.Conversion) {
	t.Helper()

	left := new(big.Int).Mul(
		big.NewInt(int64(conversion.FromAmount)),
		big.NewInt(conversion.RateNumerator),
	)
	left.Mul(left, tenTo(conversion.ToDecimalPlaces))

	right := new(big.Int).Mul(
		big.NewInt(int64(conversion.ToAmount)),
		big.NewInt(conversion.RateDenominator),
	)
	right.Mul(right, tenTo(conversion.FromDecimalPlaces))
	right.Add(right, big.NewInt(conversion.RemainderNumerator))

	if left.Cmp(right) != 0 {
		t.Errorf("the conversion does not reproduce: %s at %s credits %s, and %s does not equal %s",
			conversion.From(), conversion.Rate(), conversion.To(), left, right)
	}

	if conversion.RemainderNumerator < 0 {
		t.Errorf("the remainder is %d, and a negative one would mean more was credited than the rate produced",
			conversion.RemainderNumerator)
	}
	if conversion.RemainderNumerator >= conversion.RemainderDenominator {
		t.Errorf("the remainder is %d of %d, and a whole minor unit was dropped",
			conversion.RemainderNumerator, conversion.RemainderDenominator)
	}

	// The denominator the remainder is a fraction of is the rate's denominator
	// scaled to the source. A row where it is anything else is a row whose
	// remainder means something other than it says.
	want := new(big.Int).Mul(big.NewInt(conversion.RateDenominator), tenTo(conversion.FromDecimalPlaces))
	if want.Cmp(big.NewInt(conversion.RemainderDenominator)) != 0 {
		t.Errorf("the remainder is recorded over %d, want %s", conversion.RemainderDenominator, want)
	}
}

// tenTo is ten raised to places, exactly.
func tenTo(places int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
}

// statementOf reads a wallet's whole ledger with the operations behind it.
func statementOf(t *testing.T, service *wallet.WalletService, walletID string) wallet.Statement {
	t.Helper()

	statement, err := service.History(context.Background(), staff(), wallet.HistoryRequest{
		WalletID: walletID,
		Query:    data.Query{Limit: 200},
	})
	if err != nil {
		t.Fatalf("reading the statement of %s: %v", walletID, err)
	}
	return statement
}
