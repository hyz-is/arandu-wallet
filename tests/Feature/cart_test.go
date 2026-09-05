package feature_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What a basket does to the money, and what it refuses to do.
//
// A purchase is the only movement in this package that the application prices,
// so these tests supply a catalogue of their own: a product answers a key, a
// wallet and a price, and one of them also answers whether there are any left.
// What is being checked is never the catalogue -- it is that what left the payer
// and what arrived at the seller are exactly the items, the fee and the
// discount, that a refusal leaves nothing written, and that a line given back
// gives back exactly what it took.

// item is a product of the pretend application, priced in the payer's minor
// units.
type item struct {
	key      string
	wallet   string
	price    wallet.Amount
	askedFor int
}

func (p *item) ProductKey() string       { return p.key }
func (p *item) ReceiverWalletID() string { return p.wallet }

func (p *item) Price(_ context.Context, _ security.Grant, _ wallet.Wallet) (wallet.Amount, error) {
	p.askedFor++
	return p.price, nil
}

// limited is a product with a stock, which is the application's to keep.
type limited struct {
	*item
	left int
	// asked is how many times the stock was consulted, which is the half of
	// this that matters: a basket that checked the stock after moving money
	// would be a basket that took money for something it then refused.
	asked int
}

func (p *limited) CanBuy(_ context.Context, _ security.Grant, _ wallet.Wallet, quantity int) error {
	p.asked++
	if quantity > p.left {
		return fmt.Errorf("only %d left", p.left)
	}
	return nil
}

// Compile-time proof that the doubles answer the seams this package declares.
var (
	_ wallet.Product        = (*item)(nil)
	_ wallet.LimitedProduct = (*limited)(nil)
)

// tenthFee charges a tenth of what is paid, into a wallet named here.
type tenthFee struct {
	collector  string
	deductible bool
}

func (f tenthFee) Fee(_ context.Context, _ security.Grant, _ wallet.Wallet, _ wallet.Money) (wallet.FeeSchedule, error) {
	return wallet.FeeSchedule{
		Numerator: 1, Denominator: 10, Deductible: f.deductible, WalletID: f.collector,
	}, nil
}

// flatDiscount takes a fixed number of minor units off every line.
type flatDiscount struct{ off wallet.Amount }

func (d flatDiscount) Discount(_ context.Context, _ security.Grant, _, _ wallet.Wallet, _ wallet.Money) (wallet.Amount, error) {
	return d.off, nil
}

var (
	_ wallet.FeeProvider      = tenthFee{}
	_ wallet.DiscountProvider = flatDiscount{}
)

