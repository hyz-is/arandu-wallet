package feature_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/arandu-io/framework/data"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A line that is bought and costs nothing.
//
// A trial, a bundled item, something a shop gives away: the record of who has
// what is the same record, and an application that could not write it here
// would keep a second table of its own -- which is a second answer to "has this
// person already got one", the question the purchase row exists for.

func TestAFreeLineIsBoughtAndMovesNoMoney(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop", "main", 2)
	deposit(t, service, buyer.ID, "opening", "10.00")

	trial := &item{key: "trial", wallet: shop.ID, price: 0}
	receipt, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "free-1",
		PayerWalletID:  buyer.ID,
		Cart:           wallet.NewCart(wallet.CartItem{Product: trial}),
	})
	if err != nil {
		t.Fatalf("paying for a free line: %v", err)
	}

	if len(receipt.Purchases) != 1 {
		t.Fatalf("the basket recorded %d lines, want 1", len(receipt.Purchases))
	}
	line := receipt.Purchases[0]
	if !line.Free() {
		t.Errorf("the line says it cost %d and credited %d", line.PaidAmount, line.CreditedAmount)
	}
	if line.ProductKey != "trial" || line.OwnerWalletID != buyer.ID {
		t.Errorf("the line records %q for %s", line.ProductKey, line.OwnerWalletID)
	}

	// No ledger row on either side, because nothing moved.
	if len(receipt.Entries) != 0 {
		t.Errorf("a free line wrote %d ledger entries, and an entry of zero is a movement that says nothing happened", len(receipt.Entries))
	}
	if got := balanceOf(t, service, buyer.ID); got != 1000 {
		t.Errorf("the buyer holds %d, want 1000", got)
	}
	if got := balanceOf(t, service, shop.ID); got != 0 {
		t.Errorf("the shop holds %d, want 0", got)
	}

	// And it answers the question the record exists for.
	answers, err := service.Bought(ctx, staff(), []wallet.PurchaseQuery{
		{OwnerWalletID: buyer.ID, ReceiverWalletID: shop.ID, ProductKey: "trial"},
	})
	if err != nil {
		t.Fatalf("asking what was bought: %v", err)
	}
	if len(answers) != 1 || answers[0] == nil {
		t.Fatalf("a free line does not answer that it was bought: %v", answers)
	}
}

func TestABasketMixesFreeAndPaidLines(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop", "main", 2)
	deposit(t, service, buyer.ID, "opening", "10.00")

	receipt, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "mixed-1",
		PayerWalletID:  buyer.ID,
		Cart: wallet.NewCart(
			wallet.CartItem{Product: &item{key: "book", wallet: shop.ID, price: 300}},
			wallet.CartItem{Product: &item{key: "bookmark", wallet: shop.ID, price: 0}},
		),
	})
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if len(receipt.Purchases) != 2 {
		t.Fatalf("the basket recorded %d lines, want 2", len(receipt.Purchases))
	}
	// Two movements for the paid line and none for the free one.
	if len(receipt.Entries) != 2 {
		t.Errorf("the basket wrote %d ledger entries, want 2", len(receipt.Entries))
	}
	if got := balanceOf(t, service, buyer.ID); got != 700 {
		t.Errorf("the buyer holds %d, want 700", got)
	}
	if got := balanceOf(t, service, shop.ID); got != 300 {
		t.Errorf("the shop holds %d, want 300", got)
	}

	// The free line is given back without moving anything either.
	var free string
	for _, line := range receipt.Purchases {
		if line.Free() {
			free = line.ID
		}
	}
	if free == "" {
		t.Fatal("the basket recorded no free line")
	}
	given, err := service.Refund(ctx, staff(), wallet.RefundRequest{
		IdempotencyKey: "give-back-free", PurchaseIDs: []string{free}, Reason: "returned",
	})
	if err != nil {
		t.Fatalf("refunding a free line: %v", err)
	}
	if len(given.Entries) != 0 {
		t.Errorf("giving back a free line wrote %d entries", len(given.Entries))
	}
	if got := balanceOf(t, service, buyer.ID); got != 700 {
		t.Errorf("the buyer holds %d after a free line came back, want 700", got)
	}
}

// TestANegativePriceIsStillRefused holds the half of this that did not change.
// Zero is a price; a price below zero is a payment pointing the wrong way.
func TestANegativePriceIsStillRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop", "main", 2)
	deposit(t, service, buyer.ID, "opening", "10.00")

	if _, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "negative-1",
		PayerWalletID:  buyer.ID,
		Cart:           wallet.NewCart(wallet.CartItem{Product: &item{key: "rebate", wallet: shop.ID, price: -100}}),
	}); !errors.Is(err, wallet.ErrAmountNotPositive) {
		t.Fatalf("a line priced below zero answered %v, want ErrAmountNotPositive", err)
	}
	if got := balanceOf(t, service, buyer.ID); got != 1000 {
		t.Errorf("the refused basket moved the balance to %d", got)
	}
}

// TestAPageOfPurchasesSkipsNothingWhenSequencesTie holds the paging that free
// lines make load-bearing: they all record a sequence of zero, so a page
// anchored on the sequence alone would skip every row that shares the last
// one's.
func TestAPageOfPurchasesSkipsNothingWhenSequencesTie(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop", "main", 2)

	const lines = 7
	cart := wallet.NewCart()
	for i := range lines {
		cart = cart.With(wallet.CartItem{Product: &item{key: fmt.Sprintf("trial-%d", i), wallet: shop.ID, price: 0}})
	}
	if _, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "free-basket", PayerWalletID: buyer.ID, Cart: cart,
	}); err != nil {
		t.Fatalf("paying: %v", err)
	}

	seen := map[string]bool{}
	cursor := ""
	for range lines {
		page, err := service.PurchasesOf(ctx, staff(), buyer.ID, data.Query{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("reading a page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			if seen[row.ID] {
				t.Fatalf("%s came back on two pages", row.ID)
			}
			seen[row.ID] = true
		}
		cursor = page[len(page)-1].ID
	}
	if len(seen) != lines {
		t.Fatalf("paging reached %d of %d lines, and every one of them records the same sequence", len(seen), lines)
	}
}
