package wallet

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
	"github.com/arandu-io/hesape/database/model"
	"github.com/arandu-io/hesape/database/query"
)

// Pagination bounds for the listings. A request that asks for everything gets
// the maximum, never everything: an unbounded query is how one page load takes
// a production database down.
const (
	defaultLimit = 50
	maxLimit     = 200
)

// Bounds on the text a caller supplies. They are the widths the columns are
// created at, so a value that fits here fits there.
const (
	maxIdentifierLen     = 64
	maxNameLen           = 120
	maxIdempotencyKeyLen = 128
	maxReasonLen         = 255
	maxCurrencyLen       = 12
)

// sortableWallet is the ordering allowlist. A column name taken directly
// from a request would turn ordering into an injection surface.
var sortableWallet = map[string]string{
	"":           "created_at",
	"name":       "name",
	"slug":       "slug",
	"balance":    "balance",
	"created_at": "created_at",
}

// WalletService holds the rules of this package.
//
// It receives its collaborators through the constructor. There is no container
// and no resolution by reflection: what this service is made of is written at
// the one place that builds it, and reading that place is how somebody learns
// what the package touches.
//
// Everything a handler is allowed to do goes through here. The service is the
// only owner of the database handle, so the request layer cannot reach a Model
// before the policy has answered.
type WalletService struct {
	db     *data.DB
	policy WalletPolicy
	rates  RateProvider
}

// NewWalletService wires the service over the application's database handle.
//
// The rate provider may be nil, and nil is not a degraded mode: it is an
// application that moves money only between wallets counted the same way, which
// is most of them. A transfer that would need a rate is refused rather than
// guessed at.
func NewWalletService(db *data.DB, rates RateProvider) *WalletService {
	return &WalletService{db: db, rates: rates}
}

// OpenRequest is what opening a wallet takes.
//
// The fields are explicit and there is no mass assignment, so a request body
// cannot write a column nobody meant to expose. There is no TenantID here and
// there must never be one: the tenant comes from the Grant, which comes from
// the session. There is no Balance either -- a wallet opens empty, and money
// enters it through an operation that leaves a row in the ledger.
type OpenRequest struct {
	// HolderID is whose wallet this will be.
	HolderID string
	// Slug names which of the holder's wallets it is.
	Slug string
	// Name is what a person will call it.
	Name string
	// Currency is what its balance will count.
	Currency Currency
	// DecimalPlaces is the scale of that currency's minor unit, and it is fixed
	// once the wallet exists.
	DecimalPlaces int
}

// Validate reports the errors per field.
func (r OpenRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "holder_id", r.HolderID)
	validation.MaxLen(e, "holder_id", r.HolderID, maxIdentifierLen)
	validation.Required(e, "slug", r.Slug)
	validation.MaxLen(e, "slug", r.Slug, maxIdentifierLen)
	validation.Required(e, "name", r.Name)
	validation.MaxLen(e, "name", r.Name, maxNameLen)
	validation.Required(e, "currency", string(r.Currency))
	validation.MaxLen(e, "currency", string(r.Currency), maxCurrencyLen)
	if !ValidDecimalPlaces(r.DecimalPlaces) {
		e.Add("decimal_places", fmt.Sprintf("has to be between 0 and %d", MaxDecimalPlaces))
	}
	return e
}

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
}

// Validate reports the errors per field.
func (r DepositRequest) Validate() validation.Errors {
	return validateMovement(r.IdempotencyKey, r.WalletID, r.Amount)
}

// WithdrawRequest is what taking money out of a wallet takes.
type WithdrawRequest struct {
	// IdempotencyKey is the caller's name for this request.
	IdempotencyKey string
	// WalletID is the wallet to debit.
	WalletID string
	// Amount is the decimal to debit, written at the wallet's own scale.
	Amount string
}

// Validate reports the errors per field.
func (r WithdrawRequest) Validate() validation.Errors {
	return validateMovement(r.IdempotencyKey, r.WalletID, r.Amount)
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
}

// Validate reports the errors per field.
func (r TransferRequest) Validate() validation.Errors {
	e := validateMovement(r.IdempotencyKey, r.FromWalletID, r.Amount)
	validation.Required(e, "to_wallet_id", r.ToWalletID)
	validation.MaxLen(e, "to_wallet_id", r.ToWalletID, maxIdentifierLen)
	return e
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
}

// Validate reports the errors per field.
func (r ReverseRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "operation_id", r.OperationID)
	validation.MaxLen(e, "operation_id", r.OperationID, maxIdentifierLen)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	return e
}

