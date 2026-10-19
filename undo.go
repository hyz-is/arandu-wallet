package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
)

// VoidRequest is what withdrawing an operation that never counted takes.
type VoidRequest struct {
	// IdempotencyKey is the caller's name for this request. It is the void's
	// own key, and never the key of the operation being voided.
	IdempotencyKey string
	// OperationID is the operation to void.
	OperationID string
	// Reason is what the void is recorded as. It is required, because a
	// proposal withdrawn for no recorded reason is one nobody can account for.
	Reason string
	// AccruedAt is the date the void is about, and zero for none.
	AccruedAt time.Time
	// Meta is what the application attaches to the void.
	Meta Meta
}

// Validate reports the errors per field.
func (r VoidRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "operation_id", r.OperationID)
	validation.MaxLen(e, "operation_id", r.OperationID, maxIdentifierLen)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	checkMeta(e, "meta", r.Meta)
	return e
}

// PartialReversalRequest is what giving back part of an operation takes.
type PartialReversalRequest struct {
	// IdempotencyKey is the caller's name for this request. It is this partial
	// reversal's own key: each one is a request of its own, and the same key
	// twice gives back once.
	IdempotencyKey string
	// OperationID is the operation to give part of back.
	OperationID string
	// Amount is how much to give back, as a decimal at the scale of the
	// operation's wallets. With what earlier partial reversals gave back, it
	// may reach what the operation moved and not pass it.
	Amount string
	// Reason is what the partial reversal is recorded as, and it is required
	// for the reason a reversal's is.
	Reason string
	// AccruedAt is the date the money given back is about, and zero for none.
	AccruedAt time.Time
	// Meta is what the application attaches to the partial reversal.
	Meta Meta
}

// Validate reports the errors per field.
func (r PartialReversalRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "operation_id", r.OperationID)
	validation.MaxLen(e, "operation_id", r.OperationID, maxIdentifierLen)
	validation.Required(e, "amount", r.Amount)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	checkMeta(e, "meta", r.Meta)
	return e
}

// Compile-time proof that the requests honor the validation contract.
var (
	_ validation.Validatable = VoidRequest{}
	_ validation.Validatable = PartialReversalRequest{}
)

