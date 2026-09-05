package feature_test

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"

	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What a payment costs beyond the money it moves.
//
// The property everything here is about is that a fee neither creates money nor
// loses it: what leaves the payer is what arrives at the receiver plus what
// arrives at the wallet collecting the fee, exactly, in whole minor units. The
// share that could not be divided is on the record as a fraction rather than
// dropped, which is the same arrangement a conversion has.

// scheduled answers one fee schedule for every wallet, and counts how often it
// is asked.
//
// The count is the point as much as the schedule is: a payment that consults the
// provider twice is a payment that could have charged two different fees, and
// the only way to see it from outside is to count.
type scheduled struct {
	schedule wallet.FeeSchedule

	mu    sync.Mutex
	calls int
}

// Fee answers the configured schedule for whatever wallet it is asked about.
func (p *scheduled) Fee(_ context.Context, _ security.Grant, _ wallet.Wallet, _ wallet.Money) (wallet.FeeSchedule, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return p.schedule, nil
}

// Calls is how many times the provider has been asked.
func (p *scheduled) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// discounted answers one discount for every payment, and counts how often it is
// asked.
type discounted struct {
	amount wallet.Amount

	mu    sync.Mutex
	calls int
}

// Discount answers the configured amount for whatever payment it is asked
// about.
func (p *discounted) Discount(_ context.Context, _ security.Grant, _, _ wallet.Wallet, _ wallet.Money) (wallet.Amount, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return p.amount, nil
}

// Calls is how many times the provider has been asked.
func (p *discounted) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// Compile-time proof that both doubles answer the seams this package declares.
var (
	_ wallet.FeeProvider      = (*scheduled)(nil)
	_ wallet.DiscountProvider = (*discounted)(nil)
)

// payment is one transfer, and answers with whatever the service said.
func payment(t *testing.T, service *wallet.WalletService, from, to, key, amount string) (wallet.Receipt, error) {
	t.Helper()

	return service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: key,
		FromWalletID:   from,
		ToWalletID:     to,
		Amount:         amount,
	})
}

// assertNothingIsCreatedOrLost holds the property a fee exists to not break:
// every wallet in the movement together holds what was put into them.
func assertNothingIsCreatedOrLost(t *testing.T, service *wallet.WalletService, funded wallet.Amount, wallets ...string) {
	t.Helper()

	total := wallet.Amount(0)
	for _, id := range wallets {
		balance := balanceOf(t, service, id)
		total += balance
		if ledger := ledgerOf(t, service, id); ledger != balance {
			t.Errorf("the ledger of %s sums to %d and its balance is %d", id, ledger, balance)
		}
	}
	if total != funded {
		t.Errorf("the wallets hold %d together and %d was put into them", total, funded)
	}
}