// ListRequest is what paging through wallets takes.
type ListRequest struct {
	// Query is the page and the ordering.
	Query data.Query
	// HolderID narrows the page to one holder. It is a filter and not a
	// permission: a subject who is not an operator has it replaced by their own
	// identifier, so what they asked for cannot widen what they get.
	HolderID string
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

// validateMovement holds what every movement of money requires: a key to make
// the request replayable, a wallet to move, and an amount that is not empty.
// The amount's digits are checked against the wallet's scale later, where the
// scale is known.
func validateMovement(key, walletID, amount string) validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", key)
	validation.MaxLen(e, "idempotency_key", key, maxIdempotencyKeyLen)
	validation.Required(e, "wallet_id", walletID)
	validation.MaxLen(e, "wallet_id", walletID, maxIdentifierLen)
	validation.Required(e, "amount", amount)
	return e
}

// Compile-time proof that the requests honor the validation contract.
var (
	_ validation.Validatable = OpenRequest{}
	_ validation.Validatable = DepositRequest{}
	_ validation.Validatable = WithdrawRequest{}
	_ validation.Validatable = TransferRequest{}
	_ validation.Validatable = ReverseRequest{}
)

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
}

// Open creates a wallet for a holder.
//
// The candidate is authorized before it is stored, and the candidate is what
// the policy sees -- so a rule about whose wallet may be opened is a rule about
// the wallet being opened, and not about the person alone.
func (s *WalletService) Open(ctx context.Context, actor security.Subject, in OpenRequest) (*Wallet, error) {
	if errs := in.Validate(); errs.Any() {
		return nil, errs
	}

	proposed := Wallet{HolderID: in.HolderID, Slug: in.Slug, Name: in.Name}

	g, err := security.Authorize(ctx, s.policy, actor, WalletCreate, proposed)
	if err != nil {
		return nil, err
	}

	id, err := data.NewID()
	if err != nil {
		return nil, err
	}
	instance, err := Wallets(s.db).NewInstance(nil, false)
	if err != nil {
		return nil, err
	}
	candidate := instance.Entity
	candidate.ID = id
	candidate.TenantID = data.Tenant(g)
	candidate.HolderID = proposed.HolderID
	candidate.Slug = proposed.Slug
	candidate.Name = proposed.Name
	candidate.Currency = in.Currency
	candidate.DecimalPlaces = in.DecimalPlaces
	candidate.Balance = 0

	if _, err := candidate.Save(ctx, g); err != nil {
		// The unique index on holder and slug is what refuses a second wallet
		// under one name, and this turns its answer into one of ours. The
		// lookup runs after the failure rather than before it, because a check
		// that runs before is a check two concurrent requests both pass.
		taken, lookupErr := Wallets(s.db).NewQuery().
			Where("holder_id", "=", proposed.HolderID).
			Where("slug", "=", proposed.Slug).
			Exists(ctx, g)
		if lookupErr == nil && taken {
			return nil, ErrWalletExists
		}
		return nil, err
	}
	return candidate, nil
}

// Find returns one wallet, and asks the policy twice.
//
// The first call is on the empty candidate, because there is no way to read the
// record without a Grant and no way to hold a Grant without a decision. What it
// decides is whether this subject may read wallets at all.
//
// The second call is on the record that came back, and it is the one a rule
// about the record itself depends on: the first call saw an empty value, so
// anything the policy says about who holds the wallet never ran. Without it a
// policy can be written that looks correct, reads correctly, and is never
// consulted about the thing it protects.
//
// The read itself is already scoped by data.Tenant, so the second call is not
// what keeps customers apart. It is what keeps one customer's holders apart.
func (s *WalletService) Find(ctx context.Context, actor security.Subject, id string) (*Wallet, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletView, Wallet{})
	if err != nil {
		return nil, err
	}

	record, err := Wallets(s.db).NewQuery().WhereKey(id).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, ErrNotFound
	}

	if _, err := security.Authorize(ctx, s.policy, actor, WalletView, *record); err != nil {
		return nil, err
	}
	return record, nil
}

