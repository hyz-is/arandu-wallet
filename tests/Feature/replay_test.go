package feature_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// An idempotency key is a name the caller chose, and knowing one is not
// permission to read what it wrote.
//
// The column is unique per tenant, not per holder, so two people in one tenant
// can pick the same string and one of them can guess the other's. A lookup by
// key alone answers with whoever wrote first -- which handed the loser the
// winner's operation, entries and, on a basket, the lines it bought.

// TestAReplayIsRefusedToSomebodyOutsideTheOperation is the fast path: the key
// has already been spent, and the caller asking about it was not in it.
func TestAReplayIsRefusedToSomebodyOutsideTheOperation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	mine := openWallet(t, service, "bob", "main", 2)
	theirs := openWallet(t, service, "alice", "main", 2)
	deposit(t, service, mine.ID, "seed", "20.00")
	deposit(t, service, theirs.ID, "seed-alice", "20.00")

	// Bob spends the key on his own wallet.
	first, err := service.Withdraw(ctx, person("bob"), wallet.WithdrawRequest{
		IdempotencyKey: "shared-name", WalletID: mine.ID, Amount: "1.00",
	})
	if err != nil {
		t.Fatalf("bob's withdrawal: %v", err)
	}

	// Bob asking again is answered, which is what idempotency is for.
	again, err := service.Withdraw(ctx, person("bob"), wallet.WithdrawRequest{
		IdempotencyKey: "shared-name", WalletID: mine.ID, Amount: "1.00",
	})
	if err != nil {
		t.Fatalf("bob's own replay was refused: %v", err)
	}
	if !again.Replayed || again.Operation.ID != first.Operation.ID {
		t.Fatalf("bob's replay answered a different operation: %+v", again.Operation.ID)
	}

	// Alice naming Bob's wallet is refused by the policy, as it always was.
	if _, err := service.Withdraw(ctx, person("alice"), wallet.WithdrawRequest{
		IdempotencyKey: "shared-name", WalletID: mine.ID, Amount: "1.00",
	}); err == nil {
		t.Fatal("alice withdrew from bob's wallet")
	}

	// And Alice naming her own wallet with Bob's key is refused too, which is
	// the hole: the policy says yes about her wallet, and the key would have
	// answered with his operation.
	leaked, err := service.Withdraw(ctx, person("alice"), wallet.WithdrawRequest{
		IdempotencyKey: "shared-name", WalletID: theirs.ID, Amount: "1.00",
	})
	if err == nil {
		t.Fatal("alice was answered with an operation she was not in")
	}
	if leaked.Operation.ID != "" || len(leaked.Entries) > 0 {
		t.Fatalf("a refused replay still carried operation=%q entries=%d",
			leaked.Operation.ID, len(leaked.Entries))
	}
}

// TestTheReplayAfterALostRaceIsRefusedToSomebodyOutsideIt is the other path,
// and the one the consumer's suite does not claim to reach.
//
// The fast path above finds the key already spent. This one does not: both
// callers get past that lookup, both build an operation row under the same key,
// and the unique index refuses the second. The loser then looks up what the
// winner did -- and that lookup is a second place the key could have been
// treated as permission.
//
// The two callers are the holder and somebody else, on their own wallets, under
// one key. Whoever wins, the other has to be refused rather than answered, and
// exactly one withdrawal may have happened on each wallet at most.
func TestTheReplayAfterALostRaceIsRefusedToSomebodyOutsideIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	mine := openWallet(t, service, "bob", "main", 2)
	theirs := openWallet(t, service, "alice", "main", 2)
	deposit(t, service, mine.ID, "seed-bob", "20.00")
	deposit(t, service, theirs.ID, "seed-alice", "20.00")

	type answer struct {
		holder  string
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
	done.Add(2)

	for _, who := range []struct{ holder, walletID string }{
		{"bob", mine.ID},
		{"alice", theirs.ID},
	} {
		go func() {
			defer done.Done()
			start.Wait()

			receipt, err := service.Withdraw(ctx, person(who.holder), wallet.WithdrawRequest{
				IdempotencyKey: "one-name-two-people",
				WalletID:       who.walletID,
				Amount:         "1.00",
			})
			mu.Lock()
			defer mu.Unlock()
			answers = append(answers, answer{holder: who.holder, receipt: receipt, err: err})
		}()
	}
	start.Done()
	done.Wait()

	// Exactly one of them is answered. The other lost the race on the unique
	// index and was not in the winner's operation, so it is refused.
	answered := 0
	for _, a := range answers {
		if a.err == nil {
			answered++
			continue
		}
		if a.receipt.Operation.ID != "" || len(a.receipt.Entries) > 0 {
			t.Errorf("%s was refused and still handed operation=%q entries=%d",
				a.holder, a.receipt.Operation.ID, len(a.receipt.Entries))
		}
	}
	if answered != 1 {
		t.Fatalf("%d of two callers under one key were answered, want exactly 1", answered)
	}

	// And whoever was refused kept their money: the loser was not charged for
	// the winner's withdrawal.
	for _, a := range answers {
		balance := balanceOf(t, service, map[string]string{"bob": mine.ID, "alice": theirs.ID}[a.holder])
		want := wallet.Amount(2000)
		if a.err == nil {
			want = 1900
		}
		if balance != want {
			t.Errorf("%s holds %d, want %d", a.holder, balance, want)
		}
	}
}

