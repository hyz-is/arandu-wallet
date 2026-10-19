package feature_test

import (
	"context"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A listener is told after the write committed, so nothing it does can make the
// write not have happened -- and a panic in it must not make the caller think
// it did not.

func TestAListenerThatPanicsDoesNotBreakTheRequestAfterCommit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	told := &recorder{}
	panicking := func(context.Context, wallet.Event) { panic("the listener fell over") }
	service := wallet.NewWalletService(database(t), nil, nil, nil, panicking, told.listen)

	account := openWallet(t, service, "user-1", "main", 2)
	receipt, err := service.Deposit(ctx, staff(), wallet.DepositRequest{
		IdempotencyKey: "fund", WalletID: account.ID, Amount: "10.00",
	})
	if err != nil {
		t.Fatalf("a deposit whose listener panicked answered %v, want the receipt of the deposit that committed", err)
	}
	if receipt.Operation.ID == "" || len(receipt.Entries) != 1 {
		t.Fatalf("the deposit answered %+v, want its operation and its one entry", receipt)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d, want 1000: the write committed before any listener ran", got)
	}

	// The listener after the one that panicked is still told, every time.
	if opened := told.of(wallet.WalletOpened); len(opened) != 1 {
		t.Fatalf("%d openings were told to the second listener, want 1", len(opened))
	}
	if moved := told.of(wallet.MoneyMoved); len(moved) != 1 || moved[0].OperationID != receipt.Operation.ID {
		t.Fatalf("the second listener was told %+v, want the one movement of the deposit", moved)
	}
}
