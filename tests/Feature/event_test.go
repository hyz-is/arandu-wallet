package feature_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What a listener is told, and the one property that makes it worth telling.
//
// An event is a fact about money that moved. Everything else about the design
// -- one per movement, the balance on it, the actor off the Grant -- is
// convenience; the property is that a listener is never told about work the
// database threw away, because there is no message that takes an announcement
// back.

// recorder collects what it is told, and is safe to read from the test.
type recorder struct {
	mu     sync.Mutex
	events []wallet.Event
}

func (r *recorder) listen(_ context.Context, event wallet.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) all() []wallet.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wallet.Event(nil), r.events...)
}

func (r *recorder) of(kind wallet.EventKind) []wallet.Event {
	out := []wallet.Event{}
	for _, event := range r.all() {
		if event.Kind == kind {
			out = append(out, event)
		}
	}
	return out
}

func TestEveryMovementThatCountedIsAnnouncedOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	told := &recorder{}
	service := wallet.NewWalletService(db, nil, nil, nil, told.listen)

	source := openWallet(t, service, "user-1", "main", 2)
	target := openWallet(t, service, "user-2", "main", 2)

	if opened := told.of("wallet.opened"); len(opened) != 2 {
		t.Fatalf("%d wallets were announced as opened, want 2", len(opened))
	}

	if _, err := service.Deposit(ctx, staff(), wallet.DepositRequest{
		IdempotencyKey: "seed-1", WalletID: source.ID, Amount: "50.00",
	}); err != nil {
		t.Fatalf("depositing: %v", err)
	}
	if _, err := service.Transfer(ctx, staff(), wallet.TransferRequest{
		IdempotencyKey: "move-1", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "10.00",
		Withdrawal: wallet.Leg{Meta: wallet.Meta{"why": "rent"}},
	}); err != nil {
		t.Fatalf("transferring: %v", err)
	}

	moved := told.of("wallet.money_moved")
	if len(moved) != 3 {
		t.Fatalf("%d movements were announced, want 3: one deposit and two sides of a transfer", len(moved))
	}

	// The last two are the transfer, and each says whose money it was, which
	// way it went, and what the balance became.
	pays, receives := moved[1], moved[2]
	if pays.WalletID != source.ID || pays.EntryKind != wallet.EntryWithdraw {
		t.Errorf("the paying side was announced as %s on %s", pays.EntryKind, pays.WalletID)
	}
	if pays.Balance != 4000 {
		t.Errorf("the payer was announced holding %d, want 4000", pays.Balance)
	}
	if pays.Meta["why"] != "rent" {
		t.Errorf("the event carries %q, want the metadata the movement was given", pays.Meta["why"])
	}
	if receives.WalletID != target.ID || receives.Amount != 1000 || receives.Balance != 1000 {
		t.Errorf("the receiving side was announced as %d leaving %d on %s", receives.Amount, receives.Balance, receives.WalletID)
	}
	for _, event := range moved {
		if event.ActorID != staff().ID {
			t.Errorf("the event names %q as the actor, want %q", event.ActorID, staff().ID)
		}
		if event.Tenant != tenant {
			t.Errorf("the event names %q as the customer, want %q", event.Tenant, tenant)
		}
		if event.Currency != "BRL" || event.DecimalPlaces != 2 {
			t.Errorf("the event says %s at %d places, and the wallet is BRL at 2", event.Currency, event.DecimalPlaces)
		}
		if event.At.IsZero() {
			t.Error("the event does not say when it happened")
		}
	}
}

