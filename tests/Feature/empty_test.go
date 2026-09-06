package feature_test

import (
	"context"
	"errors"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// TestAnEmptyWalletIsToldApartFromOneThatIsMerelyShort holds the finer answer.
//
// The two are different things for a caller to do: an empty wallet is topped up
// and a short one is asked for a smaller amount. They are read off the row the
// refusing statement matched nothing on, so the answer costs no extra
// statement -- and the empty one wraps the other, so a caller that only asks
// whether the money was there is answered exactly as it always was.
func TestAnEmptyWalletIsToldApartFromOneThatIsMerelyShort(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)

	empty := openWallet(t, service, "user-1", "empty", 2)
	short := openWallet(t, service, "user-2", "short", 2)
	deposit(t, service, short.ID, "opening", "1.00")

	_, emptyErr := service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
		IdempotencyKey: "from-empty", WalletID: empty.ID, Amount: "5.00",
	})
	_, shortErr := service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
		IdempotencyKey: "from-short", WalletID: short.ID, Amount: "5.00",
	})

	if !errors.Is(emptyErr, wallet.ErrBalanceEmpty) {
		t.Errorf("a withdrawal from an empty wallet answered %v, want ErrBalanceEmpty", emptyErr)
	}
	if errors.Is(shortErr, wallet.ErrBalanceEmpty) {
		t.Errorf("a wallet holding one unit was reported as empty: %v", shortErr)
	}

	// Both are still what every caller written before this reads them as.
	for _, err := range []error{emptyErr, shortErr} {
		if !errors.Is(err, wallet.ErrInsufficientFunds) {
			t.Errorf("%v no longer answers to ErrInsufficientFunds", err)
		}
	}
}

// TestAWalletWithCreditIsNeverEmpty holds the other half of the distinction: a
// balance of zero with room to go below it is a wallet with money to spend, and
// what stopped the withdrawal was the size of it.
func TestAWalletWithCreditIsNeverEmpty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	creditLimit(t, service, account.ID, "1.00")

	_, err := service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
		IdempotencyKey: "past-the-limit", WalletID: account.ID, Amount: "5.00",
	})
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("answered %v, want ErrInsufficientFunds", err)
	}
	if errors.Is(err, wallet.ErrBalanceEmpty) {
		t.Error("a wallet with a credit limit was reported as empty, and it has money to spend")
	}
}
