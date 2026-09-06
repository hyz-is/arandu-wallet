package wallet

import (
	"context"
	"errors"
	"fmt"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
)

// DepositRequest is what putting money into a wallet takes.
type DepositRequest struct {
	// IdempotencyKey is the caller's name for this request. Sending the same
	// key twice moves money once.
	IdempotencyKey string
	// WalletID is the wallet to credit.
	WalletID string
	// Amount is the decimal to credit, written at the wallet's own scale.
	//
	// A decimal and not an integer of minor units, because the caller does not
	// have to know the scale to write "10.50" and does have to know it to write
	// 1050. A value with more fraction digits than the wallet's scale is
	// refused rather than rounded.
	Amount string
	// Pending records the movement without letting it count.
	//
	// The entry is written, the balance is not moved, and the money arrives
	// when somebody confirms the operation. False is the ordinary request and
	// the one a client that never heard of this field sends: what it asks for
	// happens, once, now.
	Pending bool
	// Meta is what the application attaches to this request: its own facts
	// about what the money was for. A movement with one leg carries them on the
	// operation, because there they are the request's.
	Meta Meta
}

// WithdrawRequest is what taking money out of a wallet takes.
type WithdrawRequest struct {
	// IdempotencyKey is the caller's name for this request.
	IdempotencyKey string
	// WalletID is the wallet to debit.
	WalletID string
	// Amount is the decimal to debit, written at the wallet's own scale.
	Amount string
	// Pending records the movement without letting it count, and holds
	// nothing: the balance is judged where the money moves, which is at the
	// confirmation.
	Pending bool
	// Force asks for the movement even where the balance and the credit limit
	// do not cover it.
	//
	// It is a field of the request and not a method beside Withdraw, because
	// two entry points for one movement are two places every later rule has to
	// be written into, and the one somebody forgets is the one that is not
	// guarded. Asking is not being answered: WalletForce is a separate
	// decision, and a subject the policy refuses it to is refused the movement.
	Force bool
	// Meta is what the application attaches to this request.
	Meta Meta
}

// TransferRequest is what moving money between two wallets takes.
type TransferRequest struct {
	// IdempotencyKey is the caller's name for this request.
	IdempotencyKey string
	// FromWalletID is the wallet the money leaves.
	FromWalletID string
	// ToWalletID is the wallet it arrives in.
	ToWalletID string
	// Amount is the decimal to move, written at the source wallet's scale.
	// What arrives is the same amount when both wallets are counted the same
	// way, and what the rate provider answers when they are not.
	Amount string
	// Withdrawal is what the leg that pays carries, and Deposit what the leg
	// that is paid carries.
	//
	// They are two values and not one flag over the pair, because the two sides
	// of a payment are not always the same decision. What leaves counting now
	// while what arrives waits is money held until somebody says it may be
	// delivered; the other way round is a delivery on credit. Both are ordinary
	// arrangements, and neither is expressible by a single yes-or-no.
	//
	// A rate, where one is needed, is quoted and recorded when the operation is
	// written, whatever either side says: what a confirmation applies is what
	// this operation wrote down.
	Withdrawal Leg
	Deposit    Leg
	// Force asks for the movement even where the source's balance and credit
	// limit do not cover it, and is answered by WalletForce.
	Force bool
	// Meta is what the application attaches to the payment as a whole. What
	// belongs to one side of it goes on that side's Leg.
	Meta Meta
}

// Leg is what one side of a movement carries.
//
// It is a value on the request rather than a second method beside the one that
// moves the money, for the reason Force is a field rather than a ForceTransfer:
// two entry points for one movement are two places every later rule has to be
// written into, and the one somebody forgets is the one that is not guarded.
type Leg struct {
	// Meta is what the application attaches to this side in particular, and it
	// is empty where this side says nothing the payment does not.
	Meta Meta
	// Pending records this side without letting it count. The entry is written,
	// the balance is not moved, and it moves when somebody confirms the
	// operation.
	Pending bool
}

