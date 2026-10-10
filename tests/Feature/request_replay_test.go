package feature_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arandu-io/framework/data"

	wallet "github.com/hyz-is/arandu-wallet"
)

// An idempotency key names one request, and a replay is that request sent
// again. These hold, for every movement, both halves of that: the same key with
// the same request answers with the first receipt and moves nothing, and the
// same key with anything else the caller decided -- the amount, a wallet,
// which side waits, the operation settled, the lines named -- is refused with
// ErrOperationConflict and writes nothing. Without the second half a caller who
// reused a key for another amount was told the money moved, and the new amount
// was dropped without a word.

// held is what a wallet looks like from outside: what it holds, how many rows
// its ledger has, and how many lines name it as their owner.
type held struct {
	balance   wallet.Amount
	entries   int
	purchases int
}

// snapshot reads every wallet named, as the operator.
func snapshot(t *testing.T, service *wallet.WalletService, walletIDs ...string) map[string]held {
	t.Helper()
	out := make(map[string]held, len(walletIDs))
	for _, id := range walletIDs {
		lines, err := service.PurchasesOf(context.Background(), staff(), id, data.Query{Limit: wallet.MaxPageSize})
		if err != nil {
			t.Fatalf("reading the purchases of %s: %v", id, err)
		}
		out[id] = held{balance: balanceOf(t, service, id), entries: entriesOf(t, service, id), purchases: len(lines)}
	}
	return out
}

// assertReplayed holds that a call was answered with the first receipt.
func assertReplayed(t *testing.T, first, again wallet.Receipt, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("the same request under the same key was refused: %v", err)
	}
	if !again.Replayed {
		t.Fatal("the same request under the same key did not report itself as a replay")
	}
	if again.Operation.ID != first.Operation.ID {
		t.Fatalf("the replay answered operation %s, want the first one, %s", again.Operation.ID, first.Operation.ID)
	}
}

// assertRefused holds that a call under a spent key that asks for something
// else is refused with ErrOperationConflict, is handed nothing, and leaves
// every named wallet exactly as it was.
func assertRefused(t *testing.T, service *wallet.WalletService, walletIDs []string, call func() (wallet.Receipt, error)) {
	t.Helper()
	before := snapshot(t, service, walletIDs...)
	receipt, err := call()
	if !errors.Is(err, wallet.ErrOperationConflict) {
		t.Fatalf("another request under a spent key answered %v, want ErrOperationConflict", err)
	}
	if receipt.Operation.ID != "" || len(receipt.Entries) > 0 || len(receipt.Purchases) > 0 || receipt.Replayed {
		t.Fatalf("the refused request was still handed operation=%q entries=%d purchases=%d replayed=%v",
			receipt.Operation.ID, len(receipt.Entries), len(receipt.Purchases), receipt.Replayed)
	}
	after := snapshot(t, service, walletIDs...)
	for _, id := range walletIDs {
		if before[id] != after[id] {
			t.Fatalf("a refused request changed wallet %s from %+v to %+v", id, before[id], after[id])
		}
	}
}

func TestADepositReplaysOnlyTheSameRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	main := openWallet(t, service, "user-1", "main", 2)
	other := openWallet(t, service, "user-1", "other", 2)
	wallets := []string{main.ID, other.ID}

	request := wallet.DepositRequest{IdempotencyKey: "deposit-1", WalletID: main.ID, Amount: "10.00"}
	first, err := service.Deposit(ctx, staff(), request)
	if err != nil {
		t.Fatalf("depositing: %v", err)
	}

	again, err := service.Deposit(ctx, staff(), request)
	assertReplayed(t, first, again, err)

	// The amount is compared as money and not as text: "10.0" is ten.
	spelled := request
	spelled.Amount = "10.0"
	again, err = service.Deposit(ctx, staff(), spelled)
	assertReplayed(t, first, again, err)

	for _, variant := range []struct {
		why  string
		edit func(*wallet.DepositRequest)
	}{
		{"another amount", func(r *wallet.DepositRequest) { r.Amount = "20.00" }},
		{"another wallet", func(r *wallet.DepositRequest) { r.WalletID = other.ID }},
		{"waiting where the first counted", func(r *wallet.DepositRequest) { r.Pending = true }},
	} {
		t.Run(variant.why, func(t *testing.T) {
			changed := request
			variant.edit(&changed)
			assertRefused(t, service, wallets, func() (wallet.Receipt, error) { return service.Deposit(ctx, staff(), changed) })
		})
	}
	if got := balanceOf(t, service, main.ID); got != 1000 {
		t.Fatalf("the wallet holds %d, want the 1000 the first request moved", got)
	}
}

