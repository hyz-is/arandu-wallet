package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/data"

	wallet "github.com/hyz-is/arandu-wallet"
)

func TestADepositRaisesTheBalanceAndLeavesAnEntry(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	receipt := deposit(t, service, account.ID, "key-1", "10.50")
	if receipt.Replayed {
		t.Fatal("the first deposit reported itself as a replay")
	}
	if len(receipt.Entries) != 1 {
		t.Fatalf("the deposit wrote %d entries, want 1", len(receipt.Entries))
	}
	if got := receipt.Entries[0].Amount; got != 1050 {
		t.Fatalf("the entry recorded %d minor units, want 1050", got)
	}
	if got := receipt.Entries[0].BalanceAfter; got != 1050 {
		t.Fatalf("the entry recorded a balance of %d after it, want 1050", got)
	}
	if got := balanceOf(t, service, account.ID); got != 1050 {
		t.Fatalf("the balance is %d, want 1050", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 1050 {
		t.Fatalf("the ledger sums to %d, want 1050", got)
	}
}

func TestAWithdrawalLowersTheBalanceAndTheLedgerStillExplainsIt(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "key-1", "10.00")

	if _, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
		IdempotencyKey: "key-2", WalletID: account.ID, Amount: "4.25",
	}); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}

	if got := balanceOf(t, service, account.ID); got != 575 {
		t.Fatalf("the balance is %d, want 575", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 575 {
		t.Fatalf("the ledger sums to %d, want 575", got)
	}
}

func TestAWithdrawalBeyondTheBalanceIsRefusedAndWritesNothing(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "key-1", "10.00")

	_, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
		IdempotencyKey: "key-2", WalletID: account.ID, Amount: "10.01",
	})
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("withdrawing more than the balance returned %v, want ErrInsufficientFunds", err)
	}

	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d after a refused withdrawal, want 1000", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the ledger sums to %d after a refused withdrawal, want 1000", got)
	}

	// And the operation rolled back with everything else, so the key is free.
	// A key burned by a request that moved nothing would be a retry the caller
	// could never make.
	if _, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
		IdempotencyKey: "key-2", WalletID: account.ID, Amount: "1.00",
	}); err != nil {
		t.Fatalf("retrying the key of a refused request: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 900 {
		t.Fatalf("the balance is %d, want 900", got)
	}
}

func TestAnAmountWithMorePrecisionThanTheWalletIsRefused(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	_, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
		IdempotencyKey: "key-1", WalletID: account.ID, Amount: "10.505",
	})
	if !errors.Is(err, wallet.ErrAmountScale) {
		t.Fatalf("a third decimal on a two-place wallet returned %v, want ErrAmountScale", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d after a refused deposit, want 0", got)
	}
}

func TestAMovementOfNothingIsRefused(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	for _, amount := range []string{"0", "0.00", "-1.00"} {
		_, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
			IdempotencyKey: "key-" + amount, WalletID: account.ID, Amount: amount,
		})
		if !errors.Is(err, wallet.ErrAmountNotPositive) {
			t.Errorf("depositing %q returned %v, want ErrAmountNotPositive", amount, err)
		}
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d, want 0", got)
	}
}

func TestAWalletIsOpenedOncePerHolderAndSlug(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	openWallet(t, service, "user-1", "main", 2)

	_, err := service.Open(context.Background(), staff(), wallet.OpenRequest{
		HolderID: "user-1", Slug: "main", Name: "Another", Currency: "BRL", DecimalPlaces: 2,
	})
	if !errors.Is(err, wallet.ErrWalletExists) {
		t.Fatalf("opening a second wallet under one slug returned %v, want ErrWalletExists", err)
	}

	// A different slug for the same holder is a different wallet, and the two
	// never mix.
	second := openWallet(t, service, "user-1", "bonus", 2)
	deposit(t, service, second.ID, "key-1", "5.00")
	first, err := service.List(context.Background(), staff(), wallet.ListRequest{HolderID: "user-1"})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("the holder has %d wallets, want 2", len(first))
	}
}