// ReverseRequest is what undoing an operation takes.
type ReverseRequest struct {
	// IdempotencyKey is the caller's name for this request. It is the
	// reversal's own key, and never the key of the operation being reversed.
	IdempotencyKey string
	// OperationID is the operation to undo.
	OperationID string
	// Reason is what the reversal is recorded as. It is required, because a
	// reversal with no reason is a movement nobody can account for later.
	Reason string
	// Meta is what the application attaches to the reversal.
	Meta Meta
}

// ConfirmRequest is what settling a pending operation takes.
type ConfirmRequest struct {
	// IdempotencyKey is the caller's name for this request. It is the
	// confirmation's own key, and never the key of the operation being
	// confirmed.
	IdempotencyKey string
	// OperationID is the operation to make count.
	OperationID string
	// Force asks for the movement even where the balance and the credit limit
	// do not cover it, and is answered by WalletForce on every wallet the
	// operation touches.
	Force bool
	// Meta is what the application attaches to the confirmation.
	Meta Meta
}

// HistoryRequest is what reading a wallet's ledger takes.
type HistoryRequest struct {
	// WalletID is the wallet whose entries are read.
	WalletID string
	// Query is the page and the ordering. The ledger is ordered by when it was
	// written and by nothing else, so Sort is not read here: a statement in
	// another order is a statement that does not add up as you read down it.
	Query data.Query
}

// Statement is a page of one wallet's ledger, together with the wallet it
// belongs to.
//
// The wallet travels with the entries because an amount is meaningless without
// the scale it was written at, and the scale is the wallet's. A page of entries
// alone would be a page every reader has to go and ask a second question about.
type Statement struct {
	// Wallet is whose ledger this is. It is a snapshot for reading and not a
	// handle to write through.
	Wallet Wallet
	// Entries are the movements, oldest first.
	Entries []*Entry
	// Operations are the requests the entries on this page were written under,
	// by operation identifier.
	//
	// They travel with the page because a movement does not say what it was:
	// the same withdrawal is written by a payment, by a reversal and by an
	// exchange, and a reader who cannot see which is reading a ledger that
	// hides the difference. An entry names its operation, and this is where
	// that name resolves.
	Operations map[string]Operation
	// Conversions are the rates those operations applied, by operation
	// identifier. Only an exchange has one, so this map is smaller than
	// Operations and is empty on a wallet that never converted.
	Conversions map[string]Conversion
	// Charges are what those operations charged beyond the money they moved,
	// by operation identifier. Only a payment that discounted or charged has
	// one, so this map is empty on a wallet nobody charged.
	Charges map[string]Charge
}

// History returns a page of one wallet's ledger, oldest first.
//
// It is a read, and it asks the policy the same two questions a read of the
// wallet itself asks: whether this subject reads ledgers, and whether they read
// this one. A statement is the whole record of somebody's money, so a path to
// it that skipped the second question would be the widest read in the package.
func (s *WalletService) History(ctx context.Context, actor security.Subject, in HistoryRequest) (Statement, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletHistory, Wallet{})
	if err != nil {
		return Statement{}, err
	}

	holder, err := Wallets(s.db).NewQuery().WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return Statement{}, err
	}
	if holder == nil {
		return Statement{}, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletHistory, *holder); err != nil {
		return Statement{}, err
	}

	// The page is anchored on the sequence, which is unique within a wallet, so
	// the keyset needs no tie-breaker and no page can repeat or skip a row. The
	// cursor is still an entry identifier, because that is what a caller has in
	// its hand; the sequence it stands for is read here.
	rows := Entries(s.db)
	page := rows.NewQuery().Where("wallet_id", "=", holder.ID)
	if in.Query.Cursor != "" {
		anchor, err := rows.NewQuery().WhereKey(in.Query.Cursor).Value(ctx, g, "sequence")
		if err != nil {
			return Statement{}, err
		}
		if anchor == nil {
			return Statement{Wallet: *holder}, nil
		}
		page = page.Where("sequence", ">", anchor)
	}

	entries, err := page.OrderBy("sequence").Limit(boundedLimit(in.Query.Limit)).Get(ctx, g)
	if err != nil {
		return Statement{}, err
	}

	operations, conversions, charges, err := s.behind(ctx, g, entries)
	if err != nil {
		return Statement{}, err
	}
	return Statement{
		Wallet:      *holder,
		Entries:     entries,
		Operations:  operations,
		Conversions: conversions,
		Charges:     charges,
	}, nil
}