// assertChargeReproduces recomputes the fee from the row alone.
//
// Every number the arithmetic used is on the record, so the identity closes to
// the last minor unit or it does not close at all -- which is the difference
// between a fee somebody can audit and a number they have to trust. Where a
// bound decided the fee there was nothing to truncate, and the row says which
// bound by carrying both.
func assertChargeReproduces(t *testing.T, charge wallet.Charge) {
	t.Helper()

	if got, want := charge.RequestedAmount-charge.Discount, charge.BaseAmount; got != want {
		t.Errorf("the base is %d and the request less the discount is %d", want, got)
	}
	if charge.FeeAmount == 0 {
		return
	}

	schedule := charge.Schedule()
	share := new(big.Int).Mul(big.NewInt(int64(charge.BaseAmount)), big.NewInt(schedule.Numerator))
	share.Quo(share, big.NewInt(schedule.Denominator))
	exact := wallet.Amount(share.Int64())

	switch {
	case exact < schedule.Minimum:
		if charge.FeeAmount != schedule.Minimum {
			t.Errorf("the share is %d, under the floor of %d, and the fee is %d",
				exact, schedule.Minimum, charge.FeeAmount)
		}
		if !charge.Exact() {
			t.Errorf("a fee the floor decided reports a remainder of %d", charge.RemainderNumerator)
		}
	case schedule.Maximum > 0 && exact > schedule.Maximum:
		if charge.FeeAmount != schedule.Maximum {
			t.Errorf("the share is %d, over the ceiling of %d, and the fee is %d",
				exact, schedule.Maximum, charge.FeeAmount)
		}
		if !charge.Exact() {
			t.Errorf("a fee the ceiling decided reports a remainder of %d", charge.RemainderNumerator)
		}
	default:
		// The share decided it, so the identity has to close: the base times
		// the numerator is the fee times the denominator plus what was left.
		left := new(big.Int).Mul(big.NewInt(int64(charge.BaseAmount)), big.NewInt(schedule.Numerator))
		right := new(big.Int).Mul(big.NewInt(int64(charge.FeeAmount)), big.NewInt(schedule.Denominator))
		right.Add(right, big.NewInt(charge.RemainderNumerator))
		if left.Cmp(right) != 0 {
			t.Errorf("the fee does not reproduce: %d/%d of %s is %s, and %s does not equal %s",
				schedule.Numerator, schedule.Denominator, charge.Base(), charge.Fee(), left, right)
		}
	}

	if charge.RemainderNumerator < 0 {
		t.Errorf("the remainder is %d, and a negative one would mean more was charged than the share produced",
			charge.RemainderNumerator)
	}
	if charge.RemainderNumerator >= charge.RemainderDenominator {
		t.Errorf("the remainder is %d of %d, and a whole minor unit was dropped",
			charge.RemainderNumerator, charge.RemainderDenominator)
	}
	if charge.RemainderDenominator != schedule.Denominator {
		t.Errorf("the remainder is recorded over %d, want the schedule's %d",
			charge.RemainderDenominator, schedule.Denominator)
	}
}

func TestAFeeIsChargedOnTopOfThePaymentAndNothingIsCreatedOrLost(t *testing.T) {
	t.Parallel()

	// Two and a half per cent, with no bounds. The share divides evenly here, so
	// what this test is about is who paid it and where it went.
	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	fees.schedule = wallet.FeeSchedule{Numerator: 25, Denominator: 1000, WalletID: collector.ID}
	deposit(t, service, payer.ID, "opening", "200.00")

	receipt, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if fees.Calls() != 1 {
		t.Fatalf("the provider was asked %d times for one payment", fees.Calls())
	}

	// The payer sends the payment and the fee; the merchant is paid in full.
	if got := balanceOf(t, service, payer.ID); got != 20000-10000-250 {
		t.Errorf("the payer holds %d, want %d", got, 20000-10000-250)
	}
	if got := balanceOf(t, service, merchant.ID); got != 10000 {
		t.Errorf("the merchant holds %d, want 10000", got)
	}
	if got := balanceOf(t, service, collector.ID); got != 250 {
		t.Errorf("the wallet collecting the fee holds %d, want 250", got)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID, collector.ID)

	// What left is what arrived plus the fee, read off the entries themselves
	// rather than off the wallets.
	if len(receipt.Entries) != 3 {
		t.Fatalf("the payment wrote %d entries, want the payer, the merchant and the fee", len(receipt.Entries))
	}
	out, in := wallet.Amount(0), wallet.Amount(0)
	for _, entry := range receipt.Entries {
		if entry.Kind == wallet.EntryWithdraw {
			out += entry.Amount
			continue
		}
		in += entry.Amount
	}
	if out != in {
		t.Errorf("%d left and %d arrived", out, in)
	}

	if receipt.Charge == nil {
		t.Fatal("the payment recorded no charge, so what it cost is a number nobody can find")
	}
	charge := *receipt.Charge
	if charge.RequestedAmount != 10000 || charge.BaseAmount != 10000 || charge.FeeAmount != 250 {
		t.Errorf("the charge records a request of %d, a base of %d and a fee of %d; want 10000, 10000 and 250",
			charge.RequestedAmount, charge.BaseAmount, charge.FeeAmount)
	}
	if charge.FeeWalletID != collector.ID {
		t.Errorf("the charge says the fee went to %s, want %s", charge.FeeWalletID, collector.ID)
	}
	if charge.FeeDeductible {
		t.Error("the charge says the receiver paid the fee")
	}
	if charge.Rounding != wallet.RoundDown {
		t.Errorf("the charge was rounded under %q", charge.Rounding)
	}
	assertChargeReproduces(t, charge)
}