func TestAWithdrawalReplaysOnlyTheSameRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	main := openWallet(t, service, "user-1", "main", 2)
	other := openWallet(t, service, "user-1", "other", 2)
	deposit(t, service, main.ID, "seed-main", "50.00")
	deposit(t, service, other.ID, "seed-other", "50.00")
	wallets := []string{main.ID, other.ID}

	request := wallet.WithdrawRequest{IdempotencyKey: "withdraw-1", WalletID: main.ID, Amount: "10.00"}
	first, err := service.Withdraw(ctx, staff(), request)
	if err != nil {
		t.Fatalf("withdrawing: %v", err)
	}
	again, err := service.Withdraw(ctx, staff(), request)
	assertReplayed(t, first, again, err)

	for _, variant := range []struct {
		why  string
		edit func(*wallet.WithdrawRequest)
	}{
		{"another amount", func(r *wallet.WithdrawRequest) { r.Amount = "11.00" }},
		{"another wallet", func(r *wallet.WithdrawRequest) { r.WalletID = other.ID }},
		{"waiting where the first counted", func(r *wallet.WithdrawRequest) { r.Pending = true }},
	} {
		t.Run(variant.why, func(t *testing.T) {
			changed := request
			variant.edit(&changed)
			assertRefused(t, service, wallets, func() (wallet.Receipt, error) { return service.Withdraw(ctx, staff(), changed) })
		})
	}
	if got := balanceOf(t, service, main.ID); got != 4000 {
		t.Fatalf("the wallet holds %d, want 4000: one withdrawal of 1000", got)
	}
}

func TestATransferReplaysOnlyTheSameRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	payee := openWallet(t, service, "user-2", "main", 2)
	third := openWallet(t, service, "user-3", "main", 2)
	deposit(t, service, payer.ID, "seed-payer", "50.00")
	deposit(t, service, payee.ID, "seed-payee", "50.00")
	wallets := []string{payer.ID, payee.ID, third.ID}

	request := wallet.TransferRequest{IdempotencyKey: "transfer-1", FromWalletID: payer.ID, ToWalletID: payee.ID, Amount: "10.00"}
	first, err := service.Transfer(ctx, staff(), request)
	if err != nil {
		t.Fatalf("transferring: %v", err)
	}
	again, err := service.Transfer(ctx, staff(), request)
	assertReplayed(t, first, again, err)

	for _, variant := range []struct {
		why  string
		edit func(*wallet.TransferRequest)
	}{
		{"another amount", func(r *wallet.TransferRequest) { r.Amount = "12.00" }},
		{"another payee", func(r *wallet.TransferRequest) { r.ToWalletID = third.ID }},
		{"another payer", func(r *wallet.TransferRequest) { r.FromWalletID = third.ID }},
		{"the other direction", func(r *wallet.TransferRequest) { r.FromWalletID, r.ToWalletID = payee.ID, payer.ID }},
		{"the paying side waiting", func(r *wallet.TransferRequest) { r.Withdrawal.Pending = true }},
		{"the paid side waiting", func(r *wallet.TransferRequest) { r.Deposit.Pending = true }},
	} {
		t.Run(variant.why, func(t *testing.T) {
			changed := request
			variant.edit(&changed)
			assertRefused(t, service, wallets, func() (wallet.Receipt, error) { return service.Transfer(ctx, staff(), changed) })
		})
	}
	if got := balanceOf(t, service, payee.ID); got != 6000 {
		t.Fatalf("the payee holds %d, want 6000: one payment of 1000", got)
	}
}

// TestAChargedTransferComparesWhatWasAskedNotWhatLeft holds the branch where
// the first entry is not the amount the caller wrote: a discount took some off
// and a fee went on top, so what was asked for is read off the charge row.
func TestAChargedTransferComparesWhatWasAskedNotWhatLeft(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fees := &scheduled{}
	discounts := &discounted{amount: 500}
	service := wallet.NewWalletService(database(t), nil, fees, discounts)
	payer := openWallet(t, service, "user-1", "main", 2)
	merchant := openWallet(t, service, "user-2", "main", 2)
	collector := openWallet(t, service, "platform", "fees", 2)
	fees.schedule = wallet.FeeSchedule{Numerator: 1, Denominator: 100, WalletID: collector.ID}
	deposit(t, service, payer.ID, "opening", "200.00")
	wallets := []string{payer.ID, merchant.ID, collector.ID}

	request := wallet.TransferRequest{IdempotencyKey: "pay-1", FromWalletID: payer.ID, ToWalletID: merchant.ID, Amount: "100.00"}
	first, err := service.Transfer(ctx, staff(), request)
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if first.Charge == nil || first.Entries[0].Amount == 10000 {
		t.Fatal("the payment was not charged, so this test reads the branch without a charge row")
	}
	again, err := service.Transfer(ctx, staff(), request)
	assertReplayed(t, first, again, err)

	// What left the payer, written as the request, is not what was asked for.
	debited := first.Entries[0].Amount
	changed := request
	changed.Amount = debited.Format(2)
	assertRefused(t, service, wallets, func() (wallet.Receipt, error) { return service.Transfer(ctx, staff(), changed) })
	if fees.Calls() != 1 || discounts.Calls() != 1 {
		t.Errorf("the providers were asked %d and %d times: a refused retry priced something", fees.Calls(), discounts.Calls())
	}
}

