package feature_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/hesape/log"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What an engine does to a transaction it refuses, and what this package does
// about it.
//
// Three claims live here, and none of them can be shown on SQLite. It takes one
// lock for the whole database, so it produces no deadlock between two rows; it
// serializes writers, so it has no isolation level for a guard to be read
// against; and a balance that stopped matching its ledger is only interesting
// once something else is trying to spend it. Every test below therefore needs
// PostgreSQL and skips without it.

// TestADeadlockIsSentAgainAndTheMovementSurvives holds the retry.
//
// The deadlock is a real one and is arranged rather than simulated. A
// connection of the test's own takes the row lock on the wallet a transfer
// reaches second and holds it; the transfer takes the first and blocks; the
// test's connection then asks for the first and the cycle is closed. PostgreSQL
// breaks it by killing one of the two, and the one it kills is the transfer:
// the victim is whoever's deadlock_timeout expires first, and the test's
// connection sets its own to ten seconds so that it is never that one.
//
// So the transfer is refused by the engine with 40P01, having written nothing.
// What the assertion below says is that the caller never sees it: the money
// moved, once, and the ledger says so.
//
// Removing the classification in service.go -- making conflicted answer false
// -- fails it with the driver's own sentence, which is the mutation this test
// exists for.
func TestADeadlockIsSentAgainAndTheMovementSurvives(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := postgres(t)
	service := wallet.NewWalletService(handle, nil, nil, nil)

	left := openWallet(t, service, "user-1", "main", 2)
	right := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, left.ID, "opening-left", "50.00")
	deposit(t, service, right.ID, "opening-right", "50.00")

	// The order the service takes the two locks in, which is by identifier and
	// never the order the request named them.
	first, second := left.ID, right.ID
	if second < first {
		first, second = second, first
	}

	blocker, err := handle.Unwrap().Conn(ctx)
	if err != nil {
		t.Fatalf("opening the connection that holds the lock: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	// Ten seconds, so that this connection is never the one PostgreSQL kills.
	// The victim of a deadlock is whichever waiter notices the cycle first, and
	// that is decided by this setting.
	if _, err := blocker.ExecContext(ctx, `SET deadlock_timeout = '10s'`); err != nil {
		t.Fatalf("setting the deadlock timeout: %v", err)
	}

	held, err := blocker.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("beginning the transaction that holds the lock: %v", err)
	}
	if _, err := held.ExecContext(ctx, `SELECT id FROM wallets WHERE id = $1 FOR UPDATE`, second); err != nil {
		_ = held.Rollback()
		t.Fatalf("taking the row lock: %v", err)
	}

	var (
		moving  sync.WaitGroup
		receipt wallet.Receipt
		moved   error
	)
	moving.Add(1)
	go func() {
		defer moving.Done()
		receipt, moved = service.Transfer(ctx, staff(), wallet.TransferRequest{
			IdempotencyKey: "one-transfer",
			FromWalletID:   first,
			ToWalletID:     second,
			Amount:         "1.00",
		})
	}()

	// Long enough for the transfer to have taken the first lock and to be
	// waiting on the second. It is a wait and not a signal because what is being
	// waited for is a lock inside the engine, which nothing in this process can
	// observe.
	time.Sleep(500 * time.Millisecond)

	// The other half of the cycle. It blocks until PostgreSQL kills the
	// transfer, which is what releases the first row.
	if _, err := held.ExecContext(ctx, `SELECT id FROM wallets WHERE id = $1 FOR UPDATE`, first); err != nil {
		_ = held.Rollback()
		t.Fatalf("the connection holding the lock was the one PostgreSQL killed, so this test proved nothing about the retry: %v", err)
	}
	if err := held.Commit(); err != nil {
		t.Fatalf("releasing the locks: %v", err)
	}

	moving.Wait()

	if moved != nil {
		t.Fatalf("the transfer answered %v, and a deadlock the engine broke is exactly what is supposed to be sent again", moved)
	}
	if receipt.Replayed {
		t.Error("the transfer answered with a replay, so the money moved on an attempt this test did not arrange")
	}
	if got := balanceOf(t, service, first); got != 4900 {
		t.Errorf("the wallet that paid holds %d, want 4900", got)
	}
	if got := balanceOf(t, service, second); got != 5100 {
		t.Errorf("the wallet that was paid holds %d, want 5100", got)
	}
	for _, id := range []string{first, second} {
		if ledger, balance := ledgerOf(t, service, id), balanceOf(t, service, id); ledger != balance {
			t.Errorf("the ledger of %s sums to %d and its balance column says %d", id, ledger, balance)
		}
	}
}