// List returns a page of wallets.
//
// It authorizes once, on the empty candidate, and the statement is what bounds
// the rows: the tenant filter the Model applies, and the holder predicate added
// here for a subject who is not an operator. A policy call per row would be one
// call per record on a page and would still not narrow the query -- a listing
// that has to read a customer's rows in order to decide it may not read them
// has already read them.
func (s *WalletService) List(ctx context.Context, actor security.Subject, in ListRequest) ([]*Wallet, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletList, Wallet{})
	if err != nil {
		return nil, err
	}

	column, ok := sortableWallet[in.Query.Sort]
	if !ok {
		return nil, fmt.Errorf("wallet: sort field not allowed: %q", in.Query.Sort)
	}

	holder := in.HolderID
	if !actor.HasRole(OperatorRole) {
		// Not a filter the caller chose: whoever is not an operator sees their
		// own wallets, whatever they asked for.
		holder = actor.ID
	}

	rows := Wallets(s.db)
	page := rows.NewQuery()
	if holder != "" {
		page = page.Where("holder_id", "=", holder)
	}
	if in.Query.Cursor != "" {
		anchor, err := rows.NewQuery().WhereKey(in.Query.Cursor).Value(ctx, g, column)
		if err != nil {
			return nil, err
		}
		if anchor == nil {
			return nil, nil
		}
		page = page.Where(func(after *model.Builder[Wallet]) {
			after.Where(column, ">", anchor).
				OrWhere(func(equal *model.Builder[Wallet]) {
					equal.Where(column, "=", anchor).Where("id", ">", in.Query.Cursor)
				})
		})
	}

	return page.OrderBy(column).OrderBy("id").Limit(boundedLimit(in.Query.Limit)).Get(ctx, g)
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
	return Statement{Wallet: *holder, Entries: entries}, nil
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
	}, []movement{{wallet: target, kind: EntryDeposit, amount: amount}})
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

	amount, err := positiveAmount(in.Amount, source.DecimalPlaces)
	if err != nil {
		return Receipt{}, err
	}

	return s.commit(ctx, g, operation{
		key:  in.IdempotencyKey,
		kind: OperationWithdraw,
	}, []movement{{wallet: source, kind: EntryWithdraw, amount: amount}})
}

// Transfer moves money out of one wallet and into another.
//
// Both movements are one operation and one transaction, so there is no state in
// which the money has left and not arrived. The authority that is checked is
// the source's: money leaving is what needs permission, and money arriving is
// bounded by the tenant the Grant carries, which is the only set of wallets the
// statement can reach at all.
//
// Two wallets counted the same way move the same number. Two counted
// differently need a rate, and the rate comes from the RateProvider the
// application configured -- without one the transfer is refused rather than
// approximated.
func (s *WalletService) Transfer(ctx context.Context, actor security.Subject, in TransferRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletTransfer, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationTransfer); err != nil || found {
		return receipt, err
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

	debited, err := positiveAmount(in.Amount, source.DecimalPlaces)
	if err != nil {
		return Receipt{}, err
	}
	credited, err := s.credited(ctx, g, *source, *target, debited)
	if err != nil {
		return Receipt{}, err
	}

	return s.commit(ctx, g, operation{
		key:  in.IdempotencyKey,
		kind: OperationTransfer,
	}, []movement{
		{wallet: source, kind: EntryWithdraw, amount: debited},
		{wallet: target, kind: EntryDeposit, amount: credited},
	})
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
// balance below zero answers ErrInsufficientFunds, because a wallet that owes
// money is a state this package has no way to represent and no way to collect.
//
// An operation can be undone once. The second attempt answers
// ErrAlreadyReversed, and the refusal is a unique index rather than a check, so
// two reversals arriving together cannot both be the one that succeeds.
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
		key:      in.IdempotencyKey,
		kind:     OperationReversal,
		reverses: original.ID,
		reason:   in.Reason,
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

// operation is what commit records before the money moves.
type operation struct {
	key      string
	kind     OperationKind
	reverses string
	reason   string
}

// movement is one wallet's share of an operation.
type movement struct {
	wallet *Wallet
	kind   EntryKind
	amount Amount
}

// stepped is the extra column every balance statement carries: the wallet's
// entry counter, moved in the same statement as the balance.
//
// It is an expression rather than a value read and incremented in Go for the
// same reason the balance is: the number an entry ends up with has to be one
// nothing else can be holding, and only the statement that writes it knows what
// the row said at that moment.
func stepped() map[string]any {
	return map[string]any{"last_sequence": query.Raw(`"last_sequence" + 1`)}
}

// commit records the operation and applies its movements, all or nothing.
//
// The operation row goes in first, and that ordering is the idempotency: the
// unique index on the tenant and the key is what refuses a second request under
// one name, so two identical calls arriving together cannot both reach the
// balance -- one of them fails at the index and rolls back with nothing
// written. The loser then finds the winner's operation and answers with it,
// which is why a failure here is looked up before it is reported.
func (s *WalletService) commit(ctx context.Context, g security.Grant, op operation, movements []movement) (Receipt, error) {
	id, err := data.NewID()
	if err != nil {
		return Receipt{}, err
	}
	instance, err := Operations(s.db).NewInstance(nil, false)
	if err != nil {
		return Receipt{}, err
	}
	record := instance.Entity
	record.ID = id
	record.TenantID = data.Tenant(g)
	record.IdempotencyKey = op.key
	record.Kind = op.kind
	record.Reason = op.reason
	// An operation settles exactly one thing: the operation it reverses, or
	// itself. With the kind beside it in a unique index, that one column
	// carries the whole rule that an operation is undone at most once, and a
	// row that undoes nothing collides with nothing -- including with the
	// reversal that names it, which differs from it in kind.
	record.ReversesID = op.reverses
	if record.ReversesID == "" {
		record.ReversesID = id
	}

	var entries []Entry
	err = data.Transaction(ctx, s.db, func(ctx context.Context) error {
		if _, err := record.Save(ctx, g); err != nil {
			return err
		}
		written, err := s.apply(ctx, g, id, movements)
		if err != nil {
			return err
		}
		entries = written
		return nil
	})
	if err != nil {
		if receipt, found, lookupErr := s.replay(ctx, g, op.key, op.kind); lookupErr == nil && found {
			return receipt, nil
		}
		return Receipt{}, err
	}
	return Receipt{Operation: *record, Entries: entries}, nil
}

// apply moves every balance and appends every entry, inside the caller's
// transaction.
//
// The wallets are touched in the order of their identifiers, and never in the
// order the request named them. Two transfers in opposite directions between
// the same pair would otherwise take their two row locks in opposite orders,
// which is a deadlock -- one that only appears under load, on the engines that
// take row locks, and is reported as a driver error nobody can place.
//
// The balance is moved by a statement carrying its own guard, and the guard is
// the whole of the concurrency safety here. Nothing reads a balance, decides,
// and writes it back: the update says what has to be true of the row it is
// updating, and the database answers how many rows that was. Zero means the
// condition was false at the moment of the write, which is the only moment that
// counts.
func (s *WalletService) apply(ctx context.Context, g security.Grant, operationID string, movements []movement) ([]Entry, error) {
	order := make([]int, len(movements))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return movements[order[a]].wallet.ID < movements[order[b]].wallet.ID
	})

	entries := make([]Entry, len(movements))
	for _, index := range order {
		entry, err := s.move(ctx, g, operationID, index, movements[index])
		if err != nil {
			return nil, err
		}
		entries[index] = entry
	}
	return entries, nil
}

