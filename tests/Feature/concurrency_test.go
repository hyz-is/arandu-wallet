package feature_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What is proved here is the only thing worth proving about money that several
// people can spend at once: that the sum of what left is never more than what
// was there. Everything else in this package is arrangement.
//
// These run against SQLite, and what they measure is the outcome rather than
// the mechanism. SQLite serializes writers -- one transaction holds the whole
// database -- so the invariant below comes out true whether the balance is
// guarded by a predicate on the update or read and checked in Go. That was
// measured, not assumed: with the guard replaced by a read-and-check, every
// test in this file still passes.
//
// The mechanism is proved in postgres_test.go, on an engine whose transactions
// really interleave, where the same mutation drives the balance negative. Read
// the two files together: this one says the package behaves, that one says why.

// withdrawers is how many goroutines race for the same balance.
//
// Far more than the balance can serve, so that all but a few of them are
// deciding against a balance that changed while they were queued. A number
// equal to what the balance affords would be a number under which every
// goroutine succeeds, and a run in which nothing is refused refuses to say
// anything.
//
// It is shared with the PostgreSQL suite, where it is the number that made the
// unguarded version fail: at two hundred against a balance of twenty, removing
// the predicate let twenty-six through and drove the column to minus six.
const withdrawers = 200

func TestConcurrentWithdrawalsNeverExceedTheBalance(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	// Twenty units in, one unit per withdrawal, and two hundred goroutines
	// asking. Exactly twenty may succeed.
	const (
		opening    = wallet.Amount(2000)
		each       = wallet.Amount(100)
		affordable = int(opening / each)
	)
	deposit(t, service, account.ID, "opening", "20.00")

	var (
		start     sync.WaitGroup
		done      sync.WaitGroup
		succeeded int64
		mu        sync.Mutex
		refusals  []error
	)
	start.Add(1)
	done.Add(withdrawers)

	for i := range withdrawers {
		go func() {
			defer done.Done()
			// Every goroutine waits on the same signal, so they arrive
			// together rather than in the order they were started.
			start.Wait()

			_, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
				IdempotencyKey: fmt.Sprintf("withdraw-%d", i),
				WalletID:       account.ID,
				Amount:         "1.00",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, wallet.ErrInsufficientFunds):
			default:
				refusals = append(refusals, err)
			}
		}()
	}
	start.Done()
	done.Wait()

	// An error that is neither success nor "not enough money" is a failure of
	// the mechanism, not of the balance, and it has to be reported as one.
	for _, err := range refusals {
		t.Errorf("a withdrawal failed for a reason that is not the balance: %v", err)
	}

	if succeeded != int64(affordable) {
		t.Fatalf("%d withdrawals of %d succeeded against an opening balance of %d, want exactly %d",
			succeeded, each, opening, affordable)
	}

	// The invariant, stated as money: what is left is what was there minus what
	// left, and it is never negative.
	balance := balanceOf(t, service, account.ID)
	if balance < 0 {
		t.Fatalf("the balance went negative: %d", balance)
	}
	if want := opening - wallet.Amount(succeeded)*each; balance != want {
		t.Fatalf("the balance is %d, want %d", balance, want)
	}

	// And the ledger says the same thing the balance column does. The column is
	// a projection, so a run in which the two disagree is a run in which an
	// update and its entry came apart -- which is the failure a transaction
	// exists to make impossible.
	if ledger := ledgerOf(t, service, account.ID); ledger != balance {
		t.Fatalf("the ledger sums to %d and the balance column says %d", ledger, balance)
	}
}

