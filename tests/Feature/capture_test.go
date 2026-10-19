package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A hold captured in part, the rest given back in the same statement, and a
// hold captured into another wallet instead of out of the ledger.

func capture(service *wallet.WalletService, actor security.Subject, in wallet.CaptureRequest) (wallet.Receipt, error) {
	return service.Capture(context.Background(), actor, in)
}

func capturedHold(t *testing.T, service *wallet.WalletService, walletID, holdID string) *wallet.Hold {
	t.Helper()
	holds, err := service.HoldsOf(context.Background(), staff(), walletID, wallet.HoldCaptured, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range holds {
		if h.ID == holdID {
			return h
		}
	}
	t.Fatalf("hold %s is not captured", holdID)
	return nil
}

func TestPartOfAHoldIsCapturedAndTheRestGivenBack(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		service := wallet.NewWalletService(db, nil, nil, nil)
		account := openWallet(t, service, "user-1", "main", 2)
		deposit(t, service, account.ID, "fund", "10.00")
		h := hold(t, service, staff(), account.ID, "hold-1", "7.00")

		receipt, err := capture(service, staff(), wallet.CaptureRequest{IdempotencyKey: "capture-1", HoldID: h.ID, Amount: "4.00"})
		if err != nil {
			t.Fatalf("capturing 4.00 of the 7.00 held: %v", err)
		}
		if receipt.Operation.Kind != wallet.OperationWithdraw || len(receipt.Entries) != 1 || receipt.Entries[0].Amount != 400 {
			t.Fatalf("the capture wrote %s %+v, want one withdrawal of 400", receipt.Operation.Kind, receipt.Entries)
		}
		if got := balanceOf(t, service, account.ID); got != 600 {
			t.Fatalf("the balance is %d, want 600", got)
		}
		if got := heldOf(t, service, account.ID); got != 0 {
			t.Fatalf("%d is still held after the hold was captured in part, want 0", got)
		}
		if captured := capturedHold(t, service, account.ID, h.ID); captured.CapturedAmount != 400 || captured.OperationID != receipt.Operation.ID {
			t.Fatalf("the hold records %d captured under %s, want 400 under %s", captured.CapturedAmount, captured.OperationID, receipt.Operation.ID)
		}

		again, err := capture(service, staff(), wallet.CaptureRequest{IdempotencyKey: "capture-1", HoldID: h.ID, Amount: "4.00"})
		assertReplayed(t, receipt, again, err)
		if _, err := capture(service, staff(), wallet.CaptureRequest{IdempotencyKey: "capture-2", HoldID: h.ID, Amount: "3.00"}); !errors.Is(err, wallet.ErrHoldNotHeld) {
			t.Fatalf("capturing the rest after a partial capture answered %v, want ErrHoldNotHeld", err)
		}
		if err := withdraw(t, service, staff(), account.ID, "spend", "6.00", false); err != nil {
			t.Fatalf("the money given back by the partial capture is not spendable: %v", err)
		}
	})
}