func TestAMovementThatDidNotCountIsAnnouncedAsProposed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	told := &recorder{}
	service := wallet.NewWalletService(db, nil, nil, nil, told.listen)

	account := openWallet(t, service, "user-1", "main", 2)
	recorded, err := service.Deposit(ctx, staff(), wallet.DepositRequest{
		IdempotencyKey: "later-1", WalletID: account.ID, Amount: "10.00", Pending: true,
	})
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	if got := told.of("wallet.money_moved"); len(got) != 0 {
		t.Fatalf("%d movements were announced as having counted, and none did", len(got))
	}
	proposed := told.of("wallet.money_proposed")
	if len(proposed) != 1 {
		t.Fatalf("%d movements were announced as proposed, want 1", len(proposed))
	}
	if proposed[0].Balance != 0 {
		t.Errorf("the proposal was announced leaving %d, and the balance did not move", proposed[0].Balance)
	}

	if _, err := service.Confirm(ctx, staff(), wallet.ConfirmRequest{
		IdempotencyKey: "settle-1", OperationID: recorded.Operation.ID,
	}); err != nil {
		t.Fatalf("confirming: %v", err)
	}
	moved := told.of("wallet.money_moved")
	if len(moved) != 1 {
		t.Fatalf("%d movements were announced after the confirmation, want 1", len(moved))
	}
	if moved[0].Balance != 1000 {
		t.Errorf("the confirmation was announced leaving %d, want 1000", moved[0].Balance)
	}
}

// TestNothingIsAnnouncedForWorkTheDatabaseThrewAway is the property the whole
// arrangement exists for.
//
// A basket runs out of money on its last line, so the transaction rolls back
// with the earlier lines' balances already moved inside it. Every one of those
// movements really happened, in the database, for as long as the transaction
// lasted -- and none of them is a fact.
//
// Move the notification inside the transaction and this test fails: the events
// of the lines that got through arrive, describing money that no longer exists
// in any row.
func TestNothingIsAnnouncedForWorkTheDatabaseThrewAway(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	told := &recorder{}
	service := wallet.NewWalletService(db, nil, nil, nil, told.listen)

	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop-1", "main", 2)
	deposit(t, service, buyer.ID, "seed-1", "30.00")

	before := len(told.all())

	cheap := &item{key: "pen", wallet: shop.ID, price: 500}
	dear := &item{key: "desk", wallet: shop.ID, price: 900000}

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
		t.Fatalf("the basket answered %v, want ErrInsufficientFunds", err)
	}

	if after := told.all(); len(after) != before {
		t.Fatalf("%d events were announced for a basket nobody paid for: %v",
			len(after)-before, after[before:])
	}

	// And the balances agree with the silence.
	if got := balanceOf(t, service, buyer.ID); got != 3000 {
		t.Fatalf("the buyer holds %d, want 3000", got)
	}
}

func TestAReplayAnnouncesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	told := &recorder{}
	service := wallet.NewWalletService(db, nil, nil, nil, told.listen)

	account := openWallet(t, service, "user-1", "main", 2)
	in := wallet.DepositRequest{IdempotencyKey: "once-1", WalletID: account.ID, Amount: "10.00"}
	if _, err := service.Deposit(ctx, staff(), in); err != nil {
		t.Fatalf("depositing: %v", err)
	}

	before := len(told.of("wallet.money_moved"))
	if _, err := service.Deposit(ctx, staff(), in); err != nil {
		t.Fatalf("depositing again under the same key: %v", err)
	}
	if after := len(told.of("wallet.money_moved")); after != before {
		t.Fatalf("a replay announced %d movements, and it moved none", after-before)
	}
}

func TestAServiceWithNoListenerCostsNothing(t *testing.T) {
	t.Parallel()

	// The ordinary wiring, which is the one every existing test uses: nothing
	// is told, and nothing about the money changes because of it.
	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)

	account := openWallet(t, service, "user-1", "main", 2)
	if _, err := service.Deposit(ctx, staff(), wallet.DepositRequest{
		IdempotencyKey: "seed-1", WalletID: account.ID, Amount: "10.00",
	}); err != nil {
		t.Fatalf("depositing with no listener wired: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the wallet holds %d, want 1000", got)
	}
}
