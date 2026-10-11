package feature_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A hold is money promised and not yet taken. What it is worth is that the
// promise is kept against every other withdrawal, and that it settles once.

func hold(t *testing.T, service *wallet.WalletService, actor security.Subject, walletID, key, amount string) *wallet.Hold {
	t.Helper()
	h, err := service.Hold(context.Background(), actor, wallet.HoldRequest{IdempotencyKey: key, WalletID: walletID, Amount: amount})
	if err != nil {
		t.Fatalf("holding %s on %s: %v", amount, walletID, err)
	}
	return h
}

func heldOf(t *testing.T, service *wallet.WalletService, walletID string) wallet.Amount {
	t.Helper()
	record, err := service.Find(context.Background(), staff(), walletID)
	if err != nil {
		t.Fatal(err)
	}
	return record.Held
}

func TestAHoldSetsMoneyAsideUntilItIsCaptured(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "fund", "10.00")

	h := hold(t, service, staff(), account.ID, "hold-1", "7.00")
	if h.Status != wallet.HoldHeld || h.Amount != 700 {
		t.Fatalf("the hold is %s for %d, want held for 700", h.Status, h.Amount)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("placing a hold moved the balance to %d, want 1000", got)
	}
	if got := heldOf(t, service, account.ID); got != 700 {
		t.Fatalf("the wallet holds %d aside, want 700", got)
	}

	// What the hold set aside is out of every other withdrawal's reach, and of
	// every other hold's.
	if err := withdraw(t, service, staff(), account.ID, "spend", "3.01", false); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("a withdrawal reaching into held money returned %v, want ErrInsufficientFunds", err)
	}
	if _, err := service.Hold(context.Background(), staff(), wallet.HoldRequest{IdempotencyKey: "hold-2", WalletID: account.ID, Amount: "3.01"}); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("a second hold reaching into held money returned %v, want ErrInsufficientFunds", err)
	}
	if err := withdraw(t, service, staff(), account.ID, "spend-rest", "3.00", false); err != nil {
		t.Fatalf("a withdrawal of what is not held was refused: %v", err)
	}

	receipt, err := service.Capture(context.Background(), staff(), wallet.CaptureRequest{IdempotencyKey: "capture-1", HoldID: h.ID})
	if err != nil {
		t.Fatalf("capturing the hold: %v", err)
	}
	again, err := service.Capture(context.Background(), staff(), wallet.CaptureRequest{IdempotencyKey: "capture-1", HoldID: h.ID})
	if err != nil || again.Operation.ID != receipt.Operation.ID {
		t.Fatalf("the same capture key again answered %v (%v), want the first capture", again.Operation.ID, err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("after the capture the balance is %d, want 0", got)
	}
	if got := heldOf(t, service, account.ID); got != 0 {
		t.Fatalf("after the capture the wallet still holds %d aside", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 0 {
		t.Fatalf("the ledger adds up to %d, want 0: the capture is a withdrawal the ledger explains", got)
	}
	if _, err := service.Capture(context.Background(), staff(), wallet.CaptureRequest{IdempotencyKey: "capture-2", HoldID: h.ID}); !errors.Is(err, wallet.ErrHoldNotHeld) {
		t.Fatalf("capturing a captured hold under another key returned %v, want ErrHoldNotHeld", err)
	}
	if _, err := service.Release(context.Background(), staff(), wallet.ReleaseRequest{HoldID: h.ID, Reason: "late"}); !errors.Is(err, wallet.ErrHoldNotHeld) {
		t.Fatalf("releasing a captured hold returned %v, want ErrHoldNotHeld", err)
	}
}

func TestAReleasedHoldGivesTheMoneyBackAndWritesNothing(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 0)
	deposit(t, service, account.ID, "fund", "5")

	h := hold(t, service, person("user-1"), account.ID, "hold-1", "5")
	if again := hold(t, service, person("user-1"), account.ID, "hold-1", "5"); again.ID != h.ID {
		t.Fatalf("the same key placed a second hold %s beside %s", again.ID, h.ID)
	}
	if _, err := service.Hold(context.Background(), person("user-1"), wallet.HoldRequest{IdempotencyKey: "hold-1", WalletID: account.ID, Amount: "4"}); !errors.Is(err, wallet.ErrOperationConflict) {
		t.Fatalf("the same key for another amount returned %v, want ErrOperationConflict", err)
	}

	released, err := service.Release(context.Background(), person("user-1"), wallet.ReleaseRequest{HoldID: h.ID, Reason: "the work did not happen"})
	if err != nil || released.Status != wallet.HoldReleased {
		t.Fatalf("releasing the hold answered %v (%v), want released", released, err)
	}
	if _, err := service.Release(context.Background(), person("user-1"), wallet.ReleaseRequest{HoldID: h.ID, Reason: "again"}); err != nil {
		t.Fatalf("releasing a released hold failed: %v", err)
	}
	if got := heldOf(t, service, account.ID); got != 0 {
		t.Fatalf("after the release the wallet holds %d aside, want 0", got)
	}
	if _, err := service.Capture(context.Background(), person("user-1"), wallet.CaptureRequest{IdempotencyKey: "c", HoldID: h.ID}); !errors.Is(err, wallet.ErrHoldNotHeld) {
		t.Fatalf("capturing a released hold returned %v, want ErrHoldNotHeld", err)
	}
	statement, err := service.History(context.Background(), staff(), wallet.HistoryRequest{WalletID: account.ID, Query: data.Query{Limit: 50}})
	if err != nil || len(statement.Entries) != 1 {
		t.Fatalf("the ledger holds %d entries (%v), want only the deposit", len(statement.Entries), err)
	}

	// Somebody else's money is not theirs to hold.
	if _, err := service.Hold(context.Background(), person("user-2"), wallet.HoldRequest{IdempotencyKey: "x", WalletID: account.ID, Amount: "1"}); err == nil {
		t.Fatal("a person held money on somebody else's wallet")
	}
}

func TestAnExpiredHoldIsReleasedAndNeverCaptured(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 0)
	deposit(t, service, account.ID, "fund", "3")

	if _, err := service.Hold(context.Background(), staff(), wallet.HoldRequest{IdempotencyKey: "past", WalletID: account.ID, Amount: "1", ExpiresAt: time.Now().Add(-time.Minute)}); err == nil {
		t.Fatal("a hold that expires before it is placed was placed")
	}
	h, err := service.Hold(context.Background(), staff(), wallet.HoldRequest{IdempotencyKey: "soon", WalletID: account.ID, Amount: "2", ExpiresAt: time.Now().Add(50 * time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := service.Capture(context.Background(), staff(), wallet.CaptureRequest{IdempotencyKey: "late", HoldID: h.ID}); !errors.Is(err, wallet.ErrHoldExpired) {
		t.Fatalf("capturing an expired hold returned %v, want ErrHoldExpired", err)
	}
	if _, err := service.ReleaseExpired(context.Background(), person("user-1"), time.Now(), 10); err == nil {
		t.Fatal("a holder swept the tenant's expired holds")
	}
	n, err := service.ReleaseExpired(context.Background(), staff(), time.Now(), 10)
	if err != nil || n != 1 {
		t.Fatalf("the sweep released %d (%v), want 1", n, err)
	}
	if got := heldOf(t, service, account.ID); got != 0 {
		t.Fatalf("after the sweep the wallet holds %d aside", got)
	}
	expired, err := service.HoldsOf(context.Background(), staff(), account.ID, wallet.HoldExpired, 10)
	if err != nil || len(expired) != 1 {
		t.Fatalf("the wallet lists %d expired holds (%v), want 1", len(expired), err)
	}
}

func TestTwoHoldsCannotBothTakeTheLastOfTheMoney(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 0)
	deposit(t, service, account.ID, "fund", "1")

	var wg sync.WaitGroup
	placed := make([]error, writers)
	for i := range placed {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, placed[i] = service.Hold(context.Background(), staff(), wallet.HoldRequest{
				IdempotencyKey: "race-" + string(rune('a'+i)), WalletID: account.ID, Amount: "1",
			})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range placed {
		if err == nil {
			ok++
		}
	}
	if ok != 1 || heldOf(t, service, account.ID) != 1 {
		t.Fatalf("%d holds were placed on one unit of money and %d is held, want 1 and 1", ok, heldOf(t, service, account.ID))
	}
}

func TestTheNewestPageOfTheLedgerComesFirstWhenAsked(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 0)
	for _, key := range []string{"a", "b", "c", "d", "e"} {
		deposit(t, service, account.ID, key, "1")
	}
	first, err := service.History(context.Background(), staff(), wallet.HistoryRequest{WalletID: account.ID, Newest: true, Query: data.Query{Limit: 2}})
	if err != nil || len(first.Entries) != 2 || first.Entries[0].Sequence != 5 || first.Entries[1].Sequence != 4 {
		t.Fatalf("the newest page is %v (%v), want sequences 5 and 4", first.Entries, err)
	}
	next, err := service.History(context.Background(), staff(), wallet.HistoryRequest{WalletID: account.ID, Newest: true, Query: data.Query{Limit: 2, Cursor: first.Entries[1].ID}})
	if err != nil || len(next.Entries) != 2 || next.Entries[0].Sequence != 3 {
		t.Fatalf("the page after it is %v (%v), want sequences 3 and 2", next.Entries, err)
	}
}

func TestAnOperatorIsDerivedOnlyFromAGrantAPolicyIssued(t *testing.T) {
	t.Parallel()

	if _, err := wallet.OperatorFor(security.Grant{}); err == nil {
		t.Fatal("an operator was derived from the zero Grant")
	}
	g := security.SystemGrant("app.credits", tenant)
	operator, err := wallet.OperatorFor(g)
	if err != nil {
		t.Fatal(err)
	}
	if operator.Tenant != tenant || !operator.HasRole(wallet.OperatorRole) {
		t.Fatalf("the operator is %+v, want the operator role in %s", operator, tenant)
	}

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 0)
	if _, err := service.Deposit(context.Background(), operator, wallet.DepositRequest{IdempotencyKey: "k", WalletID: account.ID, Amount: "2"}); err != nil {
		t.Fatalf("the derived operator could not deposit in its tenant: %v", err)
	}
	other, err := wallet.OperatorFor(security.SystemGrant("app.credits", "globex"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deposit(context.Background(), other, wallet.DepositRequest{IdempotencyKey: "k2", WalletID: account.ID, Amount: "2"}); err == nil {
		t.Fatal("an operator of another tenant reached this tenant's wallet")
	}
}