// move applies one movement: the guarded balance update, then the row it wrote.
func (s *WalletService) move(ctx context.Context, g security.Grant, operationID string, position int, m movement) (Entry, error) {
	rows := Wallets(s.db)

	var affected int64
	var err error
	switch m.kind {
	case EntryWithdraw:
		// The balance has to still be enough at the moment of the write.
		affected, err = rows.NewQuery().
			WhereKey(m.wallet.ID).
			Where("balance", ">=", int64(m.amount)).
			Decrement(ctx, g, "balance", int64(m.amount), stepped())
	case EntryDeposit:
		// And it has to still have room, or the column wraps into a negative
		// balance that no rule in this package would ever have allowed.
		affected, err = rows.NewQuery().
			WhereKey(m.wallet.ID).
			Where("balance", "<=", int64(m.amount.Ceiling())).
			Increment(ctx, g, "balance", int64(m.amount), stepped())
	default:
		return Entry{}, fmt.Errorf("wallet: %q is not a direction money moves in", m.kind)
	}
	if err != nil {
		return Entry{}, err
	}

	// Read back inside the transaction, which is the balance the entry records,
	// the position it takes in the ledger, and the answer to why an update
	// matched nothing.
	after, err := rows.NewQuery().WhereKey(m.wallet.ID).First(ctx, g)
	if err != nil {
		return Entry{}, err
	}
	if after == nil {
		return Entry{}, ErrNotFound
	}
	if affected == 0 {
		if m.kind == EntryWithdraw {
			return Entry{}, ErrInsufficientFunds
		}
		return Entry{}, ErrAmountOverflow
	}

	id, err := data.NewID()
	if err != nil {
		return Entry{}, err
	}
	instance, err := Entries(s.db).NewInstance(nil, false)
	if err != nil {
		return Entry{}, err
	}
	written := instance.Entity
	written.ID = id
	written.TenantID = data.Tenant(g)
	written.OperationID = operationID
	written.WalletID = m.wallet.ID
	written.Kind = m.kind
	written.Sequence = after.LastSequence
	written.Position = position
	written.Amount = m.amount
	written.BalanceAfter = after.Balance
	written.CreatedAt = time.Now().UTC()
	if _, err := written.Save(ctx, g); err != nil {
		return Entry{}, err
	}
	return *written, nil
}

