package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A movement can be written down without counting, and made to count later.
//
// The invariant that used to be "the balance is the sum of the entries" is now
// "the balance is the sum of the entries that settled", and Entry.Signed is
// where the difference lives: a pending row signs itself zero. So the sum over a
// whole ledger is still the balance column, and a page still reads as a running
// balance, which is what every test in this package leans on.
//
// The second quantity is new and is what the reference calls an unconfirmed
// transaction: the amounts of the rows that have not settled. It is money that
// was proposed. It is in the ledger, it is readable, and it is in no balance.

// pendingOf is what a wallet's ledger has proposed and is still waiting on,
// signed by direction. It is the number that is deliberately not in the balance.
//
// A pending row stays pending for ever, because the ledger is appended to and
// never rewritten: what says it is no longer waiting is the confirmation beside
// it, which names the operation it settled. So this reads the operations of the
// statement as well as its entries, which is what a client that wants the same
// number does.
func pendingOf(t *testing.T, service *wallet.WalletService, walletID string) wallet.Amount {
	t.Helper()

	statement := statementOf(t, service, walletID)
	settled := make(map[string]bool, len(statement.Operations))
	for _, operation := range statement.Operations {
		if confirmed := operation.Confirms(); confirmed != "" {
			settled[confirmed] = true
		}
	}

	total := wallet.Amount(0)
	for _, entry := range statement.Entries {
		if entry == nil || bool(entry.Settled) || settled[entry.OperationID] {
			continue
		}
		if entry.Kind == wallet.EntryWithdraw {
			total -= entry.Amount
			continue
		}
		total += entry.Amount
	}
	return total
}

// pend records a movement without letting it count.
func pend(t *testing.T, service *wallet.WalletService, walletID, key, amount string) wallet.Receipt {
	t.Helper()

	receipt, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
		IdempotencyKey: key,
		WalletID:       walletID,
		Amount:         amount,
		Pending:        true,
	})
	if err != nil {
		t.Fatalf("recording a pending deposit of %s: %v", amount, err)
	}
	return receipt
}

// confirm settles a pending operation and answers with whatever the service
// said.
func confirm(t *testing.T, service *wallet.WalletService, actor security.Subject, operationID, key string) (wallet.Receipt, error) {
	t.Helper()

	return service.Confirm(context.Background(), actor, wallet.ConfirmRequest{
		IdempotencyKey: key,
		OperationID:    operationID,
	})
}

func TestAPendingMovementIsRecordedAndCountsForNothing(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	receipt := pend(t, service, account.ID, "later-1", "10.00")
	if !receipt.Pending() {
		t.Fatal("a pending deposit answered that it had settled")
	}
	if len(receipt.Entries) != 1 {
		t.Fatalf("a pending deposit wrote %d entries, want 1", len(receipt.Entries))
	}
	entry := receipt.Entries[0]
	if entry.Settled {
		t.Fatal("the entry a pending deposit wrote says it settled")
	}
	if entry.Amount != 1000 {
		t.Fatalf("the pending entry records %d, want 1000: what was proposed is still readable", entry.Amount)
	}
	if entry.BalanceAfter != 0 {
		t.Fatalf("the pending entry records a balance of %d, want 0", entry.BalanceAfter)
	}
	if entry.Sequence == 0 {
		t.Fatal("the pending entry took no place in the ledger, so the page it is on cannot be read in order")
	}

	// The three numbers that say what happened: the balance did not move, the
	// ledger agrees with it, and what was proposed is countable on its own.
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d after a pending deposit, want 0", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 0 {
		t.Fatalf("the ledger sums to %d, want 0", got)
	}
	if got := pendingOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the ledger has %d waiting, want 1000", got)
	}
}

func TestConfirmingAppendsTheSettlementBesideWhatWasProposed(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	proposed := pend(t, service, account.ID, "later-1", "10.00")

	settled, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-1")
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}
	if settled.Operation.Kind != wallet.OperationConfirmation {
		t.Fatalf("the confirmation was recorded as %q", settled.Operation.Kind)
	}
	if got := settled.Operation.Confirms(); got != proposed.Operation.ID {
		t.Fatalf("the confirmation names %q as what it settled, want %q", got, proposed.Operation.ID)
	}
	if settled.Pending() {
		t.Fatal("the confirmation answered that it is itself waiting")
	}

	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d after the confirmation, want 1000", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the ledger sums to %d, want 1000", got)
	}
	if got := pendingOf(t, service, account.ID); got != 0 {
		t.Fatalf("the ledger still has %d waiting, want 0", got)
	}

	// Nothing already written changed. The row that was proposed is on the
	// record with the identifier it had, still saying it did not count, and the
	// settlement is the row beside it.
	entries := statementOf(t, service, account.ID).Entries
	if len(entries) != 2 {
		t.Fatalf("the ledger holds %d entries, want the proposal and the settlement", len(entries))
	}
	if entries[0].ID != proposed.Entries[0].ID {
		t.Fatal("confirming replaced the entry that was proposed instead of appending beside it")
	}
	if entries[0].Settled {
		t.Fatal("the entry that was proposed was rewritten to say it settled")
	}
	if entries[0].BalanceAfter != 0 {
		t.Fatalf("the proposed entry now records a balance of %d, want the 0 it was written with", entries[0].BalanceAfter)
	}
	if !entries[1].Settled || entries[1].Amount != 1000 || entries[1].Kind != wallet.EntryDeposit {
		t.Fatalf("the settlement is %s of %d, settled %t; want a settled deposit of 1000",
			entries[1].Kind, entries[1].Amount, entries[1].Settled)
	}
	if entries[1].BalanceAfter != 1000 {
		t.Fatalf("the settlement records a balance of %d, want 1000", entries[1].BalanceAfter)
	}
	if entries[1].Sequence <= entries[0].Sequence {
		t.Fatal("the settlement is not after the proposal in the ledger")
	}
}