// serializableDefault is a server whose default has been tightened, which is
// what an operator who wanted stronger guarantees leaves behind on a cluster.
//
// libpq-style startup options, which pgx reads off the connection string.
var serializableDefault = map[string]string{"options": "-c default_transaction_isolation=serializable"}

// TestThisPackageNamesTheIsolationLevelOfEveryTransactionItOpens holds the
// level, by reading the statements that were issued.
//
// The pool here is opened against a server whose default is serializable. What
// has to be true is that the transaction does not run at that level: the guard
// on a balance was written against read committed, where an update
// re-evaluates its predicate against the row the other transaction left, and a
// level chosen by whoever configured the server is not the level a guard was
// written for.
//
// It asserts the statement rather than an outcome, and that is deliberate. The
// outcome is the same either way, because the retry beside this absorbs the
// conflicts a stricter level produces -- which is exactly why the level has to
// be checked directly. The test below asserts the outcome, and passes with the
// naming removed; this one does not.
func TestThisPackageNamesTheIsolationLevelOfEveryTransactionItOpens(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(postgresWith(t, serializableDefault), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	collected := log.NewCollector("isolation")
	ctx := log.WithCollector(context.Background(), collected)
	if _, err := service.Deposit(ctx, staff(), wallet.DepositRequest{
		IdempotencyKey: "opening", WalletID: account.ID, Amount: "10.00",
	}); err != nil {
		t.Fatalf("depositing: %v", err)
	}

	statements := collected.Queries()
	named := -1
	for i, query := range statements {
		if strings.Contains(query.SQL, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED") {
			named = i
			break
		}
	}
	if named < 0 {
		t.Fatalf("no statement named the isolation level, so this transaction ran at whatever the server was configured for; %d statements were issued", len(statements))
	}
	if named+1 >= len(statements) {
		t.Fatal("the isolation level was named and nothing followed it, so no transaction was opened around it")
	}

	// It has to be the first statement of the transaction, because that is the
	// only place an engine takes it. What follows is the operation row, which is
	// the first thing every movement writes.
	next := statements[named+1].SQL
	if !strings.Contains(next, "wallet_operations") {
		t.Errorf("the statement after the isolation level is %q, and it has to be the operation row: anything in between means the level was named after the transaction had already read something", next)
	}
}

// TestTheGuardIsExactWhenTheServerDefaultIsTightened holds the outcome at a
// server somebody configured for serializable.
//
// It passes whether or not this package names its own level, and that is not a
// gap in it: with the level unnamed the transactions run serializable, the
// second writer of a row is aborted rather than re-deciding, and the retry
// turns those aborts back into the same answer. What it holds is that the whole
// arrangement -- guard, level and retry together -- still spends exactly the
// balance and no more. The statement itself is asserted above.
func TestTheGuardIsExactWhenTheServerDefaultIsTightened(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(postgresWith(t, serializableDefault), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	const (
		opening    = wallet.Amount(2000)
		each       = wallet.Amount(100)
		affordable = int(opening / each)
	)
	deposit(t, service, account.ID, "opening", "20.00")

	var (
		start     sync.WaitGroup
		done      sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		conflicts int
		other     []error
	)
	start.Add(1)
	done.Add(withdrawers)

	for i := range withdrawers {
		go func() {
			defer done.Done()
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
			case errors.Is(err, wallet.ErrConcurrencyConflict):
				conflicts++
			default:
				other = append(other, err)
			}
		}()
	}
	start.Done()
	done.Wait()

	for _, err := range other {
		t.Errorf("a withdrawal failed for a reason that is neither the balance nor a conflict: %v", err)
	}
	if conflicts > 0 {
		t.Errorf("%d withdrawals ran out of attempts against a conflict", conflicts)
	}
	if succeeded != affordable {
		t.Errorf("%d withdrawals succeeded against a balance of %d, want exactly %d", succeeded, opening, affordable)
	}

	balance := balanceOf(t, service, account.ID)
	if balance < 0 {
		t.Fatalf("the balance went negative: %d", balance)
	}
	if want := opening - wallet.Amount(succeeded)*each; balance != want {
		t.Fatalf("the balance is %d, want %d", balance, want)
	}
	if ledger := ledgerOf(t, service, account.ID); ledger != balance {
		t.Fatalf("the ledger sums to %d and the balance column says %d", ledger, balance)
	}
}

// TestAWalletThatStopsAddingUpFreezesAndIsRebuiltByAppending holds the repair.
//
// The defect is written by hand, because nothing in this package produces it:
// five units appear in the balance column with no entry behind them, which is
// the shape a lost write leaves. What follows is the whole of the answer --
// the reconciliation reports the difference, the wallet stops being served, and
// the rebuild closes it by appending one settled entry and touching no column
// but the freeze.
//
// The assertions that matter most are the two negatives. The balance column is
// the same number before and after the rebuild, so nothing was corrected in
// place; and the entry count went up by exactly one, so the repair is a row
// somebody can read rather than a value somebody changed.
func TestAWalletThatStopsAddingUpFreezesAndIsRebuiltByAppending(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := postgres(t)
	service := wallet.NewWalletService(handle, nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "20.00")

	diverge(t, handle, account.ID, 500)

	report, err := service.Reconcile(ctx, staff(), account.ID)
	if err != nil {
		t.Fatalf("reconciling: %v", err)
	}
	if report.Balanced() {
		t.Fatal("the reconciliation says the wallet adds up, and five units were just added to the column with no entry behind them")
	}
	if got := report.Difference(); got != 500 {
		t.Errorf("the difference is %d, want 500", got)
	}
	if !report.Frozen {
		t.Fatal("the wallet was not frozen, so it goes on paying out of a balance nothing explains")
	}

	// The freeze is read by the statement that would move the money.
	_, err = service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
		IdempotencyKey: "after-the-freeze", WalletID: account.ID, Amount: "1.00",
	})
	if !errors.Is(err, wallet.ErrWalletFrozen) {
		t.Fatalf("a withdrawal from a frozen wallet answered %v, want ErrWalletFrozen", err)
	}

	before := balanceOf(t, service, account.ID)
	entriesBefore := report.Entries

	receipt, err := service.Rebuild(ctx, staff(), wallet.RebuildRequest{
		IdempotencyKey: "rebuild-1",
		WalletID:       account.ID,
		Reason:         "a lost write, found by the audit",
	})
	if err != nil {
		t.Fatalf("rebuilding: %v", err)
	}
	if receipt.Operation.Kind != wallet.OperationAdjustment {
		t.Errorf("the rebuild recorded a %q, want an adjustment", receipt.Operation.Kind)
	}
	if receipt.Operation.Reason == "" {
		t.Error("the adjustment carries no reason, and a ledger row nobody can account for is what this exists to avoid")
	}
	if len(receipt.Entries) != 1 {
		t.Fatalf("the rebuild wrote %d entries, want exactly 1", len(receipt.Entries))
	}
	appended := receipt.Entries[0]
	if appended.Kind != wallet.EntryDeposit || appended.Amount != 500 {
		t.Errorf("the appended entry is a %q of %d, want a deposit of 500", appended.Kind, appended.Amount)
	}
	if !appended.Settled {
		t.Error("the appended entry did not settle, so it adds nothing to the sum it exists to close")
	}
	if appended.BalanceAfter != before {
		t.Errorf("the appended entry records a balance of %d, want the %d the column already held", appended.BalanceAfter, before)
	}

	// The column was not touched. This is the assertion the whole design is for:
	// a repair that edited the balance would be the one write in this package
	// that leaves no row behind.
	if after := balanceOf(t, service, account.ID); after != before {
		t.Errorf("the balance column moved from %d to %d, and a rebuild appends rather than corrects", before, after)
	}
	if ledger := ledgerOf(t, service, account.ID); ledger != before {
		t.Errorf("the ledger sums to %d and the balance column says %d", ledger, before)
	}

	closed, err := service.Reconcile(ctx, staff(), account.ID)
	if err != nil {
		t.Fatalf("reconciling after the rebuild: %v", err)
	}
	if !closed.Balanced() {
		t.Error("the wallet still does not add up after the rebuild")
	}
	if closed.Frozen {
		t.Error("the wallet is still frozen after the difference was closed")
	}
	if closed.Entries != entriesBefore+1 {
		t.Errorf("the ledger holds %d entries, want the %d it had plus the one the rebuild appended", closed.Entries, entriesBefore+1)
	}

	// And it serves again.
	if _, err := service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
		IdempotencyKey: "after-the-rebuild", WalletID: account.ID, Amount: "1.00",
	}); err != nil {
		t.Fatalf("withdrawing after the rebuild: %v", err)
	}
}