func TestADeductibleFeeComesOutOfWhatArrives(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	fees.schedule = wallet.FeeSchedule{Numerator: 25, Denominator: 1000, Deductible: true, WalletID: collector.ID}
	deposit(t, service, payer.ID, "opening", "200.00")

	receipt, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}

	// The payer sends exactly what they were asked for, and the fee comes out of
	// what the merchant receives. The same three wallets hold the same total.
	if got := balanceOf(t, service, payer.ID); got != 10000 {
		t.Errorf("the payer holds %d, want 10000", got)
	}
	if got := balanceOf(t, service, merchant.ID); got != 10000-250 {
		t.Errorf("the merchant holds %d, want %d", got, 10000-250)
	}
	if got := balanceOf(t, service, collector.ID); got != 250 {
		t.Errorf("the wallet collecting the fee holds %d, want 250", got)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID, collector.ID)

	if receipt.Charge == nil || !receipt.Charge.FeeDeductible {
		t.Fatal("the charge does not say that the receiver paid the fee")
	}
	assertChargeReproduces(t, *receipt.Charge)
}

func TestTheFloorAndTheCeilingDecideAtTheExtremes(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	// One per cent, never under fifty and never over five hundred minor units.
	fees.schedule = wallet.FeeSchedule{
		Numerator: 1, Denominator: 100, Minimum: 50, Maximum: 500, WalletID: collector.ID,
	}
	deposit(t, service, payer.ID, "opening", "10000.00")

	// A payment small enough that one per cent of it is under the floor. The
	// floor is charged, and a fee that rounds to nothing on a small payment is
	// exactly what a floor exists to prevent.
	small, err := payment(t, service, payer.ID, merchant.ID, "pay-small", "1.00")
	if err != nil {
		t.Fatalf("paying a small amount: %v", err)
	}
	if small.Charge.FeeAmount != 50 {
		t.Errorf("one per cent of 100 was charged as %d, want the floor of 50", small.Charge.FeeAmount)
	}
	assertChargeReproduces(t, *small.Charge)

	// A payment large enough that one per cent of it is over the ceiling.
	large, err := payment(t, service, payer.ID, merchant.ID, "pay-large", "1000.00")
	if err != nil {
		t.Fatalf("paying a large amount: %v", err)
	}
	if large.Charge.FeeAmount != 500 {
		t.Errorf("one per cent of 100000 was charged as %d, want the ceiling of 500", large.Charge.FeeAmount)
	}
	assertChargeReproduces(t, *large.Charge)

	// And one between them, where the share itself decides.
	middle, err := payment(t, service, payer.ID, merchant.ID, "pay-middle", "100.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if middle.Charge.FeeAmount != 100 {
		t.Errorf("one per cent of 10000 was charged as %d, want 100", middle.Charge.FeeAmount)
	}
	assertChargeReproduces(t, *middle.Charge)

	assertNothingIsCreatedOrLost(t, service, 1000000, payer.ID, merchant.ID, collector.ID)
	if got := balanceOf(t, service, collector.ID); got != 50+500+100 {
		t.Errorf("the wallet collecting the fees holds %d, want 650", got)
	}
}