func TestAnExchangeReplaysOnlyTheSameRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rates := &fixedRate{numerator: 5, denominator: 1}
	service := wallet.NewWalletService(database(t), rates, nil, nil)
	source := openIn(t, service, "user-1", "main", "USD", 2)
	target := openIn(t, service, "user-2", "main", "BRL", 2)
	deposit(t, service, source.ID, "seed", "10.00")
	wallets := []string{source.ID, target.ID}

	request := wallet.TransferRequest{IdempotencyKey: "exchange-1", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "4.00"}
	first, err := service.Transfer(ctx, staff(), request)
	if err != nil {
		t.Fatalf("exchanging: %v", err)
	}
	again, err := service.Transfer(ctx, staff(), request)
	assertReplayed(t, first, again, err)

	changed := request
	changed.Amount = "5.00"
	assertRefused(t, service, wallets, func() (wallet.Receipt, error) { return service.Transfer(ctx, staff(), changed) })
	if got := rates.Calls(); got != 1 {
		t.Errorf("the provider was asked %d times, want 1: a refused retry quoted a rate", got)
	}
}

func TestABasketReplaysOnlyTheSameRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	buyer := openWallet(t, service, "user-1", "main", 2)
	friend := openWallet(t, service, "user-2", "main", 2)
	shop := openWallet(t, service, "shop", "till", 2)
	other := openWallet(t, service, "other-shop", "till", 2)
	deposit(t, service, buyer.ID, "seed", "100.00")
	wallets := []string{buyer.ID, friend.ID, shop.ID, other.ID}

	book := &item{key: "book", wallet: shop.ID, price: 1000}
	pen := &item{key: "pen", wallet: shop.ID, price: 500}
	basket := wallet.NewCart(wallet.CartItem{Product: book, Quantity: 1})
	request := wallet.PayRequest{IdempotencyKey: "basket-1", PayerWalletID: buyer.ID, Cart: basket}

	first, err := service.Pay(ctx, staff(), request)
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	again, err := service.Pay(ctx, staff(), request)
	assertReplayed(t, first, again, err)

	// The catalogue may answer differently on a retry than it did on the first
	// call, and the request -- this product, this many -- is still the same.
	book.price = 1200
	again, err = service.Pay(ctx, staff(), request)
	assertReplayed(t, first, again, err)
	if book.askedFor != 1 {
		t.Errorf("the catalogue was asked %d times, want 1: a replay priced the basket again", book.askedFor)
	}

	for _, variant := range []struct {
		why  string
		cart wallet.Cart
	}{
		{"another quantity", wallet.NewCart(wallet.CartItem{Product: book, Quantity: 2})},
		{"another product", wallet.NewCart(wallet.CartItem{Product: pen, Quantity: 1})},
		{"another receiver", wallet.NewCart(wallet.CartItem{Product: book, Quantity: 1, ReceiverWalletID: other.ID})},
		{"a gift where the first was not", wallet.NewCart(wallet.CartItem{Product: book, Quantity: 1, BeneficiaryWalletID: friend.ID})},
		{"another price written on the line", wallet.NewCart(wallet.CartItem{Product: book, Quantity: 1, PricePerItem: "9.00"})},
		{"a line more", basket.With(wallet.CartItem{Product: pen, Quantity: 1})},
	} {
		t.Run(variant.why, func(t *testing.T) {
			changed := request
			changed.Cart = variant.cart
			assertRefused(t, service, wallets, func() (wallet.Receipt, error) { return service.Pay(ctx, staff(), changed) })
		})
	}

	// A price written on a line is compared with the price the row recorded.
	written := wallet.PayRequest{
		IdempotencyKey: "basket-2", PayerWalletID: buyer.ID,
		Cart: wallet.NewCart(wallet.CartItem{Product: pen, Quantity: 1, PricePerItem: "7.50"}),
	}
	priced, err := service.Pay(ctx, staff(), written)
	if err != nil {
		t.Fatalf("paying at a written price: %v", err)
	}
	again, err = service.Pay(ctx, staff(), written)
	assertReplayed(t, priced, again, err)
	rewritten := written
	rewritten.Cart = wallet.NewCart(wallet.CartItem{Product: pen, Quantity: 1, PricePerItem: "8.00"})
	assertRefused(t, service, wallets, func() (wallet.Receipt, error) { return service.Pay(ctx, staff(), rewritten) })

	if got := balanceOf(t, service, buyer.ID); got != 10000-1000-750 {
		t.Fatalf("the buyer holds %d, want %d: two baskets, each paid once", got, 10000-1000-750)
	}
}