// Void withdraws an operation that was recorded and never counted.
//
// It is the other way a pending operation ends, beside Confirm. Nothing already
// written changes: beside every pending entry of the operation, the opposite
// entry is appended, pending as well, under an operation of its own naming the
// one it voids. No balance moves, and the wallet's history reads as what was
// proposed and then that the proposal was withdrawn -- the two adding up to
// nothing, which is what the proposal amounted to.
//
// Only an operation none of whose entries counted is voided. One that counted
// in whole answers ErrNotPending, and one that counted on one side and waits on
// the other -- a transfer whose withdrawal settled and whose deposit waits --
// answers ErrNotVoidable: withdrawing the side that waits would leave the side
// that moved with nowhere to go, and that is a reversal's to decide.
//
// An operation is voided once and is then never confirmed, and one confirmed is
// never voided. Both are held by the database: the void's own row collides with
// a second void on the unique index that holds every settlement to one, and the
// operation is marked resolved by a statement that matches only while nothing
// resolved it, so a void and a confirmation racing each other cannot both
// succeed. The loser answers ErrAlreadyVoided or ErrAlreadyConfirmed.
//
// Voiding is the operator's, through WalletVoid on every wallet the operation
// touched, for the reason reversing is.
func (s *WalletService) Void(ctx context.Context, actor security.Subject, in VoidRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletVoid, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	original, err := Operations(s.db).WhereKey(in.OperationID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if original == nil {
		return Receipt{}, ErrNotFound
	}
	if original.Kind == OperationVoid {
		return Receipt{}, ErrNotVoidable
	}

	written, err := Entries(s.db).
		Where("operation_id", "=", original.ID).
		OrderBy("position").
		Get(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if len(written) == 0 {
		return Receipt{}, ErrNotFound
	}

	movements := make([]movement, 0, len(written))
	settled := 0
	for _, entry := range written {
		if entry == nil {
			continue
		}
		holder, err := Wallets(s.db).WhereKey(entry.WalletID).First(ctx, g)
		if err != nil {
			return Receipt{}, err
		}
		if holder == nil {
			return Receipt{}, ErrNotFound
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletVoid, *holder); err != nil {
			return Receipt{}, err
		}
		if entry.Settled {
			settled++
			continue
		}
		kind := EntryWithdraw
		if entry.Kind == EntryWithdraw {
			kind = EntryDeposit
		}
		movements = append(movements, movement{
			wallet: holder, kind: kind, amount: entry.Amount, pending: true, meta: entry.Meta,
		})
	}
	switch {
	case len(movements) == 0:
		return Receipt{}, ErrNotPending
	case settled > 0:
		return Receipt{}, ErrNotVoidable
	}

	// After every wallet the operation touches has been authorized, for the
	// reason a reversal's replay waits for it; and before asking whether it was
	// already voided, because the void that did it is exactly what a replay is.
	owner := settledFor(movements)
	asked := askedSettlement(original.ID)
	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationVoid, owner, asked); err != nil || found {
		return receipt, err
	}
	if voided, err := s.voided(ctx, g, original.ID); err != nil {
		return Receipt{}, err
	} else if voided {
		return Receipt{}, ErrAlreadyVoided
	}
	if done, err := s.confirmed(ctx, g, original.ID); err != nil {
		return Receipt{}, err
	} else if done {
		return Receipt{}, ErrAlreadyConfirmed
	}

	receipt, err := s.commit(ctx, g, operation{
		key:       in.IdempotencyKey,
		kind:      OperationVoid,
		settles:   original.ID,
		reason:    in.Reason,
		meta:      in.Meta,
		owner:     owner,
		asked:     asked,
		accruedAt: in.AccruedAt,
		record: func(ctx context.Context, g security.Grant, id string) error {
			return s.resolve(ctx, g, original.ID, id, ErrAlreadyConfirmed)
		},
	}, movements)
	if err != nil {
		// Read after the failure, never instead of it, as a reversal's is: the
		// unique index and the resolving statement decided, and this only
		// names which of the two answers it was.
		if voided, lookupErr := s.voided(ctx, g, original.ID); lookupErr == nil && voided {
			return Receipt{}, ErrAlreadyVoided
		}
		if done, lookupErr := s.confirmed(ctx, g, original.ID); lookupErr == nil && done {
			return Receipt{}, ErrAlreadyConfirmed
		}
	}
	return receipt, err
}

// ReversePartially gives back part of what an operation moved, and may be
// called again until all of it has been given back.
//
// Each call is an operation of its own, of kind OperationPartialReversal,
// naming the operation it gives part of back in RefersID, and it appends the
// mirror of every settled entry of that operation for the amount asked: a
// deposit gives part of its money back out, a transfer moves part of it back
// from the wallet paid to the wallet that paid. Nothing already written
// changes.
//
// What may be given back is capped by what the operation moved, cumulatively:
// the amount, with what earlier partial reversals gave back, may reach it and
// not pass it, and asking past it is ErrExceedsReversible. The cap is a
// predicate on the statement that raises the operation's ReversedAmount inside
// the same transaction as the money, so two partial reversals racing for the
// last of it cannot both pass. The same key twice gives back once.
//
// Only an operation whose settled entries all moved one amount can be reversed
// in part -- a deposit, a withdrawal, a transfer that charged no fee, a
// confirmation of one of those. An exchange, a payment that charged a fee, a
// basket and an adjustment answer ErrNotPartiallyReversible, because giving
// back part of them needs a share of each side decided. A basket is given back
// line by line, through Refund.
//
// An operation reversed whole answers ErrAlreadyReversed, and once part of one
// is given back, Reverse refuses it with ErrPartiallyReversed: what is left is
// given back here. It is the operator's, through WalletReverse on every wallet
// the operation touched.
func (s *WalletService) ReversePartially(ctx context.Context, actor security.Subject, in PartialReversalRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletReverse, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	original, err := Operations(s.db).WhereKey(in.OperationID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if original == nil {
		return Receipt{}, ErrNotFound
	}
	switch original.Kind {
	case OperationReversal, OperationPartialReversal, OperationVoid:
		return Receipt{}, ErrNotReversible
	case OperationPurchase, OperationRefund:
		return Receipt{}, ErrPurchaseOperation
	case OperationExchange, OperationAdjustment:
		return Receipt{}, ErrNotPartiallyReversible
	}

	movements, err := s.mirror(ctx, g, actor, original.ID)
	if err != nil {
		return Receipt{}, err
	}
	whole, partable := reversibleInPart(movements)
	if !partable {
		return Receipt{}, ErrNotPartiallyReversible
	}
	amount, err := positiveAmount(in.Amount, movements[0].wallet.DecimalPlaces)
	if err != nil {
		return Receipt{}, err
	}
	for i := range movements {
		movements[i].amount = amount
	}

	owner := settledFor(movements)
	asked := askedPart(original.ID, amount)
	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationPartialReversal, owner, asked); err != nil || found {
		return receipt, err
	}
	if reversed, err := s.reversed(ctx, g, original.ID); err != nil {
		return Receipt{}, err
	} else if reversed {
		return Receipt{}, ErrAlreadyReversed
	}
	if left := whole - original.ReversedAmount; amount > left {
		return Receipt{}, fmt.Errorf("%w: %s asked and %s left", ErrExceedsReversible,
			movements[0].wallet.Money(amount), movements[0].wallet.Money(left))
	}

	receipt, err := s.commit(ctx, g, operation{
		key:       in.IdempotencyKey,
		kind:      OperationPartialReversal,
		refers:    original.ID,
		reason:    in.Reason,
		meta:      in.Meta,
		owner:     owner,
		asked:     asked,
		accruedAt: in.AccruedAt,
		record: func(ctx context.Context, g security.Grant, _ string) error {
			return s.giveBackPart(ctx, g, original.ID, amount, whole)
		},
	}, movements)
	if err != nil && errors.Is(err, ErrExceedsReversible) {
		// A whole reversal raises what was given back to everything, so a
		// partial reversal that lost to one is told that rather than that it
		// asked for too much.
		if reversed, lookupErr := s.reversed(ctx, g, original.ID); lookupErr == nil && reversed {
			return Receipt{}, ErrAlreadyReversed
		}
	}
	return receipt, err
}

// reversibleInPart reports what an operation moved, when every settled entry of
// it moved the same amount, and whether that is so.
//
// It is read off the mirrored movements, which are the settled entries of the
// operation, so it is the amount a whole reversal would give back on each side.
// Where the entries disagree -- a fee on top of a payment, a fee taken out of
// what arrived, a rate between the two sides -- a part of the operation is not
// one number, and the answer is no.
func reversibleInPart(movements []movement) (Amount, bool) {
	if len(movements) == 0 {
		return 0, false
	}
	whole := movements[0].amount
	for _, m := range movements[1:] {
		if m.amount != whole || converts(*m.wallet, *movements[0].wallet) {
			return 0, false
		}
	}
	return whole, true
}

// askedPart is the claim of a partial reversal: it gives back part of this
// operation, for this amount.
func askedPart(operationID string, amount Amount) claim {
	return func(r Receipt) bool {
		return r.Operation.RefersID == operationID && len(r.Entries) > 0 && r.Entries[0].Amount == amount
	}
}

// giveBackPart raises what partial reversals gave back of an operation by an
// amount, inside the caller's transaction, when that leaves it within what the
// operation moved.
//
// The cap is the predicate of the statement: the row matches only while what
// was given back is at most the whole less this amount, so the count the
// database answers is the decision, and two partial reversals racing for the
// last of it cannot both match.
func (s *WalletService) giveBackPart(ctx context.Context, g security.Grant, operationID string, amount, whole Amount) error {
	n, err := Operations(s.db).
		Where("id", "=", operationID).
		Where("reversed_amount", "<=", int64(whole-amount)).
		Increment(ctx, g, "reversed_amount", int64(amount), nil)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrExceedsReversible
	}
	return nil
}

