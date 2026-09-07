package wallet

import (
	"context"

	"github.com/arandu-io/framework/security"
)

// Reconciliation is what one wallet's ledger adds up to, beside what its
// balance column says.
//
// The balance is a projection of the entries, so the two have to agree; this is
// the value that says whether they do, and by how much when they do not.
//
// It is a conclusion about one state of the wallet and not about the wallet as
// it is now: the entries are read up to the position the wallet held when the
// read began, so what is compared against Wallet.Balance is exactly the set of
// rows that produced it. A wallet that moves afterwards moves both numbers by
// the same amount, so Difference is what it was.
//
// Nothing here is corrected. What the difference does cause is the freeze --
// the wallet stops being served, because a balance this package cannot explain
// is a balance it should not be paying out of. Closing the difference is
// Rebuild, and it appends the row that explains it rather than editing
// anything.
type Reconciliation struct {
	// Wallet is the wallet that was read.
	Wallet Wallet
	// Settled is the sum of the movements that counted, which is what the
	// balance column has to equal.
	Settled Amount
	// Proposed is the sum of the movements that are waiting, read as what they
	// would move. Nothing checks it against anything: it is here so that
	// somebody looking at a difference can see how much is in flight.
	Proposed Amount
	// Entries is how many rows were read.
	Entries int
	// LastBalanceAfter is what the newest movement recorded the balance as, and
	// LastSequence its position. The first has to equal the balance column too,
	// which is the second half of the check: a ledger can sum correctly and
	// still have been written in an order nobody can read down.
	LastBalanceAfter Amount
	LastSequence     int64
	// Gaps is how many times the sequence skipped a number. A gap is not proof
	// of a lost write -- a transaction that rolled back after taking a number
	// leaves one -- but it is where somebody looks first.
	Gaps int
	// Frozen reports that this wallet is no longer served.
	//
	// It is what Reconcile leaves behind when Difference is not zero, and it is
	// read by the statement that would move the money rather than by anything
	// before it. Rebuild is what lifts it, in the same statement that appends
	// the row closing the difference.
	Frozen bool
}

// Reconcile reads a whole ledger, reports whether it adds up to the balance
// beside it, and stops the wallet being served where it does not.
//
// It asks the policy the same two questions every other path does. The action
// is WalletReconcile and not WalletHistory: what this leaves behind is a wallet
// that no longer moves, so it is a decision about somebody's money rather than
// a reading of it, and the person whose money it is is not the person who makes
// it.
//
// It reads the entries in pages rather than in one statement, because a ledger
// only grows and the wallet worth checking is the one with the longest one. What
// it costs is a statement per page and nothing held in memory but the running
// totals.
//
// The pages stop at the position the wallet held when the read began. That is
// what makes the answer a conclusion rather than a race: the entries summed are
// exactly the ones that produced the balance read beside them, and a movement
// arriving during the scan is left for the next one. It also makes the
// difference stable -- every later movement adds the same amount to both sides,
// so what is found here is what Rebuild will find.
//
// A difference freezes the wallet. Nothing is repaired: a number this package
// quietly corrected would be a defect nobody ever heard about, in the one table
// where the defect is money. What it does instead is refuse to keep paying out
// of a balance it cannot explain, which is the half the report alone was
// missing.
//
// A ledger that sums correctly and records the wrong running balance on its
// last row is reported and not frozen. Balanced says so, and it is the wider
// check; but no row this package can append would close that, and a freeze
// nothing can lift is a wallet taken out of service for good.
func (s *WalletService) Reconcile(ctx context.Context, actor security.Subject, walletID string) (Reconciliation, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletReconcile, Wallet{})
	if err != nil {
		return Reconciliation{}, err
	}

	holder, err := Wallets(s.db).NewQuery().WhereKey(walletID).First(ctx, g)
	if err != nil {
		return Reconciliation{}, err
	}
	if holder == nil {
		return Reconciliation{}, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletReconcile, *holder); err != nil {
		return Reconciliation{}, err
	}

	out, err := s.walk(ctx, g, *holder)
	if err != nil {
		return Reconciliation{}, err
	}
	if out.Difference() == 0 {
		return out, nil
	}
	if err := s.freeze(ctx, g, holder.ID); err != nil {
		return Reconciliation{}, err
	}
	out.Frozen = true
	return out, nil
}