// TestBothSidesOfATransferMayReplayIt states the choice the ownership check
// makes, so it is a decision somebody wrote rather than a thing that happens.
//
// A transfer writes an entry on the payer and one on the payee, and either of
// them replaying under that key is answered. The receipt describes a movement
// that this wallet's own ledger already shows; refusing it would mean the payee
// cannot ask what a payment they received consisted of.
func TestBothSidesOfATransferMayReplayIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	payer := openWallet(t, service, "bob", "main", 2)
	payee := openWallet(t, service, "alice", "main", 2)
	deposit(t, service, payer.ID, "seed", "20.00")

	first, err := service.Transfer(ctx, staff(), wallet.TransferRequest{
		IdempotencyKey: "both-sides", FromWalletID: payer.ID, ToWalletID: payee.ID, Amount: "5.00",
	})
	if err != nil {
		t.Fatalf("the transfer: %v", err)
	}

	// The payee asks under the same key, naming their own wallet as the source.
	// They are in the operation, so they are answered -- and nothing moves.
	replayed, err := service.Transfer(ctx, staff(), wallet.TransferRequest{
		IdempotencyKey: "both-sides", FromWalletID: payee.ID, ToWalletID: payer.ID, Amount: "5.00",
	})
	if err != nil {
		t.Fatalf("the payee's replay was refused: %v", err)
	}
	if !replayed.Replayed || replayed.Operation.ID != first.Operation.ID {
		t.Fatalf("the payee's replay answered a different operation")
	}
	if got := balanceOf(t, service, payer.ID); got != 1500 {
		t.Fatalf("the payer holds %d after a replay, want 1500: the replay moved money", got)
	}
	if got := balanceOf(t, service, payee.ID); got != 500 {
		t.Fatalf("the payee holds %d after a replay, want 500", got)
	}
}

// TestAKeySpentOnAnotherKindIsStillAConflict keeps the answer that was already
// right: the same key under a different kind of operation is refused as a
// conflict rather than answered or silently re-run.
func TestAKeySpentOnAnotherKindIsStillAConflict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "bob", "main", 2)
	deposit(t, service, account.ID, "one-key", "20.00")

	_, err := service.Withdraw(ctx, person("bob"), wallet.WithdrawRequest{
		IdempotencyKey: "one-key", WalletID: account.ID, Amount: "1.00",
	})
	if !errors.Is(err, wallet.ErrOperationConflict) {
		t.Fatalf("a deposit's key reused for a withdrawal answered %v, want ErrOperationConflict", err)
	}
}

// TestAReplayPricesNothing holds the property the ownership check had to be
// added without breaking: the same key twice pays once, at one price.
//
// Moving the replay lookup after the wallet is authorized moved it past more of
// each method, and the risk of that is a duplicate request doing work before it
// discovers it is a duplicate -- asking a catalogue for a price, a provider for
// a rate, a seam for a fee. None of those is free of consequence: a Price that
// reserves stock, a rate that costs a request, a fee that is metered.
//
// So this counts. The catalogue is asked once across two calls under one key,
// and the second answer is the first call's rows read back rather than priced
// again.
//
// The same claim about the rate and the fee seams is held elsewhere, because
// they belong to other methods: exchange_test.go counts the rate provider
// across a replayed exchange, and fee_test.go counts the fee and discount
// providers across a replayed payment.
func TestAReplayPricesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	buyer := openWallet(t, service, "bob", "main", 2)
	shop := openWallet(t, service, "shop", "till", 2)
	deposit(t, service, buyer.ID, "seed", "100.00")

	book := &item{key: "book", wallet: shop.ID, price: 1000}
	basket := wallet.NewCart(wallet.CartItem{Product: book, Quantity: 1})

	first, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "one-basket", PayerWalletID: buyer.ID, Cart: basket,
	})
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if book.askedFor != 1 {
		t.Fatalf("the catalogue was asked %d times for one payment", book.askedFor)
	}

	second, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "one-basket", PayerWalletID: buyer.ID, Cart: basket,
	})
	if err != nil {
		t.Fatalf("paying again under the same key: %v", err)
	}
	if !second.Replayed {
		t.Fatal("the second call paid again instead of answering with the first receipt")
	}
	if book.askedFor != 1 {
		t.Errorf("the catalogue was asked %d times across two calls under one key, want 1: "+
			"a replay discovered itself too late and priced the basket again", book.askedFor)
	}
	if len(second.Purchases) != 1 || second.Purchases[0].ID != first.Purchases[0].ID {
		t.Error("the replay answered with lines the first call did not record")
	}
	if got := balanceOf(t, service, buyer.ID); got != 9000 {
		t.Errorf("the buyer holds %d, want 9000: the basket was paid for twice", got)
	}
}