func TestAHoldIsCapturedIntoAnotherWallet(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		told := &recorder{}
		service := wallet.NewWalletService(db, nil, nil, nil, told.listen)
		buyer := openWallet(t, service, "user-1", "main", 2)
		seller := openWallet(t, service, "user-2", "main", 2)
		deposit(t, service, buyer.ID, "fund", "10.00")

		whole := hold(t, service, staff(), buyer.ID, "hold-whole", "7.00")
		receipt, err := capture(service, staff(), wallet.CaptureRequest{IdempotencyKey: "capture-whole", HoldID: whole.ID, ToWalletID: seller.ID})
		if err != nil {
			t.Fatalf("capturing the hold into the seller's wallet: %v", err)
		}
		if receipt.Operation.Kind != wallet.OperationTransfer || len(receipt.Entries) != 2 ||
			receipt.Entries[0].WalletID != buyer.ID || receipt.Entries[1].WalletID != seller.ID {
			t.Fatalf("the capture wrote %s %+v, want a transfer from the buyer to the seller", receipt.Operation.Kind, receipt.Entries)
		}
		if got := balanceOf(t, service, buyer.ID); got != 300 {
			t.Fatalf("the buyer holds %d, want 300", got)
		}
		if got := balanceOf(t, service, seller.ID); got != 700 {
			t.Fatalf("the seller holds %d, want 700", got)
		}
		if got := heldOf(t, service, buyer.ID); got != 0 {
			t.Fatalf("%d is still held, want 0", got)
		}
		again, err := capture(service, staff(), wallet.CaptureRequest{IdempotencyKey: "capture-whole", HoldID: whole.ID, ToWalletID: seller.ID})
		assertReplayed(t, receipt, again, err)
		if _, err := capture(service, staff(), wallet.CaptureRequest{IdempotencyKey: "capture-whole", HoldID: whole.ID}); !errors.Is(err, wallet.ErrOperationConflict) {
			t.Fatalf("the same key asking for a withdrawal instead answered %v, want ErrOperationConflict", err)
		}

		// In part, into the seller: the seller gets the part, the buyer the rest.
		part := hold(t, service, staff(), buyer.ID, "hold-part", "3.00")
		if _, err := capture(service, staff(), wallet.CaptureRequest{IdempotencyKey: "capture-part", HoldID: part.ID, ToWalletID: seller.ID, Amount: "2.50"}); err != nil {
			t.Fatalf("capturing 2.50 of 3.00 into the seller: %v", err)
		}
		if got := balanceOf(t, service, buyer.ID); got != 50 {
			t.Fatalf("the buyer holds %d, want 50", got)
		}
		if got := balanceOf(t, service, seller.ID); got != 950 {
			t.Fatalf("the seller holds %d, want 950", got)
		}
		if got := heldOf(t, service, buyer.ID); got != 0 {
			t.Fatalf("%d is still held, want 0", got)
		}
		for _, id := range []string{buyer.ID, seller.ID} {
			if ledger, balance := ledgerOf(t, service, id), balanceOf(t, service, id); ledger != balance {
				t.Fatalf("wallet %s sums to %d and holds %d", id, ledger, balance)
			}
		}
		if moved := told.of(wallet.MoneyMoved); len(moved) != 5 {
			t.Fatalf("%d movements were told, want 5: the deposit and both sides of each capture", len(moved))
		}
	})
}

func TestACaptureThatCannotBeTakenIsRefused(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	buyer := openWallet(t, service, "user-1", "main", 2)
	dollars := openIn(t, service, "user-2", "usd", "USD", 2)
	deposit(t, service, buyer.ID, "fund", "10.00")
	h := hold(t, service, staff(), buyer.ID, "hold-1", "7.00")

	for name, refusal := range map[string]struct {
		in   wallet.CaptureRequest
		want error
	}{
		"more than was held":         {wallet.CaptureRequest{Amount: "7.01"}, wallet.ErrCaptureExceedsHold},
		"nothing":                    {wallet.CaptureRequest{Amount: "0.00"}, wallet.ErrAmountNotPositive},
		"into the wallet it is on":   {wallet.CaptureRequest{ToWalletID: buyer.ID}, wallet.ErrSameWallet},
		"into a wallet in dollars":   {wallet.CaptureRequest{ToWalletID: dollars.ID}, wallet.ErrCurrencyMismatch},
		"into a wallet that is not":  {wallet.CaptureRequest{ToWalletID: "no-such-wallet"}, wallet.ErrNotFound},
		"by somebody else's subject": {wallet.CaptureRequest{}, security.ErrForbidden},
	} {
		in := refusal.in
		in.IdempotencyKey, in.HoldID = "refused-"+name, h.ID
		actor := staff()
		if name == "by somebody else's subject" {
			actor = person("user-2")
		}
		if _, err := capture(service, actor, in); !errors.Is(err, refusal.want) {
			t.Errorf("a capture %s answered %v, want %v", name, err, refusal.want)
		}
	}
	if got := heldOf(t, service, buyer.ID); got != 700 {
		t.Fatalf("the refusals moved the held amount to %d, want 700", got)
	}
	if got := balanceOf(t, service, buyer.ID); got != 1000 {
		t.Fatalf("the refusals moved the balance to %d, want 1000", got)
	}
}