// RebuildRequest is what closing a wallet's difference takes.
type RebuildRequest struct {
	// IdempotencyKey is the caller's name for this request. It is the
	// adjustment's own key, and sending it twice adjusts once.
	IdempotencyKey string
	// WalletID is the wallet whose ledger is being made to explain its balance.
	WalletID string
	// Reason is what the adjustment is recorded as. It is required, for the
	// reason a reversal's is: a row in a ledger that says money appeared and
	// does not say why is a row nobody can account for later.
	Reason string
	// Meta is what the application attaches to the adjustment.
	Meta Meta
}

// Rebuild closes the difference between a wallet's ledger and its balance, by
// appending the entry the ledger was missing.
//
// Nothing already written changes, and the balance column is not touched at
// all. What the wallet holds is the number every withdrawal has already been
// guarded against and every holder has already been able to spend; taking it
// away because a row is missing would be moving somebody's money to repair a
// record. So the column stands, and the ledger gains one settled entry, in the
// direction and of the size that makes the entries add up to it. There is no
// UPDATE of a balance here and there is none anywhere else either: a repair
// that edited the column would be the one write in this package that leaves no
// row behind.
//
// The entry moves no balance, which is what makes that arithmetic come out.
// An ordinary entry moves the column by exactly the amount it records, so it
// carries a difference forward instead of closing it; this one records the
// amount and moves nothing, so the ledger catches up and the column stays.
//
// It requires the wallet to be frozen, and answers ErrWalletNotFrozen where it
// is not. The freeze is what holds the two numbers still between the read that
// measures the difference and the statement that writes it -- and that
// statement names both of them, so a wallet that moved anyway leaves the whole
// transaction rolled back rather than adjusted by a stale number.
//
// The same statement lifts the freeze. A wallet whose ledger explains its
// balance again is a wallet that moves, and the two facts change together or
// neither does.
//
// A wallet whose ledger already adds up answers ErrLedgerBalanced and stays
// frozen: an adjustment of nothing would be a row saying something happened
// when nothing did, and lifting the freeze without one would be lifting it for
// a reason nobody recorded.
func (s *WalletService) Rebuild(ctx context.Context, actor security.Subject, in RebuildRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletReconcile, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	holder, err := Wallets(s.db).NewQuery().WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if holder == nil {
		return Receipt{}, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletReconcile, *holder); err != nil {
		return Receipt{}, err
	}

	// After the wallet, and never before it: knowing the key of a repair
	// somebody else asked for is not being allowed to read what it wrote.
	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationAdjustment, holder.ID); err != nil || found {
		return receipt, err
	}
	if !holder.Frozen {
		return Receipt{}, ErrWalletNotFrozen
	}

	found, err := s.walk(ctx, g, *holder)
	if err != nil {
		return Receipt{}, err
	}
	difference := found.Difference()
	if difference == 0 {
		return Receipt{}, ErrLedgerBalanced
	}

	// The direction is the ledger's and not the column's. A column holding more
	// than the entries explain is missing a deposit; one holding less is
	// missing a withdrawal.
	kind := EntryDeposit
	amount := difference
	if difference < 0 {
		kind = EntryWithdraw
		if amount, err = Amount(0).Sub(difference); err != nil {
			return Receipt{}, err
		}
	}

	return s.commit(ctx, g, operation{
		key:    in.IdempotencyKey,
		kind:   OperationAdjustment,
		reason: in.Reason,
		meta:   in.Meta,
	}, []movement{{wallet: holder, kind: kind, amount: amount, adjust: true, meta: in.Meta}})
}