// Deposit puts money into a wallet.
//
// The same idempotency key twice credits once: the second call answers with the
// receipt the first one produced, and nothing moves. See Receipt.Replayed.
func (s *WalletService) Deposit(ctx context.Context, actor security.Subject, in DepositRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletDeposit, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationDeposit); err != nil || found {
		return receipt, err
	}

	target, err := Wallets(s.db).NewQuery().WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if target == nil {
		return Receipt{}, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletDeposit, *target); err != nil {
		return Receipt{}, err
	}

	amount, err := positiveAmount(in.Amount, target.DecimalPlaces)
	if err != nil {
		return Receipt{}, err
	}

	return s.commit(ctx, g, operation{
		key:  in.IdempotencyKey,
		kind: OperationDeposit,
		meta: in.Meta,
	}, []movement{{wallet: target, kind: EntryDeposit, amount: amount, pending: in.Pending}})
}

// Withdraw takes money out of a wallet.
//
// A balance that is not enough is refused with ErrInsufficientFunds, and the
// refusal comes from the statement that would have moved the money rather than
// from a comparison made a moment earlier: the guard is a predicate on the
// update, so a balance that changed in between changes the answer.
func (s *WalletService) Withdraw(ctx context.Context, actor security.Subject, in WithdrawRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdraw, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationWithdraw); err != nil || found {
		return receipt, err
	}

	source, err := Wallets(s.db).NewQuery().WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if source == nil {
		return Receipt{}, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletWithdraw, *source); err != nil {
		return Receipt{}, err
	}
	if err := s.allowForce(ctx, actor, in.Force, *source); err != nil {
		return Receipt{}, err
	}

	amount, err := positiveAmount(in.Amount, source.DecimalPlaces)
	if err != nil {
		return Receipt{}, err
	}

	return s.commit(ctx, g, operation{
		key:  in.IdempotencyKey,
		kind: OperationWithdraw,
		meta: in.Meta,
	}, []movement{{wallet: source, kind: EntryWithdraw, amount: amount, pending: in.Pending, force: in.Force}})
}