func TestATransferMovesBothBalancesOrNeither(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	source := openWallet(t, service, "user-1", "main", 2)
	target := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	receipt, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	})
	if err != nil {
		t.Fatalf("transferring: %v", err)
	}
	if len(receipt.Entries) != 2 {
		t.Fatalf("the transfer wrote %d entries, want 2", len(receipt.Entries))
	}
	if receipt.Entries[0].Kind != wallet.EntryWithdraw || receipt.Entries[1].Kind != wallet.EntryDeposit {
		t.Fatalf("the receipt reads %s then %s, want withdraw then deposit",
			receipt.Entries[0].Kind, receipt.Entries[1].Kind)
	}
	if got := balanceOf(t, service, source.ID); got != 600 {
		t.Fatalf("the source holds %d, want 600", got)
	}
	if got := balanceOf(t, service, target.ID); got != 400 {
		t.Fatalf("the target holds %d, want 400", got)
	}

	// And a transfer the source cannot cover leaves both sides where they were.
	_, err = service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-3", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "6.01",
	})
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("an uncovered transfer returned %v, want ErrInsufficientFunds", err)
	}
	if got := balanceOf(t, service, source.ID); got != 600 {
		t.Fatalf("the source holds %d after a refused transfer, want 600", got)
	}
	if got := balanceOf(t, service, target.ID); got != 400 {
		t.Fatalf("the target holds %d after a refused transfer, want 400", got)
	}
	if got := ledgerOf(t, service, target.ID); got != 400 {
		t.Fatalf("the target's ledger sums to %d after a refused transfer, want 400", got)
	}
}

func TestATransferNeedsTwoDifferentWallets(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "key-1", "10.00")

	_, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: account.ID, ToWalletID: account.ID, Amount: "1.00",
	})
	if !errors.Is(err, wallet.ErrSameWallet) {
		t.Fatalf("a transfer to itself returned %v, want ErrSameWallet", err)
	}
}

func TestATransferBetweenCurrenciesNeedsARateProvider(t *testing.T) {
	t.Parallel()

	handle := database(t)
	service := wallet.NewWalletService(handle, nil)

	source := openIn(t, service, "user-1", "main", "BRL", 2)
	target := openIn(t, service, "user-2", "main", "USD", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	_, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	})
	if !errors.Is(err, wallet.ErrCurrencyMismatch) {
		t.Fatalf("a cross-currency transfer with no provider returned %v, want ErrCurrencyMismatch", err)
	}

	// With a provider, the rate is applied to what left and the source still
	// loses what it was asked to.
	converted := wallet.NewWalletService(handle, &fixedRate{numerator: 2, denominator: 1})
	if _, err := converted.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-3", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	}); err != nil {
		t.Fatalf("a cross-currency transfer with a provider: %v", err)
	}
	if got := balanceOf(t, converted, source.ID); got != 600 {
		t.Fatalf("the source holds %d, want 600", got)
	}
	if got := balanceOf(t, converted, target.ID); got != 800 {
		t.Fatalf("the target holds %d, want 800 at a rate of two", got)
	}
}

func TestARateQuotedForAnotherPairIsRefused(t *testing.T) {
	t.Parallel()

	handle := database(t)
	service := wallet.NewWalletService(handle, nil)
	source := openIn(t, service, "user-1", "main", "BRL", 2)
	target := openIn(t, service, "user-2", "main", "USD", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	// The provider answers about a pair nobody asked about. Applying it would
	// be applying a number that means something else.
	wrong := wallet.NewWalletService(handle, misquotedRate{from: "JPY", to: "USD"})
	_, err := wrong.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	})
	if !errors.Is(err, wallet.ErrRatePair) {
		t.Fatalf("a rate for another pair returned %v, want ErrRatePair", err)
	}
	if got := balanceOf(t, service, target.ID); got != 0 {
		t.Fatalf("the target holds %d, want 0", got)
	}

	// And one that does not say when it was quoted, which is a rate nothing
	// can be reproduced against.
	undated := wallet.NewWalletService(handle, undatedRate{})
	_, err = undated.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-3", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	})
	if !errors.Is(err, wallet.ErrRateNotQuoted) {
		t.Fatalf("an undated rate returned %v, want ErrRateNotQuoted", err)
	}
	if got := balanceOf(t, service, target.ID); got != 0 {
		t.Fatalf("the target holds %d, want 0", got)
	}

	// A provider with no quote refuses, and its refusal is what the caller
	// sees rather than a rate this package invented.
	missing := wallet.NewWalletService(handle, unavailableRate{})
	_, err = missing.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-4", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	})
	if !errors.Is(err, errRateUnavailable) {
		t.Fatalf("a provider with no quote returned %v, want the provider's own error", err)
	}
	if got := balanceOf(t, service, source.ID); got != 1000 {
		t.Fatalf("the source holds %d, want the untouched 1000", got)
	}
}

