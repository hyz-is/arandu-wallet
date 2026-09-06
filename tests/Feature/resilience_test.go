package feature_test

import (
	"context"
	"errors"

	"testing"

	"github.com/arandu-io/framework/data"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A balance that stopped matching the ledger explaining it, and what this
// package does about that.
//
// The interesting half needs PostgreSQL: a wallet that disagrees with its own
// ledger only matters once something else is trying to spend it, and SQLite
// serializes writers so it cannot show a guard being read at the write rather
// than before it. The refusals that need no contention run anywhere.

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
