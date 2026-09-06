package feature_test

import (
	"context"
	"errors"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// Taking a wallet out of service, and the boundary with the freeze.
//
// The two are one gate on the statement and two columns on the row, and the
// reason they are two is that they are lifted by different things. The test
// below is what says so: a package that folded them into one "unavailable"
// column would still pass every other test here, and the first person to look
// at a stopped wallet would not be able to tell an investigation from a
// decision.

func TestAFrozenWalletAndAClosedOneAreRefusedDifferently(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := database(t)
	service := wallet.NewWalletService(handle, nil, nil, nil)

	frozen := openWallet(t, service, "user-1", "frozen", 2)
	deposit(t, service, frozen.ID, "opening-frozen", "10.00")
	diverge(t, handle, frozen.ID, 500)
	if report, err := service.Reconcile(ctx, staff(), frozen.ID); err != nil {
		t.Fatalf("reconciling: %v", err)
	} else if !report.Frozen {
		t.Fatal("the wallet was not frozen")
	}

	closed := openWallet(t, service, "user-2", "closed", 2)
	if _, err := service.Close(ctx, staff(), wallet.CloseRequest{
		WalletID: closed.ID, Reason: "the holder left",
	}); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// Same request, same gate, two answers -- and each names what has to happen
	// next.
	_, frozenErr := service.Deposit(ctx, staff(), wallet.DepositRequest{
		IdempotencyKey: "into-frozen", WalletID: frozen.ID, Amount: "1.00",
	})
	_, closedErr := service.Deposit(ctx, staff(), wallet.DepositRequest{
		IdempotencyKey: "into-closed", WalletID: closed.ID, Amount: "1.00",
	})
	if !errors.Is(frozenErr, wallet.ErrWalletFrozen) {
		t.Errorf("a deposit into a frozen wallet answered %v, want ErrWalletFrozen", frozenErr)
	}
	if !errors.Is(closedErr, wallet.ErrWalletClosed) {
		t.Errorf("a deposit into a closed wallet answered %v, want ErrWalletClosed", closedErr)
	}
	if errors.Is(frozenErr, wallet.ErrWalletClosed) || errors.Is(closedErr, wallet.ErrWalletFrozen) {
		t.Error("the two answers are the same value, so a caller cannot tell an investigation from a decision")
	}

	// And each is lifted by its own thing, and by nothing else.
	if _, err := service.Reopen(ctx, staff(), frozen.ID); !errors.Is(err, wallet.ErrNotFound) {
		t.Errorf("reopening a frozen wallet answered %v, and a freeze is not a closure", err)
	}
	if _, err := service.Rebuild(ctx, staff(), wallet.RebuildRequest{
		IdempotencyKey: "rebuild-closed", WalletID: closed.ID, Reason: "wrong wallet",
	}); !errors.Is(err, wallet.ErrWalletNotFrozen) {
		t.Errorf("rebuilding a closed wallet answered %v, and a closure is not a discrepancy", err)
	}
}

func TestClosingRefusesAWalletThatStillHoldsMoney(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "10.00")

	if _, err := service.Close(ctx, staff(), wallet.CloseRequest{
		WalletID: account.ID, Reason: "consolidated",
	}); !errors.Is(err, wallet.ErrWalletHoldsMoney) {
		t.Fatalf("closing a wallet holding ten answered %v, want ErrWalletHoldsMoney", err)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the refused close moved the balance to %d", got)
	}

	// Emptied, it closes.
	if _, err := service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
		IdempotencyKey: "emptying", WalletID: account.ID, Amount: "10.00",
	}); err != nil {
		t.Fatalf("emptying: %v", err)
	}
	record, err := service.Close(ctx, staff(), wallet.CloseRequest{
		WalletID: account.ID, Reason: "consolidated",
	})
	if err != nil {
		t.Fatalf("closing an empty wallet: %v", err)
	}
	if !record.Closed {
		t.Fatal("the wallet says it is open after being closed")
	}
}

func TestAClosedWalletKeepsItsLedgerAndComesBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "10.00")
	if _, err := service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
		IdempotencyKey: "emptying", WalletID: account.ID, Amount: "10.00",
	}); err != nil {
		t.Fatalf("emptying: %v", err)
	}
	if _, err := service.Close(ctx, staff(), wallet.CloseRequest{
		WalletID: account.ID, Reason: "the holder left",
	}); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// The row is there, the ledger is there, and both are readable. That is the
	// difference between closing and deleting.
	statement, err := service.History(ctx, staff(), wallet.HistoryRequest{WalletID: account.ID})
	if err != nil {
		t.Fatalf("reading a closed wallet's statement: %v", err)
	}
	if len(statement.Entries) != 2 {
		t.Errorf("the closed wallet's ledger holds %d entries, want 2", len(statement.Entries))
	}
	if _, err := service.FindBySlug(ctx, staff(), "user-1", "main"); err != nil {
		t.Errorf("reading a closed wallet by name: %v", err)
	}

	// Closing twice writes once, and says which of the two things happened.
	if _, err := service.Close(ctx, staff(), wallet.CloseRequest{
		WalletID: account.ID, Reason: "again",
	}); !errors.Is(err, wallet.ErrWalletClosed) {
		t.Errorf("closing a closed wallet answered %v, want ErrWalletClosed", err)
	}

	back, err := service.Reopen(ctx, staff(), account.ID)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if back.Closed {
		t.Fatal("the wallet says it is closed after being reopened")
	}
	if _, err := service.Deposit(ctx, staff(), wallet.DepositRequest{
		IdempotencyKey: "after-reopening", WalletID: account.ID, Amount: "1.00",
	}); err != nil {
		t.Fatalf("depositing into a reopened wallet: %v", err)
	}
}

func TestOnlyAnOperatorClosesAWallet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	if _, err := service.Close(ctx, person("user-1"), wallet.CloseRequest{
		WalletID: account.ID, Reason: "mine to close",
	}); err == nil {
		t.Error("a holder closed their own wallet, and what that stops is money other people may be owed")
	}
	if _, err := service.Reopen(ctx, person("user-1"), account.ID); err == nil {
		t.Error("a holder reopened a wallet")
	}
}