// Transfer moves money out of one wallet and into another, converting it when
// the two are not counted the same way.
//
// Both movements are one operation and one transaction, so there is no state in
// which the money has left and not arrived. The authority that is checked is
// the source's: money leaving is what needs permission, and money arriving is
// bounded by the tenant the Grant carries, which is the only set of wallets the
// statement can reach at all.
//
// Two wallets counted the same way move the same number and the operation is
// recorded as a transfer. Two counted differently -- another currency, or the
// same currency at another scale -- need a rate, and the operation is recorded
// as an exchange with the rate it was made at beside it. Which of the two it is
// comes from the wallets and never from the request: a caller cannot ask for a
// transfer and be given a conversion, or ask for a conversion between wallets
// that need none, because neither is a thing the caller decides.
//
// This is one path and not two. An Exchange method beside this one would be a
// second way to move money between two wallets, differing only in a field it
// wrote -- and the two would drift, because everything true of a transfer is
// true of an exchange except the rate.
//
// Without a configured RateProvider a conversion is refused rather than
// approximated.
func (s *WalletService) Transfer(ctx context.Context, actor security.Subject, in TransferRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletTransfer, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if in.FromWalletID == in.ToWalletID {
		return Receipt{}, ErrSameWallet
	}

	rows := Wallets(s.db)
	source, err := rows.NewQuery().WhereKey(in.FromWalletID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	target, err := rows.NewQuery().WhereKey(in.ToWalletID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if source == nil || target == nil {
		return Receipt{}, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletTransfer, *source); err != nil {
		return Receipt{}, err
	}
	if err := s.allowForce(ctx, actor, in.Force, *source); err != nil {
		return Receipt{}, err
	}

	// The wallets are read before the key is, because the kind an idempotent
	// replay has to match is the kind these two wallets produce, and that is
	// not known until they are loaded. Doing it the other way round would look
	// up an exchange under the name "transfer" and answer that the key belongs
	// to a different request -- which would make a retried exchange fail on
	// exactly the second attempt idempotency exists for.
	kind := OperationTransfer
	if converts(*source, *target) {
		kind = OperationExchange
	}
	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, kind); err != nil || found {
		return receipt, err
	}

	requested, err := positiveAmount(in.Amount, source.DecimalPlaces)
	if err != nil {
		return Receipt{}, err
	}

	// What the payer is charged less comes off first, because everything after
	// it is a share of what is being paid rather than of what was asked for.
	discount, err := s.discount(ctx, g, *source, *target, requested)
	if err != nil {
		return Receipt{}, err
	}
	base, err := requested.Sub(discount)
	if err != nil {
		return Receipt{}, err
	}
	if base <= 0 {
		return Receipt{}, ErrAmountNotPositive
	}

	credited, applied, err := s.convert(ctx, g, *source, *target, base)
	if err != nil {
		return Receipt{}, err
	}

	charged, collector, err := s.charge(ctx, g, *source, *target, requested, discount, base)
	if err != nil {
		return Receipt{}, err
	}

	// Who pays the fee is the only thing the schedule changes about the
	// movement: on top of the payment, or out of what arrives. Either way what
	// leaves is what arrives plus the fee, exactly.
	debited := base
	if collector != nil {
		if charged.schedule.Deductible {
			credited, err = credited.Sub(charged.fee.Amount)
			if err != nil {
				return Receipt{}, err
			}
			if credited <= 0 {
				return Receipt{}, fmt.Errorf("%w: %s of %s", ErrFeeExceedsAmount,
					charged.money(charged.fee.Amount), charged.money(base))
			}
		} else if debited, err = base.Add(charged.fee.Amount); err != nil {
			return Receipt{}, err
		}
	}

	movements := []movement{
		{
			wallet: source, kind: EntryWithdraw, amount: debited,
			pending: in.Withdrawal.Pending, force: in.Force, meta: in.Withdrawal.Meta,
		},
		{
			wallet: target, kind: EntryDeposit, amount: credited,
			pending: in.Deposit.Pending, meta: in.Deposit.Meta,
		},
	}
	if collector != nil {
		// The fee settles with the money it is a share of, which is the money
		// that leaves: a fee that counted while the payment it was taken out of
		// did not would be a charge for a payment that has not happened.
		movements = append(movements, movement{
			wallet: collector, kind: EntryDeposit, amount: charged.fee.Amount,
			pending: in.Withdrawal.Pending,
		})
	}

	return s.commit(ctx, g, operation{
		key:    in.IdempotencyKey,
		kind:   kind,
		meta:   in.Meta,
		rate:   applied,
		charge: charged,
	}, movements)
}

// Reverse undoes an operation by appending its opposite.
//
// Nothing already written changes. The original operation and its entries stay
// exactly as they were, and a second operation appears beside them naming the
// one it settles, with one mirrored entry per entry of the original. A
// statement therefore reads as what happened and then what was undone, which is
// what a person asking "why is this balance what it is" needs to see.
//
// It is refused when the money is no longer there: a reversal that would take a
// balance past what the wallet may hold answers ErrInsufficientFunds.
//
// It is refused as well when the operation never moved anything, with
// ErrNotSettled. What you undo is the operation that moved the money, and for a
// movement that waited to be confirmed that is the confirmation.
//
// An operation can be undone once. The second attempt answers
// ErrAlreadyReversed, and the refusal is a unique index rather than a check, so
// two reversals arriving together cannot both be the one that succeeds.
//
// Undoing an exchange moves back exactly what moved, on each side, in the
// currency it moved in. No rate is asked for and none is recorded: the amounts
// are read off the entries the exchange wrote, so what left comes back whole
// and what arrived goes back whole, whatever the pair is worth today.
// Converting again at a new rate would be a second exchange wearing the name of
// the first one's undoing, and it would leave one of the two wallets short.
func (s *WalletService) Reverse(ctx context.Context, actor security.Subject, in ReverseRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletReverse, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationReversal); err != nil || found {
		return receipt, err
	}

	original, err := Operations(s.db).NewQuery().WhereKey(in.OperationID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if original == nil {
		return Receipt{}, ErrNotFound
	}
	if original.Kind == OperationReversal {
		return Receipt{}, ErrNotReversible
	}
	// A basket is undone line by line and never whole. Reversing one would give
	// back every line of it including the ones already refunded, and the unique
	// index that keeps a line from being refunded twice knows nothing about a
	// reversal of the operation above it.
	if original.Kind == OperationPurchase || original.Kind == OperationRefund {
		return Receipt{}, ErrPurchaseOperation
	}
	if reversed, err := s.reversed(ctx, g, original.ID); err != nil {
		return Receipt{}, err
	} else if reversed {
		return Receipt{}, ErrAlreadyReversed
	}

	movements, err := s.mirror(ctx, g, actor, original.ID)
	if err != nil {
		return Receipt{}, err
	}

	receipt, err := s.commit(ctx, g, operation{
		key:     in.IdempotencyKey,
		kind:    OperationReversal,
		settles: original.ID,
		reason:  in.Reason,
		meta:    in.Meta,
	}, movements)
	if err != nil && !errors.Is(err, ErrInsufficientFunds) {
		// The unique index on the operation being settled is what refuses a
		// second reversal, and this turns its answer into ours. Read after the
		// failure, never instead of it: a check that ran before is a check two
		// concurrent reversals both passed.
		if reversed, lookupErr := s.reversed(ctx, g, original.ID); lookupErr == nil && reversed {
			return Receipt{}, ErrAlreadyReversed
		}
	}
	return receipt, err
}

// Confirm makes an operation that was only recorded count.
//
// It is the second half of a movement written as pending: the entries are
// already in the ledger, saying what was proposed and counting for nothing, and
// this appends the settled entry beside each of them under an operation that
// names the one it settles. Nothing already written changes -- which is why
// this is an operation of its own rather than a column somebody flips, and why
// a statement afterwards reads as what was asked for and then what happened.
//
// The money is judged here, because here is where it moves. A pending
// withdrawal holds nothing, so a confirmation whose wallet no longer covers it
// answers ErrInsufficientFunds, and the balance it is judged against is the
// balance at this write and not the one when the request was recorded.
//
// An operation is confirmed once. The second attempt answers
// ErrAlreadyConfirmed, and the refusal is a unique index rather than a check,
// so two confirmations arriving together cannot both be the one that succeeds.
// An operation with nothing waiting -- one that settled when it was made, a
// reversal, another confirmation -- answers ErrNotPending.
//
// There is one method and not the reference's pair of a safe and an unsafe one.
// This is the safe one: it reports why it could not settle instead of answering
// false, and a caller that wants the movement anyway asks for it in the request
// and is answered by the policy.
func (s *WalletService) Confirm(ctx context.Context, actor security.Subject, in ConfirmRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletConfirm, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationConfirmation); err != nil || found {
		return receipt, err
	}

	original, err := Operations(s.db).NewQuery().WhereKey(in.OperationID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if original == nil {
		return Receipt{}, ErrNotFound
	}

	movements, err := s.settle(ctx, g, actor, in, original.ID)
	if err != nil {
		return Receipt{}, err
	}

	receipt, err := s.commit(ctx, g, operation{
		key:     in.IdempotencyKey,
		kind:    OperationConfirmation,
		settles: original.ID,
		meta:    in.Meta,
	}, movements)
	if err != nil && !errors.Is(err, ErrInsufficientFunds) {
		// The unique index on the operation being settled is what refuses a
		// second confirmation, and this turns its answer into ours. Read after
		// the failure, never instead of it: a check that ran before is a check
		// two concurrent confirmations both passed.
		if done, lookupErr := s.confirmed(ctx, g, original.ID); lookupErr == nil && done {
			return Receipt{}, ErrAlreadyConfirmed
		}
	}
	return receipt, err
}