func TestAReversalAppendsTheOppositeAndChangesNothingAlreadyWritten(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	source := openWallet(t, service, "user-1", "main", 2)
	target := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, source.ID, "key-1", "10.00")

	moved, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-2", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00",
	})
	if err != nil {
		t.Fatalf("transferring: %v", err)
	}

	undone, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "key-3", OperationID: moved.Operation.ID, Reason: "chargeback",
	})
	if err != nil {
		t.Fatalf("reversing: %v", err)
	}
	if got := undone.Operation.Reverses(); got != moved.Operation.ID {
		t.Fatalf("the reversal names %q as what it undoes, want %q", got, moved.Operation.ID)
	}
	if len(undone.Entries) != 2 {
		t.Fatalf("the reversal wrote %d entries, want 2", len(undone.Entries))
	}

	if got := balanceOf(t, service, source.ID); got != 1000 {
		t.Fatalf("the source holds %d after the reversal, want 1000", got)
	}
	if got := balanceOf(t, service, target.ID); got != 0 {
		t.Fatalf("the target holds %d after the reversal, want 0", got)
	}

	// The original entries are still there, unchanged, and the statement reads
	// as what happened and then what was undone. A status column rewritten in
	// place could not answer this question at all.
	statement, err := service.History(context.Background(), staff(), wallet.HistoryRequest{WalletID: target.ID})
	if err != nil {
		t.Fatalf("reading the statement: %v", err)
	}
	if len(statement.Entries) != 2 {
		t.Fatalf("the target's statement has %d entries, want 2", len(statement.Entries))
	}
	if statement.Entries[0].Kind != wallet.EntryDeposit || statement.Entries[0].Amount != 400 {
		t.Fatalf("the first entry is %s of %d, want a deposit of 400", statement.Entries[0].Kind, statement.Entries[0].Amount)
	}
	if statement.Entries[1].Kind != wallet.EntryWithdraw || statement.Entries[1].Amount != 400 {
		t.Fatalf("the second entry is %s of %d, want a withdrawal of 400", statement.Entries[1].Kind, statement.Entries[1].Amount)
	}
	if statement.Entries[0].OperationID != moved.Operation.ID {
		t.Fatal("the original entry was rewritten to belong to another operation")
	}
	if got := ledgerOf(t, service, target.ID); got != 0 {
		t.Fatalf("the target's ledger sums to %d, want 0", got)
	}
}

func TestAnOperationIsReversedOnce(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)
	credited := deposit(t, service, account.ID, "key-1", "10.00")

	undone, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "key-2", OperationID: credited.Operation.ID, Reason: "duplicate",
	})
	if err != nil {
		t.Fatalf("reversing: %v", err)
	}

	// A second reversal under a new key, which is not a replay: it is a second
	// request to undo a movement that has already been undone.
	_, err = service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "key-3", OperationID: credited.Operation.ID, Reason: "duplicate again",
	})
	if !errors.Is(err, wallet.ErrAlreadyReversed) {
		t.Fatalf("a second reversal returned %v, want ErrAlreadyReversed", err)
	}

	// And a reversal cannot itself be reversed: undoing an undo is a new
	// movement with its own reason, not a second undo of the same one.
	_, err = service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "key-4", OperationID: undone.Operation.ID, Reason: "changed our mind",
	})
	if !errors.Is(err, wallet.ErrNotReversible) {
		t.Fatalf("reversing a reversal returned %v, want ErrNotReversible", err)
	}

	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d, want 0", got)
	}
}

func TestAReversalIsRefusedWhenTheMoneyIsGone(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)
	credited := deposit(t, service, account.ID, "key-1", "10.00")

	if _, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
		IdempotencyKey: "key-2", WalletID: account.ID, Amount: "10.00",
	}); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}

	// A wallet that owes money is a state this package cannot represent, so the
	// reversal is refused rather than driving the balance below zero.
	_, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "key-3", OperationID: credited.Operation.ID, Reason: "chargeback",
	})
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("reversing spent money returned %v, want ErrInsufficientFunds", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d, want 0", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 0 {
		t.Fatalf("the ledger sums to %d, want 0", got)
	}
}

func TestTheStatementPagesThroughTheWholeLedger(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	const movements = 7
	for i := range movements {
		deposit(t, service, account.ID, "key-"+string(rune('a'+i)), "1.00")
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < movements+2; pages++ {
		statement, err := service.History(context.Background(), staff(), wallet.HistoryRequest{
			WalletID: account.ID,
			Query:    data.Query{Cursor: cursor, Limit: 3},
		})
		if err != nil {
			t.Fatalf("reading the statement: %v", err)
		}
		if len(statement.Entries) == 0 {
			break
		}
		for _, entry := range statement.Entries {
			if seen[entry.ID] {
				t.Fatalf("the entry %s came back on two pages", entry.ID)
			}
			seen[entry.ID] = true
		}
		if len(statement.Entries) < 3 {
			break
		}
		cursor = statement.Entries[len(statement.Entries)-1].ID
	}
	if len(seen) != movements {
		t.Fatalf("paging read %d entries, want %d", len(seen), movements)
	}
}