func TestABasketMovesExactlyTheItemsLessTheDiscountPlusTheFee(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil,
		tenthFee{collector: "will-be-set"}, flatDiscount{off: 100})

	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop-1", "main", 2)
	house := openWallet(t, service, "house", "fees", 2)
	deposit(t, service, buyer.ID, "seed-1", "500.00")

	// The collector is a wallet, so the schedule can only be built once there is
	// one. Everything else about the service is already wired.
	service = wallet.NewWalletService(db, nil, tenthFee{collector: house.ID}, flatDiscount{off: 100})

	book := &item{key: "book", wallet: shop.ID, price: 2500}
	pen := &item{key: "pen", wallet: shop.ID, price: 300}

	receipt, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1",
		PayerWalletID:  buyer.ID,
		Cart: wallet.NewCart(
			wallet.CartItem{Product: book, Quantity: 2},
			wallet.CartItem{Product: pen, Quantity: 3},
		),
	})
	if err != nil {
		t.Fatalf("paying for the basket: %v", err)
	}

	// Line by line, in the payer's minor units:
	//   book: 2500 x 2 = 5000, less 100 = 4900, fee 490, paid 5390
	//   pen:   300 x 3 =  900, less 100 =  800, fee  80, paid  880
	if len(receipt.Purchases) != 2 {
		t.Fatalf("the basket recorded %d lines, want 2", len(receipt.Purchases))
	}
	wantLines := []struct {
		key                                            string
		requested, discount, base, fee, paid, credited wallet.Amount
	}{
		{"book", 5000, 100, 4900, 490, 5390, 4900},
		{"pen", 900, 100, 800, 80, 880, 800},
	}
	for i, want := range wantLines {
		got := receipt.Purchases[i]
		if got.ProductKey != want.key {
			t.Fatalf("line %d is %q, want %q", i, got.ProductKey, want.key)
		}
		for _, check := range []struct {
			name       string
			got, wants wallet.Amount
		}{
			{"requested", got.RequestedAmount, want.requested},
			{"discount", got.Discount, want.discount},
			{"base", got.BaseAmount, want.base},
			{"fee", got.FeeAmount, want.fee},
			{"paid", got.PaidAmount, want.paid},
			{"credited", got.CreditedAmount, want.credited},
		} {
			if check.got != check.wants {
				t.Errorf("%s: %s is %d, want %d", want.key, check.name, check.got, check.wants)
			}
		}
	}

	// And the balances are the sum of exactly those numbers.
	if got := balanceOf(t, service, buyer.ID); got != 50000-5390-880 {
		t.Errorf("the buyer holds %d, want %d", got, 50000-5390-880)
	}
	if got := balanceOf(t, service, shop.ID); got != 4900+800 {
		t.Errorf("the shop holds %d, want %d", got, 4900+800)
	}
	if got := balanceOf(t, service, house.ID); got != 490+80 {
		t.Errorf("the house holds %d, want %d", got, 490+80)
	}

	// Nothing was lost between the wallets: what left one is what arrived in
	// the other two.
	left := 50000 - balanceOf(t, service, buyer.ID)
	arrived := balanceOf(t, service, shop.ID) + balanceOf(t, service, house.ID)
	if left != arrived {
		t.Fatalf("%d left the buyer and %d arrived elsewhere", left, arrived)
	}

	// And every wallet's ledger sums to its balance.
	for _, held := range []*wallet.Wallet{buyer, shop, house} {
		if got, want := ledgerOf(t, service, held.ID), balanceOf(t, service, held.ID); got != want {
			t.Errorf("the ledger of %s sums to %d and the balance is %d", held.Slug, got, want)
		}
	}

	// One operation, and the whole basket under it.
	if receipt.Operation.Kind != wallet.OperationPurchase {
		t.Errorf("the basket was recorded as %q, want %q", receipt.Operation.Kind, wallet.OperationPurchase)
	}
	if len(receipt.Entries) != 6 {
		t.Errorf("the basket wrote %d entries, want 6: two lines of pay, receive and fee", len(receipt.Entries))
	}
}

func TestAProductThatRanOutIsRefusedBeforeAnyMoneyMoves(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)

	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop-1", "main", 2)
	deposit(t, service, buyer.ID, "seed-1", "500.00")

	plenty := &item{key: "book", wallet: shop.ID, price: 1000}
	scarce := &limited{item: &item{key: "ticket", wallet: shop.ID, price: 1000}, left: 1}

	_, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1",
		PayerWalletID:  buyer.ID,
		Cart: wallet.NewCart(
			wallet.CartItem{Product: plenty, Quantity: 1},
			wallet.CartItem{Product: scarce, Quantity: 2},
		),
	})
	if !errors.Is(err, wallet.ErrProductStock) {
		t.Fatalf("a basket asking for more than there is answered %v, want ErrProductStock", err)
	}
	if scarce.asked == 0 {
		t.Fatal("the stock was never consulted")
	}

	// Nothing moved, on either side, and the first line of the basket did not
	// slip through before the second was refused.
	if got := balanceOf(t, service, buyer.ID); got != 50000 {
		t.Errorf("the buyer holds %d, want 50000: a refused basket moved money", got)
	}
	if got := balanceOf(t, service, shop.ID); got != 0 {
		t.Errorf("the shop holds %d, want 0: a refused basket paid somebody", got)
	}
	if got := ledgerOf(t, service, shop.ID); got != 0 {
		t.Errorf("the shop's ledger sums to %d, want 0", got)
	}

	// And the key is still free, because nothing was recorded under it.
	if _, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1",
		PayerWalletID:  buyer.ID,
		Cart:           wallet.NewCart(wallet.CartItem{Product: plenty}),
	}); err != nil {
		t.Fatalf("the refused basket had already claimed its idempotency key: %v", err)
	}
}