func TestABalanceReadBeforeTheWriteIsRevalidatedByIt(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "10.00")

	// The defect this is written against: read the balance, decide the
	// withdrawal fits, and let somebody else empty the wallet in between. Here
	// the two are sequential, so what is shown is that a balance read through
	// the public surface is not what the withdrawal is decided on -- the
	// interleaved form of the same question is in postgres_test.go.
	seen := balanceOf(t, service, account.ID)
	if seen != 1000 {
		t.Fatalf("the balance read before the race is %d, want 1000", seen)
	}

	if _, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
		IdempotencyKey: "drain", WalletID: account.ID, Amount: "10.00",
	}); err != nil {
		t.Fatalf("draining the wallet: %v", err)
	}

	// Now spend what was seen a moment ago. The guard is a predicate on the
	// update, so it is evaluated against the row as it is now.
	_, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
		IdempotencyKey: "stale", WalletID: account.ID, Amount: seen.Format(2),
	})
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("a withdrawal of a balance read earlier returned %v, want ErrInsufficientFunds", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d, want 0", got)
	}
}

func TestTheSameIdempotencyKeyCreditsOnce(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	first := deposit(t, service, account.ID, "key-1", "10.00")
	second := deposit(t, service, account.ID, "key-1", "10.00")

	if first.Replayed {
		t.Fatal("the first deposit reported itself as a replay")
	}
	if !second.Replayed {
		t.Fatal("the second deposit under the same key did not report itself as a replay")
	}
	if second.Operation.ID != first.Operation.ID {
		t.Fatalf("the replay answered with operation %s, want the first one, %s",
			second.Operation.ID, first.Operation.ID)
	}
	if len(second.Entries) != 1 || second.Entries[0].ID != first.Entries[0].ID {
		t.Fatal("the replay answered with entries other than the first call's")
	}

	// Measured where it matters.
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d after the same request twice, want 1000", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the ledger sums to %d after the same request twice, want 1000", got)
	}
}

func TestConcurrentRequestsUnderOneKeyCreditOnce(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	// The retry that arrives before the first request has finished. A lookup
	// alone would let both through, because both would look and find nothing;
	// what stops the second is the unique index the operation row is inserted
	// under, and the loser answering with the winner's receipt.
	const callers = 50

	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		written int
		failed  []error
	)
	start.Add(1)
	done.Add(callers)

	for range callers {
		go func() {
			defer done.Done()
			start.Wait()

			receipt, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
				IdempotencyKey: "one-key", WalletID: account.ID, Amount: "10.00",
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, err)
				return
			}
			if !receipt.Replayed {
				written++
			}
		}()
	}
	start.Done()
	done.Wait()

	for _, err := range failed {
		t.Errorf("a call under a shared key failed: %v", err)
	}
	if written != 1 {
		t.Errorf("%d of %d callers reported that they moved the money, want exactly 1", written, callers)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d after %d identical requests, want 1000", got, callers)
	}
	if got := ledgerOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the ledger sums to %d, want 1000", got)
	}
}

func TestAnIdempotencyKeyReusedForAnotherRequestIsRefused(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "key-1", "10.00")

	// One key cannot be the name of two different requests. Answering the
	// deposit's receipt to a withdrawal would be answering a question nobody
	// asked, and answering nothing would be moving money under a name that
	// already means something else.
	_, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
		IdempotencyKey: "key-1", WalletID: account.ID, Amount: "1.00",
	})
	if !errors.Is(err, wallet.ErrOperationConflict) {
		t.Fatalf("reusing a deposit's key for a withdrawal returned %v, want ErrOperationConflict", err)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d, want 1000", got)
	}
}