// closeToPartialReversal marks an operation that could be reversed in part as
// reversed whole, inside the whole reversal's transaction: what was given back
// becomes everything it moved, so no partial reversal can follow. It matches
// only while nothing was given back yet, which is what refuses a whole
// reversal racing a partial one.
func (s *WalletService) closeToPartialReversal(ctx context.Context, g security.Grant, operationID string, whole Amount) error {
	n, err := Operations(s.db).
		Where("id", "=", operationID).
		Where("reversed_amount", "=", int64(0)).
		Update(ctx, g, map[string]any{"reversed_amount": int64(whole)})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrPartiallyReversed
	}
	return nil
}

// resolve marks an operation's pending entries settled by a confirmation or a
// void, inside that one's transaction. It matches only while nothing resolved
// the operation, and answers taken where something already did.
func (s *WalletService) resolve(ctx context.Context, g security.Grant, operationID, by string, taken error) error {
	n, err := Operations(s.db).
		Where("id", "=", operationID).
		Where("resolved_by", "=", "").
		Update(ctx, g, map[string]any{"resolved_by": by})
	if err != nil {
		return err
	}
	if n != 1 {
		return taken
	}
	return nil
}

// voided reports whether an operation has already been withdrawn.
func (s *WalletService) voided(ctx context.Context, g security.Grant, operationID string) (bool, error) {
	return Operations(s.db).
		Where("reverses_id", "=", operationID).
		Where("kind", "=", string(OperationVoid)).
		Exists(ctx, g)
}