// TestARebuildRefusesAWalletNothingIsWrongWith holds the two refusals that keep
// an adjustment from being a way to write a ledger row for any reason.
//
// A wallet nobody froze is refused, because the difference it would be adjusted
// by can still change under the read that measures it. And a frozen wallet
// whose ledger already adds up is refused too, and stays frozen: it is what
// somebody has just "fixed" by editing the column, and lifting the freeze
// without a row would be lifting it for a reason nobody recorded.
func TestARebuildRefusesAWalletNothingIsWrongWith(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := database(t)
	service := wallet.NewWalletService(handle, nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "20.00")

	if _, err := service.Rebuild(ctx, staff(), wallet.RebuildRequest{
		IdempotencyKey: "too-early", WalletID: account.ID, Reason: "nothing is wrong",
	}); !errors.Is(err, wallet.ErrWalletNotFrozen) {
		t.Fatalf("rebuilding a wallet nobody froze answered %v, want ErrWalletNotFrozen", err)
	}

	diverge(t, handle, account.ID, 500)
	if report, err := service.Reconcile(ctx, staff(), account.ID); err != nil {
		t.Fatalf("reconciling: %v", err)
	} else if !report.Frozen {
		t.Fatal("the wallet was not frozen by a difference of five units")
	}

	// Somebody puts the column back by hand, which closes the difference and
	// records nothing.
	diverge(t, handle, account.ID, -500)

	if _, err := service.Rebuild(ctx, staff(), wallet.RebuildRequest{
		IdempotencyKey: "nothing-to-do", WalletID: account.ID, Reason: "already put back",
	}); !errors.Is(err, wallet.ErrLedgerBalanced) {
		t.Fatalf("rebuilding a wallet that adds up answered %v, want ErrLedgerBalanced", err)
	}
	if _, err := service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
		IdempotencyKey: "still-frozen", WalletID: account.ID, Amount: "1.00",
	}); !errors.Is(err, wallet.ErrWalletFrozen) {
		t.Fatalf("a withdrawal answered %v, and a wallet whose freeze nothing lifted is still frozen", err)
	}
}