func TestAGiftDebitsWhoPaysAndBelongsToSomebodyElse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)

	payer := openWallet(t, service, "user-1", "main", 2)
	friend := openWallet(t, service, "user-2", "main", 2)
	shop := openWallet(t, service, "shop-1", "main", 2)
	deposit(t, service, payer.ID, "seed-1", "100.00")

	book := &item{key: "book", wallet: shop.ID, price: 2500}

	receipt, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "gift-1",
		PayerWalletID:  payer.ID,
		Cart: wallet.NewCart(wallet.CartItem{
			Product: book, BeneficiaryWalletID: friend.ID,
		}),
	})
	if err != nil {
		t.Fatalf("giving: %v", err)
	}

	// The money is the payer's and the thing bought is not.
	if got := balanceOf(t, service, payer.ID); got != 10000-2500 {
		t.Errorf("the payer holds %d, want %d", got, 10000-2500)
	}
	if got := balanceOf(t, service, friend.ID); got != 0 {
		t.Errorf("the beneficiary holds %d, want 0: a gift moved money into their wallet", got)
	}
	if got := balanceOf(t, service, shop.ID); got != 2500 {
		t.Errorf("the shop holds %d, want 2500", got)
	}

	line := receipt.Purchases[0]
	if line.Kind != wallet.PurchaseGift {
		t.Errorf("the line was recorded as %q, want %q", line.Kind, wallet.PurchaseGift)
	}
	if line.PayerWalletID != payer.ID {
		t.Errorf("the line was paid by %s, want %s", line.PayerWalletID, payer.ID)
	}
	if line.OwnerWalletID != friend.ID {
		t.Errorf("the line belongs to %s, want %s", line.OwnerWalletID, friend.ID)
	}

	// And it is the beneficiary who has it, not the person who paid.
	answers, err := service.Bought(ctx, staff(), []wallet.PurchaseQuery{
		{OwnerWalletID: friend.ID, ReceiverWalletID: shop.ID, ProductKey: "book"},
		{OwnerWalletID: friend.ID, ReceiverWalletID: shop.ID, ProductKey: "book", IncludeGifts: true},
		{OwnerWalletID: payer.ID, ReceiverWalletID: shop.ID, ProductKey: "book", IncludeGifts: true},
	})
	if err != nil {
		t.Fatalf("asking who has it: %v", err)
	}
	if answers[0] != nil {
		t.Error("a gift answered a question about what somebody paid for")
	}
	if answers[1] == nil {
		t.Error("the beneficiary was not found to have been given it")
	}
	if answers[2] != nil {
		t.Error("the person who paid was found to have it, and it is not theirs")
	}
}