func TestWhatTheShareCouldNotDivideIsRecordedRatherThanDropped(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	// A third of the payment, which no number of cents divides evenly.
	fees.schedule = wallet.FeeSchedule{Numerator: 1, Denominator: 3, WalletID: collector.ID}
	deposit(t, service, payer.ID, "opening", "200.00")

	receipt, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "10.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	charge := *receipt.Charge

	// A third of a thousand is 333 and a third. The fee is truncated down, so
	// nothing is charged that the share did not produce, and the third that
	// could not be charged is on the row.
	if charge.FeeAmount != 333 {
		t.Errorf("a third of 1000 was charged as %d, want 333", charge.FeeAmount)
	}
	if charge.Exact() {
		t.Error("a third of 1000 reported that it divided evenly")
	}
	if charge.RemainderNumerator != 1 || charge.RemainderDenominator != 3 {
		t.Errorf("what was left is recorded as %d over %d, want 1 over 3",
			charge.RemainderNumerator, charge.RemainderDenominator)
	}
	assertChargeReproduces(t, charge)
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID, collector.ID)
}

func TestADiscountLowersWhatIsPaidAndTheFeeFollowsIt(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	discounts := &discounted{amount: 1000}
	service := wallet.NewWalletService(database(t), nil, fees, discounts)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	fees.schedule = wallet.FeeSchedule{Numerator: 10, Denominator: 100, WalletID: collector.ID}
	deposit(t, service, payer.ID, "opening", "200.00")

	receipt, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if discounts.Calls() != 1 {
		t.Fatalf("the discount provider was asked %d times for one payment", discounts.Calls())
	}

	// A hundred asked for, ten off, so ninety is paid -- and the fee is ten per
	// cent of the ninety and not of the hundred. A fee charged on a price
	// nobody paid is a fee on money that never moved.
	charge := *receipt.Charge
	if charge.RequestedAmount != 10000 || charge.Discount != 1000 || charge.BaseAmount != 9000 {
		t.Errorf("the charge records %d asked for, %d off and a base of %d; want 10000, 1000 and 9000",
			charge.RequestedAmount, charge.Discount, charge.BaseAmount)
	}
	if charge.FeeAmount != 900 {
		t.Errorf("the fee is %d, want ten per cent of the discounted 9000", charge.FeeAmount)
	}
	if got := balanceOf(t, service, merchant.ID); got != 9000 {
		t.Errorf("the merchant holds %d, want the discounted 9000", got)
	}
	if got := balanceOf(t, service, payer.ID); got != 20000-9000-900 {
		t.Errorf("the payer holds %d, want %d", got, 20000-9000-900)
	}
	assertChargeReproduces(t, charge)
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID, collector.ID)
}

func TestADiscountAloneIsStillRecorded(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, &discounted{amount: 1000})
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, payer.ID, "opening", "200.00")

	receipt, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if receipt.Charge == nil {
		t.Fatal("a discounted payment recorded nothing, so a receipt smaller than the request says nothing about why")
	}
	if receipt.Charge.Discount != 1000 || receipt.Charge.FeeAmount != 0 {
		t.Errorf("the charge records %d off and a fee of %d; want 1000 and 0",
			receipt.Charge.Discount, receipt.Charge.FeeAmount)
	}
	if len(receipt.Entries) != 2 {
		t.Errorf("a payment with no fee wrote %d entries, want 2", len(receipt.Entries))
	}
	if got := balanceOf(t, service, merchant.ID); got != 9000 {
		t.Errorf("the merchant holds %d, want 9000", got)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID)
}

func TestAPaymentWithNoScheduleAndNoDiscountRecordsNothing(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, &discounted{})
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, payer.ID, "opening", "200.00")

	receipt, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if receipt.Charge != nil {
		t.Fatalf("a payment that cost nothing recorded a charge of %d", receipt.Charge.FeeAmount)
	}
	if got := balanceOf(t, service, merchant.ID); got != 10000 {
		t.Errorf("the merchant holds %d, want 10000", got)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID)
}