func TestAnOperationIsConfirmedOnce(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	proposed := pend(t, service, account.ID, "later-1", "10.00")

	if _, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-1"); err != nil {
		t.Fatalf("confirming: %v", err)
	}

	// A second key is a second request, and it is refused.
	if _, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-2"); !errors.Is(err, wallet.ErrAlreadyConfirmed) {
		t.Fatalf("a second confirmation returned %v, want ErrAlreadyConfirmed", err)
	}

	// The same key is the same request, and it answers with what it did.
	replayed, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-1")
	if err != nil {
		t.Fatalf("replaying the confirmation: %v", err)
	}
	if !replayed.Replayed {
		t.Fatal("the replayed confirmation did not say so")
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d after one deposit confirmed twice, want 1000", got)
	}
}

func TestConfirmingWhatHasNothingWaitingIsRefused(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	// An ordinary deposit settled when it was made.
	credited := deposit(t, service, account.ID, "now-1", "10.00")
	if _, err := confirm(t, service, staff(), credited.Operation.ID, "settle-1"); !errors.Is(err, wallet.ErrNotPending) {
		t.Fatalf("confirming a settled deposit returned %v, want ErrNotPending", err)
	}

	// And a confirmation is itself settled, so it has nothing waiting either.
	proposed := pend(t, service, account.ID, "later-1", "5.00")
	settled, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-2")
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}
	if _, err := confirm(t, service, staff(), settled.Operation.ID, "settle-3"); !errors.Is(err, wallet.ErrNotPending) {
		t.Fatalf("confirming a confirmation returned %v, want ErrNotPending", err)
	}

	if _, err := confirm(t, service, staff(), "operation-nobody-wrote", "settle-4"); !errors.Is(err, wallet.ErrNotFound) {
		t.Fatalf("confirming an operation that does not exist returned %v, want ErrNotFound", err)
	}
	if got := balanceOf(t, service, account.ID); got != 1500 {
		t.Fatalf("the balance is %d, want 1500", got)
	}
}

func TestAPendingWithdrawalHoldsNothingAndIsJudgedWhereItMoves(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "10.00")

	proposed, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
		IdempotencyKey: "later-1", WalletID: account.ID, Amount: "10.00", Pending: true,
	})
	if err != nil {
		t.Fatalf("recording a pending withdrawal: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("a pending withdrawal moved the balance to %d, want 1000", got)
	}

	// Nothing is held, so the money is still spendable, and this is the
	// difference from the reference worth knowing about: the balance is judged
	// at the confirmation, which is where it moves.
	if err := withdraw(t, service, staff(), account.ID, "now-1", "10.00", false); err != nil {
		t.Fatalf("the pending withdrawal held the money: %v", err)
	}
	if _, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-1"); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("confirming against an empty wallet returned %v, want ErrInsufficientFunds", err)
	}

	// The refusal wrote nothing: the balance is where the settled withdrawal
	// left it, and the proposal is still waiting.
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d, want 0", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 0 {
		t.Fatalf("the ledger sums to %d, want 0", got)
	}
	if got := pendingOf(t, service, account.ID); got != -1000 {
		t.Fatalf("the ledger has %d waiting, want -1000", got)
	}

	// With the money back it settles, and the guard that refused it is the one
	// that lets it through.
	deposit(t, service, account.ID, "refund", "10.00")
	if _, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-2"); err != nil {
		t.Fatalf("confirming a covered withdrawal: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d after the confirmation, want 0", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 0 {
		t.Fatalf("the ledger sums to %d, want 0", got)
	}
	if got := pendingOf(t, service, account.ID); got != 0 {
		t.Fatalf("the ledger still has %d waiting, want 0", got)
	}
}