// replay answers with what this idempotency key already did, if anything.
//
// A key that names an operation of another kind is a conflict rather than a
// replay: one key cannot be the name of two different requests, and answering
// the deposit's receipt to a withdrawal would be answering a question nobody
// asked.
func (s *WalletService) replay(ctx context.Context, g security.Grant, key string, kind OperationKind) (Receipt, bool, error) {
	record, err := Operations(s.db).NewQuery().Where("idempotency_key", "=", key).First(ctx, g)
	if err != nil {
		return Receipt{}, false, err
	}
	if record == nil {
		return Receipt{}, false, nil
	}
	if record.Kind != kind {
		return Receipt{}, false, ErrOperationConflict
	}

	written, err := Entries(s.db).NewQuery().
		Where("operation_id", "=", record.ID).
		OrderBy("position").
		Get(ctx, g)
	if err != nil {
		return Receipt{}, false, err
	}
	entries := make([]Entry, 0, len(written))
	for _, entry := range written {
		if entry != nil {
			entries = append(entries, *entry)
		}
	}
	return Receipt{Operation: *record, Entries: entries, Replayed: true}, true, nil
}

// reversed reports whether an operation has already been undone.
func (s *WalletService) reversed(ctx context.Context, g security.Grant, operationID string) (bool, error) {
	return Operations(s.db).NewQuery().
		Where("reverses_id", "=", operationID).
		Where("kind", "=", string(OperationReversal)).
		Exists(ctx, g)
}

// mirror turns the entries of an operation into the movements that undo it, and
// asks the policy about every wallet they touch.
//
// The question is asked per wallet and not once for the operation, because a
// transfer's two entries are two people's money and a reversal moves both.
func (s *WalletService) mirror(ctx context.Context, g security.Grant, actor security.Subject, operationID string) ([]movement, error) {
	written, err := Entries(s.db).NewQuery().
		Where("operation_id", "=", operationID).
		OrderBy("position").
		Get(ctx, g)
	if err != nil {
		return nil, err
	}
	if len(written) == 0 {
		return nil, ErrNotFound
	}

	rows := Wallets(s.db)
	movements := make([]movement, 0, len(written))
	for _, entry := range written {
		if entry == nil {
			continue
		}
		holder, err := rows.NewQuery().WhereKey(entry.WalletID).First(ctx, g)
		if err != nil {
			return nil, err
		}
		if holder == nil {
			return nil, ErrNotFound
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletReverse, *holder); err != nil {
			return nil, err
		}
		kind := EntryWithdraw
		if entry.Kind == EntryWithdraw {
			kind = EntryDeposit
		}
		movements = append(movements, movement{wallet: holder, kind: kind, amount: entry.Amount})
	}
	return movements, nil
}

// credited is how much arrives in the target wallet.
//
// The same number when both wallets count the same thing at the same scale, and
// the rate provider's answer when they do not. The answer is checked before it
// is used: a provider that replies in the wrong currency or at the wrong scale
// is a provider whose number means something other than what this package would
// write, and writing it anyway is how an exchange rate becomes a rounding error
// nobody can trace.
func (s *WalletService) credited(ctx context.Context, g security.Grant, source, target Wallet, debited Amount) (Amount, error) {
	if source.Currency == target.Currency && source.DecimalPlaces == target.DecimalPlaces {
		return debited, nil
	}
	if s.rates == nil {
		return 0, ErrCurrencyMismatch
	}

	converted, err := s.rates.ConvertTo(ctx, g, source.Money(debited), target.Currency, target.DecimalPlaces)
	if err != nil {
		return 0, err
	}
	if converted.Currency != target.Currency || converted.DecimalPlaces != target.DecimalPlaces {
		return 0, fmt.Errorf("wallet: the rate provider answered in %s at %d places and the wallet holds %s at %d",
			converted.Currency, converted.DecimalPlaces, target.Currency, target.DecimalPlaces)
	}
	if converted.Amount <= 0 {
		return 0, ErrAmountNotPositive
	}
	return converted.Amount, nil
}

// positiveAmount reads what the caller wrote at the scale of the wallet it is
// for, and refuses everything that is not money moving.
func positiveAmount(text string, places int) (Amount, error) {
	amount, err := ParseAmount(text, places)
	if err != nil {
		return 0, err
	}
	if amount <= 0 {
		return 0, ErrAmountNotPositive
	}
	return amount, nil
}

// boundedLimit is what a page size becomes: the default when nothing was asked
// for, and the maximum when more was.
func boundedLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	}
	return limit
}