func TestAConfirmationReplaysOnlyTheSameRequest(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	proposed := pend(t, service, account.ID, "later-1", "10.00")
	waiting := pend(t, service, account.ID, "later-2", "5.00")
	wallets := []string{account.ID}

	first, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-1")
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}
	again, err := confirm(t, service, staff(), proposed.Operation.ID, "settle-1")
	assertReplayed(t, first, again, err)

	assertRefused(t, service, wallets, func() (wallet.Receipt, error) {
		return confirm(t, service, staff(), waiting.Operation.ID, "settle-1")
	})
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the wallet holds %d, want 1000: one of the two confirmed, once", got)
	}
}

// TestAStrangerUnderASpentKeyIsToldTheSame holds that a caller who was not in
// the operation is refused with the answer any other request under a spent key
// gets, and handed nothing of it.
func TestAStrangerUnderASpentKeyIsToldTheSame(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	mine := openWallet(t, service, "bob", "main", 2)
	theirs := openWallet(t, service, "alice", "main", 2)
	deposit(t, service, mine.ID, "spent", "10.00")

	assertRefused(t, service, []string{mine.ID, theirs.ID}, func() (wallet.Receipt, error) {
		return service.Deposit(ctx, person("alice"), wallet.DepositRequest{
			IdempotencyKey: "spent", WalletID: theirs.ID, Amount: "10.00",
		})
	})
}

// TestDifferentRequestsRacingUnderOneKeyMoveOnce is the path where the lookup
// before pricing finds nothing, because nothing has committed yet: every
// caller builds an operation row, the unique index lets one in, and each loser
// looks up what won. Callers who asked for what won are answered with it, and
// callers who asked for another amount are refused -- never with the index
// error underneath, and never by moving their own amount.
func TestDifferentRequestsRacingUnderOneKeyMoveOnce(t *testing.T) {
	t.Parallel()

	raceOneKey(t, wallet.NewWalletService(database(t), nil, nil, nil))
}

// TestDifferentRequestsRacingUnderOneKeyMoveOnceOnPostgres is the same race on
// an engine whose transactions really interleave.
func TestDifferentRequestsRacingUnderOneKeyMoveOnceOnPostgres(t *testing.T) {
	t.Parallel()

	raceOneKey(t, wallet.NewWalletService(postgres(t), nil, nil, nil))
}

// raceOneKey runs forty deposits under one key, half of them for ten and half
// for twenty, all released at once.
func raceOneKey(t *testing.T, service *wallet.WalletService) {
	t.Helper()

	account := openWallet(t, service, "user-1", "main", 2)
	const callers = 40

	type answer struct {
		amount  string
		receipt wallet.Receipt
		err     error
	}
	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		answers []answer
	)
	start.Add(1)
	done.Add(callers)
	for i := range callers {
		amount := "10.00"
		if i%2 == 1 {
			amount = "20.00"
		}
		go func() {
			defer done.Done()
			start.Wait()
			receipt, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
				IdempotencyKey: "raced", WalletID: account.ID, Amount: amount,
			})
			mu.Lock()
			defer mu.Unlock()
			answers = append(answers, answer{amount: amount, receipt: receipt, err: err})
		}()
	}
	start.Done()
	done.Wait()

	won := ""
	for _, a := range answers {
		if a.err == nil && !a.receipt.Replayed {
			if won != "" {
				t.Fatalf("two callers under one key both moved money: %s and %s", won, a.amount)
			}
			won = a.amount
		}
	}
	if won == "" {
		t.Fatal("no caller moved the money")
	}
	for _, a := range answers {
		switch {
		case a.amount == won && a.err != nil:
			t.Errorf("a caller who asked for what won was refused: %v", a.err)
		case a.amount != won && !errors.Is(a.err, wallet.ErrOperationConflict):
			t.Errorf("a caller who asked for %s under a key spent on %s answered %v, want ErrOperationConflict", a.amount, won, a.err)
		}
	}
	want := wallet.Amount(1000)
	if won == "20.00" {
		want = 2000
	}
	if got := balanceOf(t, service, account.ID); got != want {
		t.Fatalf("the wallet holds %d, want %d: the amount that won, once", got, want)
	}
	if got := ledgerOf(t, service, account.ID); got != want {
		t.Fatalf("the ledger sums to %d, want %d", got, want)
	}
}
