package feature_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arandu-io/framework/data"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What the application attaches to a movement, and the two sides of a payment
// answering separately.
//
// Both are about the same thing: a movement is not one fact. A payment has a
// side that pays and a side that is paid, and an application that could only
// say one thing about the pair would be an application whose receipt says the
// same sentence to the person charged and the person credited.

func TestMetadataTravelsWithTheOperationAndComesBackOnAReplay(t *testing.T) {
	t.Parallel()

	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	attached := wallet.Meta{"invoice": "INV-2026-0007", "channel": "pix"}
	receipt, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
		IdempotencyKey: "meta-1",
		WalletID:       account.ID,
		Amount:         "10.00",
		Meta:           attached,
	})
	if err != nil {
		t.Fatalf("depositing with metadata: %v", err)
	}
	if got := receipt.Operation.Meta["invoice"]; got != "INV-2026-0007" {
		t.Fatalf("the operation carries invoice %q, want INV-2026-0007", got)
	}
	if got := receipt.Operation.Meta["channel"]; got != "pix" {
		t.Fatalf("the operation carries channel %q, want pix", got)
	}

	// The same key again answers with what the first call recorded, read off the
	// row rather than off the request -- so the metadata comes back from the
	// column and not from the value the caller happened to send again.
	replay, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
		IdempotencyKey: "meta-1",
		WalletID:       account.ID,
		Amount:         "10.00",
	})
	if err != nil {
		t.Fatalf("replaying the deposit: %v", err)
	}
	if !replay.Replayed {
		t.Fatal("the second call moved money instead of answering with the first one's receipt")
	}
	if got := replay.Operation.Meta["invoice"]; got != "INV-2026-0007" {
		t.Fatalf("the replayed operation carries invoice %q, want INV-2026-0007", got)
	}
}

func TestTheTwoSidesOfAPaymentCarryTheirOwnFacts(t *testing.T) {
	t.Parallel()

	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)
	source := openWallet(t, service, "user-1", "main", 2)
	target := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, source.ID, "seed-1", "50.00")

	receipt, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "legs-1",
		FromWalletID:   source.ID,
		ToWalletID:     target.ID,
		Amount:         "10.00",
		Meta:           wallet.Meta{"order": "A-1"},
		Withdrawal:     wallet.Leg{Meta: wallet.Meta{"statement": "Payment to shop"}},
		Deposit:        wallet.Leg{Meta: wallet.Meta{"statement": "Sale to user-1"}},
	})
	if err != nil {
		t.Fatalf("transferring: %v", err)
	}
	if got := receipt.Operation.Meta["order"]; got != "A-1" {
		t.Fatalf("the operation carries order %q, want A-1", got)
	}
	if len(receipt.Entries) != 2 {
		t.Fatalf("the transfer wrote %d entries, want 2", len(receipt.Entries))
	}
	for _, entry := range receipt.Entries {
		want := "Payment to shop"
		if entry.Kind == wallet.EntryDeposit {
			want = "Sale to user-1"
		}
		if got := entry.Meta["statement"]; got != want {
			t.Errorf("the %s entry says %q, want %q", entry.Kind, got, want)
		}
	}

	// And it is the column that says so, not the value still in hand.
	statement, err := service.History(context.Background(), staff(), wallet.HistoryRequest{
		WalletID: target.ID,
		Query:    data.Query{Limit: 10},
	})
	if err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}
	if len(statement.Entries) != 1 {
		t.Fatalf("the receiving wallet has %d entries, want 1", len(statement.Entries))
	}
	if got := statement.Entries[0].Meta["statement"]; got != "Sale to user-1" {
		t.Fatalf("the stored entry says %q, want %q", got, "Sale to user-1")
	}
}

func TestOneSideOfAPaymentCanCountWhileTheOtherWaits(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)
	source := openWallet(t, service, "user-1", "main", 2)
	target := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, source.ID, "seed-1", "50.00")

	// The payer is debited now and the delivery waits: money held until
	// somebody says it may be handed over.
	receipt, err := service.Transfer(ctx, staff(), wallet.TransferRequest{
		IdempotencyKey: "escrow-1",
		FromWalletID:   source.ID,
		ToWalletID:     target.ID,
		Amount:         "10.00",
		Withdrawal:     wallet.Leg{Pending: false},
		Deposit:        wallet.Leg{Pending: true},
	})
	if err != nil {
		t.Fatalf("transferring with one side held: %v", err)
	}
	if receipt.Pending() {
		t.Fatal("the receipt reports nothing moved, and the payer was debited")
	}

	if got := balanceOf(t, service, source.ID); got != 4000 {
		t.Fatalf("the payer holds %d, want 4000: the side that counts did not", got)
	}
	if got := balanceOf(t, service, target.ID); got != 0 {
		t.Fatalf("the receiver holds %d, want 0: the side that waits counted", got)
	}

	// And the balance column is the sum of the settled entries on both sides.
	if got := ledgerOf(t, service, source.ID); got != 4000 {
		t.Fatalf("the payer's ledger sums to %d, want 4000", got)
	}
	if got := ledgerOf(t, service, target.ID); got != 0 {
		t.Fatalf("the receiver's ledger sums to %d, want 0", got)
	}

	settled, err := service.Confirm(ctx, staff(), wallet.ConfirmRequest{
		IdempotencyKey: "escrow-confirm-1",
		OperationID:    receipt.Operation.ID,
	})
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}
	if len(settled.Entries) != 1 {
		t.Fatalf("the confirmation moved %d wallets, want 1: only the side that waited had anything to settle", len(settled.Entries))
	}
	if got := balanceOf(t, service, target.ID); got != 1000 {
		t.Fatalf("the receiver holds %d after the confirmation, want 1000", got)
	}
	if got := balanceOf(t, service, source.ID); got != 4000 {
		t.Fatalf("the payer holds %d after the confirmation, want 4000: the side that already counted counted twice", got)
	}
}

func TestMetadataLargerThanTheColumnIsRefusedAsAField(t *testing.T) {
	t.Parallel()

	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	_, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
		IdempotencyKey: "big-1",
		WalletID:       account.ID,
		Amount:         "10.00",
		Meta:           wallet.Meta{"blob": strings.Repeat("x", wallet.MaxMetaBytes+1)},
	})
	if err == nil {
		t.Fatal("a payload larger than the column was accepted")
	}
	if !strings.Contains(err.Error(), "meta") {
		t.Fatalf("the refusal does not name the field: %v", err)
	}
	if errors.Is(err, wallet.ErrNotFound) {
		t.Fatalf("the refusal came from storage rather than from the field: %v", err)
	}

	// Nothing was recorded, so the key is still free for the request that fits.
	if _, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
		IdempotencyKey: "big-1",
		WalletID:       account.ID,
		Amount:         "10.00",
	}); err != nil {
		t.Fatalf("the refused request had already claimed its idempotency key: %v", err)
	}
}