func TestARefundGivesBackOnlyWhatWasUndone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)
	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop-1", "main", 2)
	house := openWallet(t, service, "house", "fees", 2)
	deposit(t, service, buyer.ID, "seed-1", "500.00")

	service = wallet.NewWalletService(db, nil, tenthFee{collector: house.ID}, nil)
	book := &item{key: "book", wallet: shop.ID, price: 2500}
	pen := &item{key: "pen", wallet: shop.ID, price: 300}

	bought, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1",
		PayerWalletID:  buyer.ID,
		Cart: wallet.NewCart(
			wallet.CartItem{Product: book, Quantity: 2},
			wallet.CartItem{Product: pen, Quantity: 3},
		),
	})
	if err != nil {
		t.Fatalf("paying: %v", err)
	}

	afterBuying := map[string]wallet.Amount{
		buyer.ID: balanceOf(t, service, buyer.ID),
		shop.ID:  balanceOf(t, service, shop.ID),
		house.ID: balanceOf(t, service, house.ID),
	}
	books := bought.Purchases[0]

	given, err := service.Refund(ctx, staff(), wallet.RefundRequest{
		IdempotencyKey: "refund-1",
		PurchaseIDs:    []string{books.ID},
		Reason:         "the customer sent them back",
	})
	if err != nil {
		t.Fatalf("refunding one line: %v", err)
	}
	if given.Operation.Kind != wallet.OperationRefund {
		t.Errorf("the refund was recorded as %q, want %q", given.Operation.Kind, wallet.OperationRefund)
	}

	// Exactly the one line came back: what it paid, what it credited and the
	// fee it was charged, and nothing of the line that was kept.
	if got, want := balanceOf(t, service, buyer.ID), afterBuying[buyer.ID]+books.PaidAmount; got != want {
		t.Errorf("the buyer holds %d, want %d", got, want)
	}
	if got, want := balanceOf(t, service, shop.ID), afterBuying[shop.ID]-books.CreditedAmount; got != want {
		t.Errorf("the shop holds %d, want %d", got, want)
	}
	if got, want := balanceOf(t, service, house.ID), afterBuying[house.ID]-books.FeeAmount; got != want {
		t.Errorf("the house holds %d, want %d", got, want)
	}

	// The other line is still bought, and the refunded one is not.
	answers, err := service.Bought(ctx, staff(), []wallet.PurchaseQuery{
		{OwnerWalletID: buyer.ID, ReceiverWalletID: shop.ID, ProductKey: "book"},
		{OwnerWalletID: buyer.ID, ReceiverWalletID: shop.ID, ProductKey: "pen"},
	})
	if err != nil {
		t.Fatalf("asking what is still bought: %v", err)
	}
	if answers[0] != nil {
		t.Error("a refunded line still answers as bought")
	}
	if answers[1] == nil {
		t.Error("the line that was kept stopped answering as bought")
	}

	// The ledger closes on every wallet the refund touched.
	for _, held := range []*wallet.Wallet{buyer, shop, house} {
		if got, want := ledgerOf(t, service, held.ID), balanceOf(t, service, held.ID); got != want {
			t.Errorf("the ledger of %s sums to %d and the balance is %d", held.Slug, got, want)
		}
	}

	// And a line is given back once.
	if _, err := service.Refund(ctx, staff(), wallet.RefundRequest{
		IdempotencyKey: "refund-2",
		PurchaseIDs:    []string{books.ID},
		Reason:         "again",
	}); !errors.Is(err, wallet.ErrAlreadyRefunded) {
		t.Fatalf("a second refund of one line answered %v, want ErrAlreadyRefunded", err)
	}

	// A basket is undone line by line, so reversing the whole operation is
	// refused: it would give back the line that already came back.
	if _, err := service.Reverse(ctx, staff(), wallet.ReverseRequest{
		IdempotencyKey: "reverse-1",
		OperationID:    bought.Operation.ID,
		Reason:         "all of it",
	}); !errors.Is(err, wallet.ErrPurchaseOperation) {
		t.Fatalf("reversing a purchase answered %v, want ErrPurchaseOperation", err)
	}
}