// TestOnlyAnOperatorReconcilesOrRebuilds holds who may do either.
//
// A holder may read the whole of their own ledger and may not conclude
// anything about it that writes. The two are different decisions and the
// package asks about them separately.
func TestOnlyAnOperatorReconcilesOrRebuilds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	if _, err := service.History(ctx, person("user-1"), wallet.HistoryRequest{WalletID: account.ID}); err != nil {
		t.Fatalf("a holder reading their own ledger: %v", err)
	}
	if _, err := service.Reconcile(ctx, person("user-1"), account.ID); err == nil {
		t.Error("a holder reconciled their own wallet, and what that writes is a wallet that no longer moves")
	}
	if _, err := service.Rebuild(ctx, person("user-1"), wallet.RebuildRequest{
		IdempotencyKey: "mine", WalletID: account.ID, Reason: "i say so",
	}); err == nil {
		t.Error("a holder rebuilt their own wallet, and what that writes is a ledger row no request produced")
	}
}

// diverge moves a balance column without writing the entry that explains it,
// which is the defect this package cannot produce and has to be able to answer.
//
// It goes through the handle rather than the service on purpose: every path the
// service offers keeps the two in step, so the only way to arrange the disagreement
// is to go round it.
func diverge(t *testing.T, handle *data.DB, walletID string, by int64) {
	t.Helper()

	if _, err := handle.ExecContext(context.Background(),
		`UPDATE wallets SET balance = balance + ? WHERE id = ?`, by, walletID); err != nil {
		t.Fatalf("moving the balance column by hand: %v", err)
	}
}