func TestAFeeNeverCrossesARate(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), &fixedRate{numerator: 5, denominator: 1}, fees, nil)
	payer := openIn(t, service, "user-1", "main", "USD", 2)
	merchant := openIn(t, service, "user-2", "main", "BRL", 2)
	collector := openIn(t, service, "platform", "fees", "BRL", 2)
	fees.schedule = wallet.FeeSchedule{Numerator: 1, Denominator: 100, WalletID: collector.ID}
	deposit(t, service, payer.ID, "opening", "200.00")

	// The two wallets are not counted the same way, so the fee would have to be
	// carried through the rate: a rounding of a number that is already the
	// result of a rounding, which neither side would add up to afterwards.
	_, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if !errors.Is(err, wallet.ErrFeeCurrencyMismatch) {
		t.Fatalf("a fee on an exchange returned %v, want ErrFeeCurrencyMismatch", err)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID, collector.ID)

	// The same refusal when it is the wallet collecting the fee that is counted
	// differently, even though the two ends of the payment agree.
	sameMerchant := openIn(t, service, "user-3", "main", "USD", 2)
	fees.schedule = wallet.FeeSchedule{Numerator: 1, Denominator: 100, WalletID: collector.ID}
	if _, err := payment(t, service, payer.ID, sameMerchant.ID, "pay-2", "100.00"); !errors.Is(err, wallet.ErrFeeCurrencyMismatch) {
		t.Fatalf("a fee credited in another currency returned %v, want ErrFeeCurrencyMismatch", err)
	}
	if got := balanceOf(t, service, payer.ID); got != 20000 {
		t.Errorf("the refused payment moved the payer to %d", got)
	}
}

func TestAFeeNeedsAThirdWalletThatExists(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, payer.ID, "opening", "200.00")

	// A fee that goes back to the payer or to the receiver is not a fee: it is
	// a smaller payment written as one.
	for name, id := range map[string]string{"the payer": payer.ID, "the receiver": merchant.ID} {
		fees.schedule = wallet.FeeSchedule{Numerator: 1, Denominator: 100, WalletID: id}
		if _, err := payment(t, service, payer.ID, merchant.ID, "pay-"+name, "100.00"); !errors.Is(err, wallet.ErrFeeWallet) {
			t.Errorf("a fee credited to %s returned %v, want ErrFeeWallet", name, err)
		}
	}

	fees.schedule = wallet.FeeSchedule{Numerator: 1, Denominator: 100, WalletID: "wallet-nobody-opened"}
	if _, err := payment(t, service, payer.ID, merchant.ID, "pay-missing", "100.00"); !errors.Is(err, wallet.ErrFeeWallet) {
		t.Errorf("a fee credited to a wallet nobody opened returned %v, want ErrFeeWallet", err)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID)
}

func TestAScheduleThatCannotBeAppliedIsRefused(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	deposit(t, service, payer.ID, "opening", "200.00")

	for name, broken := range map[string]struct {
		schedule wallet.FeeSchedule
		want     error
	}{
		"a share of the whole payment": {
			wallet.FeeSchedule{Numerator: 1, Denominator: 1, WalletID: collector.ID}, wallet.ErrFeeShare,
		},
		"a share above one": {
			wallet.FeeSchedule{Numerator: 3, Denominator: 2, WalletID: collector.ID}, wallet.ErrFeeShare,
		},
		"a share of nothing": {
			wallet.FeeSchedule{Minimum: 50, WalletID: collector.ID}, wallet.ErrFeeShare,
		},
		"a floor above the ceiling": {
			wallet.FeeSchedule{Numerator: 1, Denominator: 100, Minimum: 500, Maximum: 50, WalletID: collector.ID},
			wallet.ErrFeeBounds,
		},
		"a negative floor": {
			wallet.FeeSchedule{Numerator: 1, Denominator: 100, Minimum: -1, WalletID: collector.ID},
			wallet.ErrFeeBounds,
		},
		"no wallet to credit": {
			wallet.FeeSchedule{Numerator: 1, Denominator: 100}, wallet.ErrFeeWallet,
		},
	} {
		fees.schedule = broken.schedule
		if _, err := payment(t, service, payer.ID, merchant.ID, "pay-"+name, "100.00"); !errors.Is(err, broken.want) {
			t.Errorf("%s returned %v, want %v", name, err, broken.want)
		}
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID, collector.ID)
}