func TestTransfersInOppositeDirectionsSettleWithoutDeadlock(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	left := openWallet(t, service, "user-1", "main", 2)
	right := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, left.ID, "opening-left", "50.00")
	deposit(t, service, right.ID, "opening-right", "50.00")

	// Two hundred transfers, half in each direction, between the same pair. On
	// an engine that takes row locks this is the shape that deadlocks when the
	// two sides lock in the order the request named them; the service orders by
	// wallet identifier instead, so both directions take the same two locks in
	// the same sequence.
	//
	// SQLite takes one lock for the whole database and cannot produce that
	// deadlock, so what this run shows is the outcome: every transfer settles,
	// none is lost and none is doubled. The deadlock itself is in
	// postgres_test.go, where removing the ordering produces SQLSTATE 40P01.
	const pairs = 100

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
		fails []error
	)
	start.Add(1)
	done.Add(pairs * 2)

	send := func(from, to, key string) {
		defer done.Done()
		start.Wait()
		_, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
			IdempotencyKey: key, FromWalletID: from, ToWalletID: to, Amount: "1.00",
		})
		if err != nil {
			mu.Lock()
			fails = append(fails, err)
			mu.Unlock()
		}
	}

	for i := range pairs {
		go send(left.ID, right.ID, fmt.Sprintf("left-to-right-%d", i))
		go send(right.ID, left.ID, fmt.Sprintf("right-to-left-%d", i))
	}
	start.Done()
	done.Wait()

	for _, err := range fails {
		t.Errorf("a transfer failed: %v", err)
	}

	// Equal traffic in both directions, so both balances come back to where
	// they started -- and the total is conserved whatever the individual
	// balances are, which is the property that survives a partial failure.
	leftBalance := balanceOf(t, service, left.ID)
	rightBalance := balanceOf(t, service, right.ID)
	if total := leftBalance + rightBalance; total != 10000 {
		t.Fatalf("the two wallets hold %d together, want 10000", total)
	}
	if leftBalance != 5000 || rightBalance != 5000 {
		t.Fatalf("the wallets hold %d and %d, want 5000 each", leftBalance, rightBalance)
	}
	if ledger := ledgerOf(t, service, left.ID); ledger != leftBalance {
		t.Fatalf("the left ledger sums to %d and its balance column says %d", ledger, leftBalance)
	}
	if ledger := ledgerOf(t, service, right.ID); ledger != rightBalance {
		t.Fatalf("the right ledger sums to %d and its balance column says %d", ledger, rightBalance)
	}
}

func TestConcurrentReversalsOfOneOperationUndoItOnce(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)
	credited := deposit(t, service, account.ID, "opening", "10.00")

	// Each caller uses its own idempotency key, so nothing here is a replay:
	// these are genuinely different requests to undo the same movement, and the
	// unique index on what an operation settles is what refuses all but one.
	const callers = 25

	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		undone  int
		refused int
		other   []error
	)
	start.Add(1)
	done.Add(callers)

	for i := range callers {
		go func() {
			defer done.Done()
			start.Wait()

			_, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
				IdempotencyKey: fmt.Sprintf("reverse-%d", i),
				OperationID:    credited.Operation.ID,
				Reason:         "chargeback",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				undone++
			case errors.Is(err, wallet.ErrAlreadyReversed):
				refused++
			default:
				other = append(other, err)
			}
		}()
	}
	start.Done()
	done.Wait()

	for _, err := range other {
		t.Errorf("a reversal failed for a reason other than being second: %v", err)
	}
	if undone != 1 {
		t.Errorf("%d of %d reversals succeeded, want exactly 1", undone, callers)
	}
	if refused != callers-undone {
		t.Errorf("%d reversals were refused as already done, want %d", refused, callers-undone)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d after one deposit and its reversal, want 0", got)
	}
}

func TestConcurrentDepositsKeepTheLedgerAndTheBalanceTogether(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	// The other direction of the same claim. Nothing here can be refused, so
	// what is measured is that every credit landed exactly once and that the
	// projection did not fall behind the ledger under load.
	const credits = 150

	var start, done sync.WaitGroup
	var mu sync.Mutex
	var fails []error
	start.Add(1)
	done.Add(credits)

	for i := range credits {
		go func() {
			defer done.Done()
			start.Wait()
			_, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
				IdempotencyKey: fmt.Sprintf("credit-%d", i), WalletID: account.ID, Amount: "0.07",
			})
			if err != nil {
				mu.Lock()
				fails = append(fails, err)
				mu.Unlock()
			}
		}()
	}
	start.Done()
	done.Wait()

	for _, err := range fails {
		t.Errorf("a deposit failed: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != credits*7 {
		t.Fatalf("the balance is %d after %d credits of 7, want %d", got, credits, credits*7)
	}
	if got := ledgerOf(t, service, account.ID); got != credits*7 {
		t.Fatalf("the ledger sums to %d, want %d", got, credits*7)
	}
}