func TestOneBadLineLeavesNoHalfOfABasketWritten(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)

	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop-1", "main", 2)
	deposit(t, service, buyer.ID, "seed-1", "30.00")

	cheap := &item{key: "pen", wallet: shop.ID, price: 500}
	dear := &item{key: "desk", wallet: shop.ID, price: 900000}

	// The first two lines fit and the third does not, so the balance runs out
	// in the middle of the basket.
	_, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1",
		PayerWalletID:  buyer.ID,
		Cart: wallet.NewCart(
			wallet.CartItem{Product: cheap},
			wallet.CartItem{Product: cheap},
			wallet.CartItem{Product: dear},
		),
	})
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("a basket past the balance answered %v, want ErrInsufficientFunds", err)
	}

	if got := balanceOf(t, service, buyer.ID); got != 3000 {
		t.Errorf("the buyer holds %d, want 3000: part of the basket was paid for", got)
	}
	if got := balanceOf(t, service, shop.ID); got != 0 {
		t.Errorf("the shop holds %d, want 0: part of the basket was delivered", got)
	}

	// No entry, no operation and no line survived the rollback.
	statement, err := service.History(ctx, staff(), wallet.HistoryRequest{
		WalletID: shop.ID,
		Query:    data.Query{Limit: 50},
	})
	if err != nil {
		t.Fatalf("reading the shop's ledger: %v", err)
	}
	if len(statement.Entries) != 0 {
		t.Fatalf("the shop's ledger holds %d entries, want none", len(statement.Entries))
	}

	lines, err := service.PurchasesOf(ctx, staff(), buyer.ID, data.Query{Limit: 50})
	if err != nil {
		t.Fatalf("reading what the buyer bought: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("%d lines were recorded for a basket nobody paid for", len(lines))
	}
}

func TestTheSameBasketPaidTwiceUnderOneKeyPaysOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)

	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop-1", "main", 2)
	deposit(t, service, buyer.ID, "seed-1", "100.00")

	book := &item{key: "book", wallet: shop.ID, price: 2500}
	cart := wallet.NewCart(wallet.CartItem{Product: book, Quantity: 2})

	first, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1", PayerWalletID: buyer.ID, Cart: cart,
	})
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	second, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1", PayerWalletID: buyer.ID, Cart: cart,
	})
	if err != nil {
		t.Fatalf("paying again under the same key: %v", err)
	}

	if !second.Replayed {
		t.Fatal("the second call paid again instead of answering with the first one's receipt")
	}
	if got := balanceOf(t, service, buyer.ID); got != 10000-5000 {
		t.Fatalf("the buyer holds %d, want %d: the basket was paid for twice", got, 10000-5000)
	}
	if len(second.Purchases) != len(first.Purchases) {
		t.Fatalf("the replay answered %d lines and the first call recorded %d", len(second.Purchases), len(first.Purchases))
	}
	if second.Purchases[0].ID != first.Purchases[0].ID {
		t.Error("the replay answered with a line the first call did not record")
	}
}

func TestABasketNeverCrossesARate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, &fixedRate{numerator: 5, denominator: 1}, nil, nil)

	buyer := openIn(t, service, "user-1", "main", "BRL", 2)
	shop := openIn(t, service, "shop-1", "main", "USD", 2)
	deposit(t, service, buyer.ID, "seed-1", "100.00")

	book := &item{key: "book", wallet: shop.ID, price: 2500}

	_, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1",
		PayerWalletID:  buyer.ID,
		Cart:           wallet.NewCart(wallet.CartItem{Product: book}),
	})
	if !errors.Is(err, wallet.ErrCurrencyMismatch) {
		t.Fatalf("a basket across two currencies answered %v, want ErrCurrencyMismatch", err)
	}
	if got := balanceOf(t, service, buyer.ID); got != 10000 {
		t.Fatalf("the buyer holds %d, want 10000", got)
	}
}

func TestAPriceOnTheLineIsWhatIsCharged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)

	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop-1", "main", 2)
	deposit(t, service, buyer.ID, "seed-1", "100.00")

	book := &item{key: "book", wallet: shop.ID, price: 2500}

	receipt, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1",
		PayerWalletID:  buyer.ID,
		Cart:           wallet.NewCart(wallet.CartItem{Product: book, PricePerItem: "9.99", Quantity: 2}),
	})
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if got := receipt.Purchases[0].PricePerItem; got != 999 {
		t.Errorf("the line was priced at %d, want 999", got)
	}
	if got := receipt.Purchases[0].RequestedAmount; got != 1998 {
		t.Errorf("the line asked for %d, want 1998", got)
	}
	if book.askedFor != 0 {
		t.Errorf("the product was asked its price %d times, and the line already said what it costs", book.askedFor)
	}
}