func TestAPaymentThatWouldArriveAsNothingIsRefused(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	// A floor larger than the payment, taken out of what arrives.
	fees.schedule = wallet.FeeSchedule{
		Numerator: 1, Denominator: 100, Minimum: 500, Deductible: true, WalletID: collector.ID,
	}
	deposit(t, service, payer.ID, "opening", "200.00")

	if _, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "6.00"); err != nil {
		t.Fatalf("a payment the fee leaves something of was refused: %v", err)
	}
	if _, err := payment(t, service, payer.ID, merchant.ID, "pay-2", "5.00"); !errors.Is(err, wallet.ErrFeeExceedsAmount) {
		t.Fatalf("a payment the fee would consume entirely returned %v, want ErrFeeExceedsAmount", err)
	}
	if _, err := payment(t, service, payer.ID, merchant.ID, "pay-3", "4.00"); !errors.Is(err, wallet.ErrFeeExceedsAmount) {
		t.Fatalf("a payment smaller than the fee returned %v, want ErrFeeExceedsAmount", err)
	}
	if got := balanceOf(t, service, merchant.ID); got != 100 {
		t.Errorf("the merchant holds %d, want the 100 left by the first payment", got)
	}
	if got := balanceOf(t, service, collector.ID); got != 500 {
		t.Errorf("the wallet collecting the fee holds %d, want 500", got)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID, collector.ID)
}

func TestADiscountIsNeverNegativeAndNeverTheWholePayment(t *testing.T) {
	t.Parallel()

	discounts := &discounted{amount: -100}
	service := wallet.NewWalletService(database(t), nil, nil, discounts)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, payer.ID, "opening", "200.00")

	// A negative discount is a fee charged through the field that says it is
	// not one.
	if _, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00"); !errors.Is(err, wallet.ErrDiscountNegative) {
		t.Fatalf("a negative discount returned %v, want ErrDiscountNegative", err)
	}

	// And one that takes the whole payment leaves a movement of nothing.
	discounts.amount = 10000
	if _, err := payment(t, service, payer.ID, merchant.ID, "pay-2", "100.00"); !errors.Is(err, wallet.ErrAmountNotPositive) {
		t.Fatalf("a discount of the whole payment returned %v, want ErrAmountNotPositive", err)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID)
}

func TestTheSameKeyPaysOnceAndIsChargedOnce(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	discounts := &discounted{amount: 500}
	service := wallet.NewWalletService(database(t), nil, fees, discounts)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	fees.schedule = wallet.FeeSchedule{Numerator: 1, Denominator: 100, WalletID: collector.ID}
	deposit(t, service, payer.ID, "opening", "200.00")

	first, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	again, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}
	if !again.Replayed {
		t.Fatal("the replayed payment did not say so")
	}

	// The providers were asked for the first call and for nothing since, and
	// the second answer is the first answer read back off the row.
	if fees.Calls() != 1 || discounts.Calls() != 1 {
		t.Errorf("the providers were asked %d and %d times for one payment", fees.Calls(), discounts.Calls())
	}
	if again.Charge == nil {
		t.Fatal("the replayed payment answered with no charge")
	}
	if again.Charge.OperationID != first.Charge.OperationID ||
		again.Charge.FeeAmount != first.Charge.FeeAmount ||
		again.Charge.Discount != first.Charge.Discount {
		t.Errorf("the replay was told a fee of %d and a discount of %d; the first call charged %d and %d",
			again.Charge.FeeAmount, again.Charge.Discount, first.Charge.FeeAmount, first.Charge.Discount)
	}
	if got := balanceOf(t, service, collector.ID); got != first.Charge.FeeAmount {
		t.Errorf("the wallet collecting the fee holds %d after two identical requests, want %d",
			got, first.Charge.FeeAmount)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID, collector.ID)
}