func TestWhatIsUndoneIsTheOperationThatMovedTheMoney(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	proposed := pend(t, service, account.ID, "later-1", "10.00")

	// A pending operation moved nothing, so there is nothing to undo. Undoing
	// it would append the opposite of a movement that never happened.
	_, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "undo-1", OperationID: proposed.Operation.ID, Reason: "changed their mind",
	})
	if !errors.Is(err, wallet.ErrNotSettled) {
		t.Fatalf("reversing a pending operation returned %v, want ErrNotSettled", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the refused reversal moved the balance to %d", got)
	}

	settled, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-1")
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}

	// The confirmation is what moved the money, so the confirmation is what is
	// undone, and undoing it takes back exactly what it put in.
	if _, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "undo-2", OperationID: settled.Operation.ID, Reason: "chargeback",
	}); err != nil {
		t.Fatalf("reversing the confirmation: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d after the confirmation was undone, want 0", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 0 {
		t.Fatalf("the ledger sums to %d, want 0", got)
	}
}

func TestAPendingTransferMovesNeitherSideUntilItIsConfirmed(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	source := openWallet(t, service, "user-1", "main", 2)
	target := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, source.ID, "opening", "10.00")

	proposed, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "later-1", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00", Pending: true,
	})
	if err != nil {
		t.Fatalf("recording a pending transfer: %v", err)
	}
	if len(proposed.Entries) != 2 {
		t.Fatalf("a pending transfer wrote %d entries, want 2", len(proposed.Entries))
	}
	for _, entry := range proposed.Entries {
		if entry.Settled {
			t.Fatalf("an entry of a pending transfer on %s says it settled", entry.WalletID)
		}
	}
	if got := balanceOf(t, service, source.ID); got != 1000 {
		t.Fatalf("the source holds %d, want 1000", got)
	}
	if got := balanceOf(t, service, target.ID); got != 0 {
		t.Fatalf("the target holds %d, want 0", got)
	}

	if _, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-1"); err != nil {
		t.Fatalf("confirming the transfer: %v", err)
	}
	if got := balanceOf(t, service, source.ID); got != 600 {
		t.Fatalf("the source holds %d, want 600", got)
	}
	if got := balanceOf(t, service, target.ID); got != 400 {
		t.Fatalf("the target holds %d, want 400", got)
	}
	for _, id := range []string{source.ID, target.ID} {
		if got, want := ledgerOf(t, service, id), balanceOf(t, service, id); got != want {
			t.Errorf("the ledger of %s sums to %d and its balance is %d", id, got, want)
		}
		if got := pendingOf(t, service, id); got != 0 {
			t.Errorf("the ledger of %s still has %d waiting", id, got)
		}
	}
}

func TestAPendingExchangeQuotesItsRateOnceAndSettlesAtIt(t *testing.T) {
	t.Parallel()

	// A provider that answers differently every time, so a confirmation that
	// asked again would settle at a number nobody was told.
	rates := &driftingRate{}
	service := wallet.NewWalletService(database(t), rates, nil, nil)
	source := openIn(t, service, "user-1", "main", "USD", 2)
	target := openIn(t, service, "user-2", "main", "BRL", 2)
	deposit(t, service, source.ID, "opening", "100.00")

	proposed, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "later-1", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "10.00", Pending: true,
	})
	if err != nil {
		t.Fatalf("recording a pending exchange: %v", err)
	}
	if proposed.Conversion == nil {
		t.Fatal("a pending exchange recorded no rate, so what it settles at is whatever is quoted later")
	}
	quoted := *proposed.Conversion
	if rates.Calls() != 1 {
		t.Fatalf("the provider was asked %d times to record one rate", rates.Calls())
	}

	if _, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-1"); err != nil {
		t.Fatalf("confirming the exchange: %v", err)
	}
	if rates.Calls() != 1 {
		t.Fatalf("the provider was asked %d times, and the confirmation applies what was written down", rates.Calls())
	}
	if got := balanceOf(t, service, target.ID); got != quoted.ToAmount {
		t.Fatalf("the target holds %d and the recorded conversion credited %d", got, quoted.ToAmount)
	}
	if got := balanceOf(t, service, source.ID); got != 9000 {
		t.Fatalf("the source holds %d, want 9000", got)
	}
	assertConversionReproduces(t, quoted)
}

func TestConfirmingIsTheHoldersOnTheirOwnMoneyAndNobodyElsesf(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	proposed := pend(t, service, account.ID, "later-1", "10.00")

	// A signed-in stranger is refused on the row, which is where the holder is
	// finally compared.
	if _, err := confirm(t, service, person("user-2"), proposed.Operation.ID, "settle-1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a stranger confirmed somebody else's deposit: got %v, want ErrForbidden", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the refused confirmation moved the balance to %d", got)
	}

	// The holder is answered: they could have made the deposit count when it
	// was made, so refusing them here would refuse something they can already
	// do.
	if _, err := confirm(t, service, person("user-1"), proposed.Operation.ID, "settle-2"); err != nil {
		t.Fatalf("the holder was refused their own pending deposit: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d, want 1000", got)
	}
}