func TestUndoingAPaymentTakesTheFeeBackToo(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	fees.schedule = wallet.FeeSchedule{Numerator: 25, Denominator: 1000, WalletID: collector.ID}
	deposit(t, service, payer.ID, "opening", "200.00")

	paid, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}

	// A reversal mirrors every entry the operation wrote, and the fee was one
	// of them. No rule about fees is needed here: the movement is undone by
	// what it moved.
	if _, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "undo-1", OperationID: paid.Operation.ID, Reason: "chargeback",
	}); err != nil {
		t.Fatalf("undoing the payment: %v", err)
	}
	if got := balanceOf(t, service, payer.ID); got != 20000 {
		t.Errorf("the payer holds %d after the payment was undone, want 20000", got)
	}
	if got := balanceOf(t, service, merchant.ID); got != 0 {
		t.Errorf("the merchant holds %d, want 0", got)
	}
	if got := balanceOf(t, service, collector.ID); got != 0 {
		t.Errorf("the wallet collecting the fee holds %d, want 0", got)
	}
	assertNothingIsCreatedOrLost(t, service, 20000, payer.ID, merchant.ID, collector.ID)
}

func TestAChargedPaymentIsReadableInTheStatement(t *testing.T) {
	t.Parallel()

	fees := &scheduled{}
	service := wallet.NewWalletService(database(t), nil, fees, &discounted{amount: 500})
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	fees.schedule = wallet.FeeSchedule{Numerator: 1, Denominator: 100, WalletID: collector.ID}
	deposit(t, service, payer.ID, "opening", "200.00")

	paid, err := payment(t, service, payer.ID, merchant.ID, "pay-1", "100.00")
	if err != nil {
		t.Fatalf("paying: %v", err)
	}

	// The charge travels with the page for the same reason the rate does: a
	// movement that is smaller than what was asked for says why beside itself
	// rather than in a second request nobody makes.
	statement := statementOf(t, service, merchant.ID)
	charge, ok := statement.Charges[paid.Operation.ID]
	if !ok {
		t.Fatalf("the merchant's statement carries no charge for the payment it was paid by")
	}
	if charge.FeeAmount != paid.Charge.FeeAmount || charge.Discount != paid.Charge.Discount {
		t.Errorf("the statement reports a fee of %d and a discount of %d; the receipt said %d and %d",
			charge.FeeAmount, charge.Discount, paid.Charge.FeeAmount, paid.Charge.Discount)
	}
	assertChargeReproduces(t, charge)

	// And the wallet collecting it sees the same charge on its own page.
	if _, ok := statementOf(t, service, collector.ID).Charges[paid.Operation.ID]; !ok {
		t.Error("the wallet collecting the fee cannot read what it was paid for")
	}

	// The response says the same, beside the items rather than inside them: one
	// charge belongs to one operation, and repeating it on every entry would be
	// repeating one fact until two copies of it could differ.
	beside := wallet.NewEntryCollection(statement, "").With()
	costs, ok := beside["charges"].([]map[string]any)
	if !ok || len(costs) != 1 {
		t.Fatalf("a page of the merchant's ledger answers with %v beside its items, want one charge", beside["charges"])
	}
	if got := costs[0]["fee_minor"]; got != int64(charge.FeeAmount) {
		t.Errorf("the page reports a fee of %v, want %d", got, charge.FeeAmount)
	}
	if got := costs[0]["operation_id"]; got != paid.Operation.ID {
		t.Errorf("the page reports the charge of %v, want the payment %s", got, paid.Operation.ID)
	}

	// A page with nothing charged on it says nothing rather than an empty list.
	plain := openWallet(t, service, "user-9", "main", 2)
	deposit(t, service, plain.ID, "opening", "10.00")
	if _, named := wallet.NewEntryCollection(statementOf(t, service, plain.ID), "").With()["charges"]; named {
		t.Error("a page with nothing charged on it still answers with charges")
	}
}
