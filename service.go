package wallet

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
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
	maxDescriptionLen    = 255
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
	db        *data.DB
	policy    WalletPolicy
	rates     RateProvider
	fees      FeeProvider
	discounts DiscountProvider
	listeners []Listener
}

// NewWalletService wires the service over the application's database handle.
//
// Every provider may be nil, and nil is not a degraded mode. It is an
// application that moves money only between wallets counted the same way, that
// charges nothing to be paid, and that discounts nothing -- which is most of
// them. A transfer that would need a rate is refused rather than guessed at,
// and a payment with no schedule is a payment with no fee.
//
// They are parameters and not a struct of options, for the reason Config is a
// struct and not a map: what this service is made of is written at the one place
// that builds it, and reading that place is how somebody learns what the package
// reaches for.
//
// The listeners are last and variadic because there may be none, which is the
// ordinary case: an application that wants to be told what its money did says so
// by passing something, and one that does not passes nothing rather than a nil
// it has to remember the meaning of.
func NewWalletService(db *data.DB, rates RateProvider, fees FeeProvider, discounts DiscountProvider, listeners ...Listener) *WalletService {
	return &WalletService{
		db:        db,
		rates:     rates,
		fees:      fees,
		discounts: discounts,
		listeners: append([]Listener(nil), listeners...),
	}
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
	//
	// Empty is derived from Name, so an application that has one word for a
	// wallet writes it once. What it derives to is Slugify's answer, and a name
	// that derives to nothing -- punctuation, or a script this package cannot
	// fold -- is refused rather than turned into a slug nobody chose.
	Slug string
	// Name is what a person will call it.
	Name string
	// Description is what a person is told it is for, and it may be empty.
	Description string
	// Meta is what the application attaches to the wallet itself: facts that
	// are true of the wallet rather than of any one movement.
	Meta Meta
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
	validation.MaxLen(e, "description", r.Description, maxDescriptionLen)
	checkMeta(e, "meta", r.Meta)
	validation.Required(e, "currency", string(r.Currency))
	validation.MaxLen(e, "currency", string(r.Currency), maxCurrencyLen)
	if !ValidDecimalPlaces(r.DecimalPlaces) {
		e.Add("decimal_places", fmt.Sprintf("has to be between 0 and %d", MaxDecimalPlaces))
	}
	return e
}

// CreditRequest is what setting a wallet's credit limit takes.
type CreditRequest struct {
	// WalletID is the wallet whose limit is being set.
	WalletID string
	// Limit is how far below zero the wallet may go, as a decimal at its own
	// scale, and "0" is a wallet that may not go below zero at all.
	//
	// A magnitude and never a negative number: the sign belongs to the rule,
	// which is that the balance may not end below the negative of this. A limit
	// written with a minus is refused rather than read as its own opposite.
	Limit string
}

// Validate reports the errors per field.
func (r CreditRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "wallet_id", r.WalletID)
	validation.MaxLen(e, "wallet_id", r.WalletID, maxIdentifierLen)
	validation.Required(e, "limit", r.Limit)
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

// Validate reports the errors per field.
func (r DepositRequest) Validate() validation.Errors {
	return validateMovement(r.IdempotencyKey, r.WalletID, r.Amount, r.Meta)
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

// Validate reports the errors per field.
func (r WithdrawRequest) Validate() validation.Errors {
	return validateMovement(r.IdempotencyKey, r.WalletID, r.Amount, r.Meta)
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

// Validate reports the errors per field.
func (r TransferRequest) Validate() validation.Errors {
	e := validateMovement(r.IdempotencyKey, r.FromWalletID, r.Amount, r.Meta)
	validation.Required(e, "to_wallet_id", r.ToWalletID)
	validation.MaxLen(e, "to_wallet_id", r.ToWalletID, maxIdentifierLen)
	checkMeta(e, "withdrawal_meta", r.Withdrawal.Meta)
	checkMeta(e, "deposit_meta", r.Deposit.Meta)
	return e
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

// Validate reports the errors per field.
func (r ReverseRequest) Validate() validation.Errors {
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

// Validate reports the errors per field.
func (r ConfirmRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "operation_id", r.OperationID)
	validation.MaxLen(e, "operation_id", r.OperationID, maxIdentifierLen)
	checkMeta(e, "meta", r.Meta)
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
func validateMovement(key, walletID, amount string, meta Meta) validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", key)
	validation.MaxLen(e, "idempotency_key", key, maxIdempotencyKeyLen)
	validation.Required(e, "wallet_id", walletID)
	validation.MaxLen(e, "wallet_id", walletID, maxIdentifierLen)
	validation.Required(e, "amount", amount)
	checkMeta(e, "meta", meta)
	return e
}

// checkMeta reports why what the application attached under a field cannot be
// stored.
//
// It is answered here rather than left to the column, because a movement the
// database refuses is a movement refused after the operation was recorded --
// and the caller would be told about a storage limit by a failed write instead
// of about the payload it sent by a rejected field.
func checkMeta(e validation.Errors, field string, meta Meta) {
	if err := meta.Validate(); err != nil {
		e.Add(field, err.Error())
	}
}

// PayRequest is what paying for a basket takes.
//
// There is no pending mode here, and its absence is a decision. A movement is
// recorded without counting so that somebody can say later whether it happened;
// a basket that has not been paid for is a basket, and what an application
// wants held is the delivery, which is a transfer whose two sides settle apart.
// A second half-paid state, with lines that are on the record and money that is
// not, would be a second answer to what "has this been bought" means.
type PayRequest struct {
	// IdempotencyKey is the caller's name for this request. Sending the same
	// key twice pays once.
	IdempotencyKey string
	// PayerWalletID is the wallet the money leaves. Every line of the basket
	// is paid from it, and every price is read at its scale.
	PayerWalletID string
	// Cart is what is being bought.
	Cart Cart
	// Force asks for the payment even where the balance and the credit limit do
	// not cover it, and is answered by WalletForce on the wallet paying.
	Force bool
}

// Validate reports the errors per field.
func (r PayRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "payer_wallet_id", r.PayerWalletID)
	validation.MaxLen(e, "payer_wallet_id", r.PayerWalletID, maxIdentifierLen)
	for field, messages := range r.Cart.Validate() {
		for _, message := range messages {
			e.Add(field, message)
		}
	}
	return e
}

// RefundRequest is what giving back some lines of a purchase takes.
type RefundRequest struct {
	// IdempotencyKey is the caller's name for this request. It is the refund's
	// own key, and never the key of the purchase being given back.
	IdempotencyKey string
	// PurchaseIDs are the lines to give back. They may come from one basket or
	// from several: what is undone is a line, and which request it was part of
	// changes nothing about the money.
	PurchaseIDs []string
	// Reason is what the refund is recorded as. It is required, for the reason a
	// reversal's is: money that moved for no recorded reason is money nobody can
	// account for later.
	Reason string
	// Force asks for the movement even where the wallet giving the money back
	// does not cover it, and is answered by WalletForce.
	Force bool
	// Meta is what the application attaches to the refund.
	Meta Meta
}

// Validate reports the errors per field.
func (r RefundRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	if len(r.PurchaseIDs) == 0 {
		e.Add("purchase_ids", "names no line, and a refund of nothing is not a movement")
	}
	if len(r.PurchaseIDs) > MaxCartLines {
		e.Add("purchase_ids", ErrCartTooLarge.Error())
	}
	for _, id := range r.PurchaseIDs {
		validation.Required(e, "purchase_ids", id)
		validation.MaxLen(e, "purchase_ids", id, maxIdentifierLen)
	}
	checkMeta(e, "meta", r.Meta)
	return e
}

// Compile-time proof that the requests honor the validation contract.
var (
	_ validation.Validatable = OpenRequest{}
	_ validation.Validatable = PayRequest{}
	_ validation.Validatable = RefundRequest{}
	_ validation.Validatable = CreditRequest{}
	_ validation.Validatable = DepositRequest{}
	_ validation.Validatable = WithdrawRequest{}
	_ validation.Validatable = TransferRequest{}
	_ validation.Validatable = ReverseRequest{}
	_ validation.Validatable = ConfirmRequest{}
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

// Open creates a wallet for a holder.
//
// The candidate is authorized before it is stored, and the candidate is what
// the policy sees -- so a rule about whose wallet may be opened is a rule about
// the wallet being opened, and not about the person alone.
func (s *WalletService) Open(ctx context.Context, actor security.Subject, in OpenRequest) (*Wallet, error) {
	in.Slug = Slugify(in.Slug, in.Name)
	if errs := in.Validate(); errs.Any() {
		return nil, errs
	}

	proposed := Wallet{
		HolderID: in.HolderID, Slug: in.Slug, Name: in.Name,
		Description: in.Description, Meta: in.Meta,
	}

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
	candidate.Description = in.Description
	candidate.Meta = in.Meta
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

	s.notify(ctx, g, Event{
		Kind:          WalletOpened,
		WalletID:      candidate.ID,
		Currency:      candidate.Currency,
		DecimalPlaces: candidate.DecimalPlaces,
		Balance:       candidate.Balance,
	})
	return candidate, nil
}

// SetCredit sets how far below zero a wallet may go.
//
// The limit is a column on the wallet and not a value the caller passes with
// each withdrawal, because it is read by the statement that moves the money:
// the guard compares the balance against the amount less this column, so what
// applies is what the row holds at that instant. A limit that travelled with
// the request would be a limit the request chose.
//
// Lowering one is guarded the same way. The write requires the balance to be
// within the new limit at the moment it happens, so a wallet is never left
// further below zero than any withdrawal could have taken it; where it already
// is, the answer is ErrCreditBelowBalance and nothing changes.
//
// It is asked about twice, like every other read of one wallet: once to decide
// whether this subject sets limits at all, and once about the wallet whose
// limit it is.
func (s *WalletService) SetCredit(ctx context.Context, actor security.Subject, in CreditRequest) (*Wallet, error) {
	if errs := in.Validate(); errs.Any() {
		return nil, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletCredit, Wallet{})
	if err != nil {
		return nil, err
	}

	rows := Wallets(s.db)
	record, err := rows.NewQuery().WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletCredit, *record); err != nil {
		return nil, err
	}

	limit, err := ParseAmount(in.Limit, record.DecimalPlaces)
	if err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, ErrCreditNegative
	}

	affected, err := rows.NewQuery().
		WhereKey(in.WalletID).
		Where("balance", ">=", int64(-limit)).
		Update(ctx, g, map[string]any{"credit_limit": int64(limit)})
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrCreditBelowBalance
	}

	written, err := rows.NewQuery().WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if written == nil {
		return nil, ErrNotFound
	}

	s.notify(ctx, g, Event{
		Kind:          WalletCreditChanged,
		WalletID:      written.ID,
		Currency:      written.Currency,
		DecimalPlaces: written.DecimalPlaces,
		Amount:        written.CreditLimit,
		Balance:       written.Balance,
	})
	return written, nil
}

// DefaultSlug is the slug of the wallet a holder has when nobody said which.
//
// A constant rather than a rule: this package opens no wallet by itself and
// never falls back to one, so what this names is a convention an application
// can share with its own code and with anybody reading its rows. An application
// whose holders have exactly one wallet opens it under this and never writes the
// word again.
const DefaultSlug = "default"

// FindBySlug returns the wallet a holder keeps under this slug.
//
// It is the read for the caller who knows whose money it is and what they call
// it, which is most callers: the pair is what names a wallet, it is under the
// unique index the table was created with, and an application that had to keep
// a generated identifier beside its own user row would be keeping a second key
// for a row it can already name.
//
// It opens nothing. A read that created the wallet it did not find would be a
// write behind a name that promises a read -- and the first caller to ask about
// a holder who has none would silently open one, under a currency and a scale
// this package would have had to guess.
//
// It asks the policy the same two questions Find asks, in the same order and for
// the same reason: the first decides whether this subject reads wallets, and the
// second is the one a rule about the holder answers.
func (s *WalletService) FindBySlug(ctx context.Context, actor security.Subject, holderID, slug string) (*Wallet, error) {
	if errs := validateName(holderID, slug); errs.Any() {
		return nil, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletView, Wallet{})
	if err != nil {
		return nil, err
	}

	record, err := Wallets(s.db).NewQuery().
		Where("holder_id", "=", holderID).
		Where("slug", "=", slug).
		First(ctx, g)
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

// validateName reports why a holder and a slug cannot name a wallet.
func validateName(holderID, slug string) validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "holder_id", holderID)
	validation.MaxLen(e, "holder_id", holderID, maxIdentifierLen)
	validation.Required(e, "slug", slug)
	validation.MaxLen(e, "slug", slug, maxIdentifierLen)
	return e
}

// DescribeRequest is what changing a wallet's labels takes.
//
// Labels and nothing else. The slug, the currency and the scale are absent and
// have to be: the first names which of a holder's wallets this is and is under
// a unique index, and the other two decide what every amount already written
// means. A wallet whose scale changed would be a wallet whose whole ledger
// silently moved a decimal point.
type DescribeRequest struct {
	// WalletID is the wallet being relabelled.
	WalletID string
	// Name is what a person calls it. It is required, because a wallet with no
	// name is a row in a list nobody can pick out.
	Name string
	// Description is what a person is told it is for, and empty clears it.
	Description string
	// Meta is what the application attaches to the wallet, and it replaces what
	// was there rather than merging into it: a partial write would make
	// "remove this name" impossible to express.
	Meta Meta
}

// Validate reports the errors per field.
func (r DescribeRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "wallet_id", r.WalletID)
	validation.MaxLen(e, "wallet_id", r.WalletID, maxIdentifierLen)
	validation.Required(e, "name", r.Name)
	validation.MaxLen(e, "name", r.Name, maxNameLen)
	validation.MaxLen(e, "description", r.Description, maxDescriptionLen)
	checkMeta(e, "meta", r.Meta)
	return e
}

// Describe changes what a wallet is called, what it is for, and the facts the
// application keeps about it.
//
// It touches no money and no column any guard reads, which is why it is allowed
// on a frozen wallet: the freeze says this package cannot explain the balance,
// and a sentence about what the wallet is for is not a claim about the balance.
// A relabelling refused because of a discrepancy would be a refusal nobody could
// act on -- the person correcting the label is usually the person investigating
// the discrepancy.
//
// It is asked about twice, like every other write to one wallet: once to decide
// whether this subject relabels wallets at all, and once about the wallet whose
// label it is.
func (s *WalletService) Describe(ctx context.Context, actor security.Subject, in DescribeRequest) (*Wallet, error) {
	if errs := in.Validate(); errs.Any() {
		return nil, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletDescribe, Wallet{})
	if err != nil {
		return nil, err
	}

	rows := Wallets(s.db)
	record, err := rows.NewQuery().WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletDescribe, *record); err != nil {
		return nil, err
	}

	if _, err := rows.NewQuery().WhereKey(in.WalletID).Update(ctx, g, map[string]any{
		"name":        in.Name,
		"description": in.Description,
		"meta":        in.Meta,
	}); err != nil {
		return nil, err
	}

	relabelled, err := rows.NewQuery().WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if relabelled == nil {
		return nil, ErrNotFound
	}
	return relabelled, nil
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

// behind reads the operations a page of entries was written under, the rates
// those operations applied, and what they charged.
//
// Three statements for the whole page rather than three per entry: a page is up
// to two hundred rows, and a query per row is the read that makes a statement
// slow on exactly the accounts that have a history worth reading. Two of the
// three are skipped where the page has nothing of that kind on it.
//
// All are read through the Model with the Grant, so all are scoped to the
// tenant like everything else. A ledger that could be explained by another
// customer's operations would be a ledger that leaks one.
func (s *WalletService) behind(ctx context.Context, g security.Grant, entries []*Entry) (map[string]Operation, map[string]Conversion, map[string]Charge, error) {
	ids := make([]any, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry == nil || seen[entry.OperationID] {
			continue
		}
		seen[entry.OperationID] = true
		ids = append(ids, entry.OperationID)
	}
	if len(ids) == 0 {
		return map[string]Operation{}, map[string]Conversion{}, map[string]Charge{}, nil
	}

	rows, err := Operations(s.db).NewQuery().WhereIn("id", ids).Get(ctx, g)
	if err != nil {
		return nil, nil, nil, err
	}
	operations := make(map[string]Operation, len(rows))
	converting := make([]any, 0, len(rows))
	paying := make([]any, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		operations[row.ID] = *row
		switch row.Kind {
		case OperationExchange:
			converting = append(converting, row.ID)
			paying = append(paying, row.ID)
		case OperationTransfer:
			paying = append(paying, row.ID)
		}
	}

	conversions := make(map[string]Conversion, len(converting))
	if len(converting) > 0 {
		rates, err := Conversions(s.db).NewQuery().WhereIn("operation_id", converting).Get(ctx, g)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, rate := range rates {
			if rate != nil {
				conversions[rate.OperationID] = *rate
			}
		}
	}

	charges := make(map[string]Charge, len(paying))
	if len(paying) > 0 {
		charged, err := Charges(s.db).NewQuery().WhereIn("operation_id", paying).Get(ctx, g)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, row := range charged {
			if row != nil {
				charges[row.OperationID] = *row
			}
		}
	}
	return operations, conversions, charges, nil
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

// allowForce asks the policy about ignoring the limit, and only where the
// request asked for it.
//
// The question is about the wallet the money leaves, because that is the money
// the limit protects. A movement nobody asked to force asks nothing, so a
// subject who may never force is refused nothing they did not request.
func (s *WalletService) allowForce(ctx context.Context, actor security.Subject, force bool, record Wallet) error {
	if !force {
		return nil
	}
	_, err := security.Authorize(ctx, s.policy, actor, WalletForce, record)
	return err
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

// discount is what the payer is charged less on this payment, and zero where
// nothing answers.
//
// The provider is asked once, and what it answers is recorded: a discount that
// was decided and not written down is a receipt that says a smaller number than
// the request without saying why.
func (s *WalletService) discount(ctx context.Context, g security.Grant, source, target Wallet, requested Amount) (Amount, error) {
	if s.discounts == nil {
		return 0, nil
	}
	discount, err := s.discounts.Discount(ctx, g, source, target, source.Money(requested))
	if err != nil {
		return 0, err
	}
	if discount < 0 {
		return 0, fmt.Errorf("%w: got %s", ErrDiscountNegative, source.Money(discount))
	}
	return discount, nil
}

// charge is what this payment costs beyond the money it moves, and the wallet
// the fee is credited to.
//
// Both are nil where there is nothing to record: no discount and no schedule.
// A discount with no fee still produces a record and no third wallet, because
// the number the payer was charged less is a fact about the payment whether or
// not anybody charged for it.
//
// The provider is asked once and what it answers is applied once, which is the
// same arrangement the rate has and for the same reason: a schedule read twice
// could answer twice, and then the fee that was taken and the fee on the record
// would be two different stories about one payment.
//
// A fee never crosses a rate. The two wallets have to be counted the same way
// and so does the one collecting, because the fee is a share of the payment and
// is charged in the payment's money -- carrying it through a rate would round a
// number that is already the result of a rounding.
func (s *WalletService) charge(ctx context.Context, g security.Grant, source, target Wallet, requested, discount, base Amount) (*appliedCharge, *Wallet, error) {
	var schedule FeeSchedule
	if s.fees != nil {
		answered, err := s.fees.Fee(ctx, g, target, source.Money(base))
		if err != nil {
			return nil, nil, err
		}
		schedule = answered
	}

	if !schedule.Charges() {
		if discount == 0 {
			return nil, nil, nil
		}
		return &appliedCharge{
			currency:      source.Currency,
			decimalPlaces: source.DecimalPlaces,
			requested:     requested,
			discount:      discount,
			base:          base,
		}, nil, nil
	}

	if err := schedule.Validate(); err != nil {
		return nil, nil, err
	}
	if converts(source, target) {
		return nil, nil, fmt.Errorf("%w: %s and %s", ErrFeeCurrencyMismatch,
			source.Money(base), target.Money(0))
	}
	if schedule.WalletID == source.ID || schedule.WalletID == target.ID {
		return nil, nil, fmt.Errorf("%w: it names one of the two wallets the payment is between", ErrFeeWallet)
	}

	collector, err := Wallets(s.db).NewQuery().WhereKey(schedule.WalletID).First(ctx, g)
	if err != nil {
		return nil, nil, err
	}
	if collector == nil {
		return nil, nil, fmt.Errorf("%w: %s is not a wallet of this customer", ErrFeeWallet, schedule.WalletID)
	}
	if converts(source, *collector) {
		return nil, nil, fmt.Errorf("%w: the payment is %s and the fee would be credited in %s",
			ErrFeeCurrencyMismatch, source.Money(base), collector.Money(0))
	}

	fee, err := schedule.Fee(source.Money(base))
	if err != nil {
		return nil, nil, err
	}
	return &appliedCharge{
		currency:      source.Currency,
		decimalPlaces: source.DecimalPlaces,
		requested:     requested,
		discount:      discount,
		base:          base,
		schedule:      schedule,
		fee:           fee,
	}, collector, nil
}

// converts reports that money moving between these two wallets has to go
// through a rate.
//
// A different currency, and also the same currency at a different scale. The
// second is a conversion too: the digits move, the division does not always
// come out whole, and what is left over has to be recorded for the same reason
// it does when the currency changes. Calling it a plain transfer would be
// calling a rounding a transfer.
func converts(source, target Wallet) bool {
	return source.Currency != target.Currency || source.DecimalPlaces != target.DecimalPlaces
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

// operation is what commit records before the money moves.
type operation struct {
	key     string
	kind    OperationKind
	settles string
	reason  string
	meta    Meta
	rate    *appliedRate
	charge  *appliedCharge
	// lines are the basket this operation paid for or gave back, and empty on
	// every operation that bought nothing. They are written inside the same
	// transaction as the movements, because a line whose money moved and whose
	// record is missing is a purchase nobody can find and nobody can refund.
	lines []purchaseLine
}

// purchaseLine is one line of a basket as it will be recorded, and there are
// none where an operation bought nothing.
//
// It carries every number the arithmetic used, which is everything the recorded
// row holds. It is one value travelling from the place that asked the seams to
// the place that writes the row, so there is exactly one price, one discount and
// one schedule per line and no second lookup between them.
type purchaseLine struct {
	position      int
	payer         string
	owner         string
	receiver      string
	productKey    string
	quantity      int
	currency      Currency
	decimalPlaces int
	price         Amount
	requested     Amount
	discount      Amount
	base          Amount
	schedule      FeeSchedule
	fee           Fee
	paid          Amount
	credited      Amount
	kind          PurchaseKind
	settles       string
	force         bool
	meta          Meta
	// movement is where this line's first movement sits in the operation's
	// movements, which is how the row learns the position it took in a ledger:
	// the sequence is decided by the statement that moved the balance, and only
	// that statement knows what the row said at the moment it ran.
	movement int
}

// appliedRate is the conversion an operation performed, and nil where it
// performed none.
//
// It carries the rate as it was quoted, the money that went in and the money
// that came out, which is everything the recorded row holds. It is one value
// travelling from the place that asked the provider to the place that writes
// the row, so there is exactly one rate in the operation and no second lookup
// between them.
type appliedRate struct {
	rate      Rate
	from      Money
	converted Converted
}

// appliedCharge is what an operation charged beyond the money it moved, and nil
// where it charged nothing.
//
// It carries every number the arithmetic used, which is everything the recorded
// row holds. It is one value travelling from the place that asked the providers
// to the place that writes the row, so there is exactly one discount and one
// schedule in the operation and no second lookup between them.
type appliedCharge struct {
	currency      Currency
	decimalPlaces int
	requested     Amount
	discount      Amount
	base          Amount
	schedule      FeeSchedule
	fee           Fee
}

// money reads an amount at the currency and scale this charge was counted in.
func (c appliedCharge) money(amount Amount) Money {
	return Money{Amount: amount, Currency: c.currency, DecimalPlaces: c.decimalPlaces}
}

// movement is one wallet's share of an operation.
type movement struct {
	wallet *Wallet
	kind   EntryKind
	amount Amount
	// pending records the movement without moving the balance. The entry it
	// writes takes its place in the wallet's history and counts for nothing
	// until a confirmation appends the settled entry beside it.
	pending bool
	// force lowers this movement's floor to the range of the column, and is
	// authorized where the request that asked for it is read. It is a field
	// rather than a second kind of movement, so there is one statement, one
	// guard and one place the floor is decided.
	force bool
	// adjust makes this movement the ledger row that closes a difference
	// between a wallet's entries and the balance column beside them.
	//
	// It moves no balance, and that is the whole of it: the column already
	// holds the number, and what was missing was an entry explaining it, so the
	// row is appended and the column is left exactly as it was. It is also the
	// one movement that writes while a wallet is frozen, because it is what
	// lifts the freeze.
	adjust bool
	// meta is what the application attached to this side of the operation.
	meta Meta
}

// withdrawalFloor is what the balance has to be at least for a withdrawal to
// happen, written as the expression its statement carries.
//
// Ordinarily it is the amount less the wallet's credit limit, which is the same
// thing as saying the balance may not end up further below zero than the limit
// allows. The column is named rather than read, so the limit that applies is
// the one the row holds at the moment of the write -- a limit fetched a moment
// earlier is a limit two concurrent withdrawals both spend.
//
// A forced movement lowers the floor to the smallest value the column can hold,
// and no further. The guard stays on the statement and stops being about the
// money, which is the only thing force is allowed to change: a statement with no
// guard at all would let the column wrap, and a balance that wrapped has changed
// sign.
//
// The amount goes into the SQL rather than into a placeholder because it stands
// beside a column on the right of a comparison. It is an int64 this package
// parsed, never text a caller wrote.
func withdrawalFloor(amount Amount, force bool) query.Expression {
	if force {
		return query.Raw(strconv.FormatInt(math.MinInt64+int64(amount), 10))
	}
	return query.Raw(strconv.FormatInt(int64(amount), 10) + ` - "credit_limit"`)
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

// released is what an adjustment writes: its own position in the ledger, and
// the freeze lifted.
//
// One statement and not two, because a wallet stops being frozen for exactly
// the reason the row was appended. Two statements would leave a moment in which
// one of the two had happened, and the moment where the freeze is gone and the
// row is not is a wallet serving a balance nothing explains.
func released() map[string]any {
	values := stepped()
	values["frozen"] = int64(0)
	return values
}

// maxCommitAttempts is how many times one movement is sent again after the
// engine refused it as a conflict with another transaction.
//
// Four, and the number is a bound rather than a preference. A request retried
// without one is a request that never answers, and the caller holding a
// connection open for it is the one whose own timeout ends up deciding. Three
// further attempts clear the contention that two transactions crossing produce;
// what they do not clear is a wallet every request in the system is queueing
// behind, and that is a shape to report rather than to wait out.
const maxCommitAttempts = 4

// commitBackoff is how long to wait before the attempt after this one.
//
// It doubles, and it carries a random half. Two transactions that conflicted
// did so because they ran together, and two that retry after the same fixed
// pause run together again -- so the pause is a range rather than a number, and
// the two come back at different moments. It is measured in milliseconds
// because the losing transaction's row locks are already released: what is
// being waited out is the winner finishing, not a timeout.
func commitBackoff(attempt int) time.Duration {
	step := int64(1<<attempt) * int64(time.Millisecond)
	return time.Duration(step/2 + rand.Int64N(step/2))
}

// conflictCodes are the answers that mean the engine rolled this transaction
// back over another one, rather than anything about the request.
//
// Serialization failure and deadlock, by SQLSTATE, and not the whole of class
// 40. The class also holds an integrity constraint violation, which would
// repeat on every attempt, and a completion of unknown outcome, which is not
// something to decide by class -- so the two that mean "nothing was written,
// send it again" are named and the rest are not.
var conflictCodes = map[string]bool{
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
}

// conflicted reports that the engine refused this transaction as a conflict
// with another one.
//
// The answer is read through an interface a driver satisfies rather than by
// importing a driver: a driver imported here would be a driver in every build
// that installs this package, and the SQLSTATE is the one thing the engines
// spell the same way. A driver that reports no state answers no, which is the
// safe direction -- an error nobody classified is reported to the caller
// instead of being sent again.
//
// SQLite has no SQLSTATE and reaches none of these. Its writers are serialized
// by the database itself, so what contention produces there is a busy file that
// the driver's own handle waits on, and a transaction that reaches this package
// with an error has failed for a reason retrying will not change.
func conflicted(err error) bool {
	var stated interface{ SQLState() string }
	if !errors.As(err, &stated) {
		return false
	}
	return conflictCodes[stated.SQLState()]
}

// pause waits, and gives up where the caller has.
//
// A retry that ignored the context would go on holding a request whose client
// has gone, which is the failure the deadline exists to prevent.
func pause(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// supported reports whether this package has been run against the engine a
// handle speaks.
//
// PostgreSQL and SQLite, and the list is what the suite covers rather than what
// the SQL would compile on. Every statement here is written through the Model
// and would run on more engines than these two. What does not travel with it is
// the reading of the guard: what an update sees of a row another transaction is
// changing is the engine's answer, nothing that compiles checks it, and an
// engine no test has interleaved two withdrawals on is an engine whose answer
// nobody here has read.
func supported(dialect data.Dialect) error {
	switch dialect {
	case data.DialectPostgres, data.DialectSQLite:
		return nil
	}
	return fmt.Errorf("%w: got %q", ErrUnsupportedDialect, dialect)
}

// isolate names the isolation level of a transaction this package opened.
//
// The guard on a balance is a predicate on the update, and what that predicate
// is evaluated against, while another transaction is changing the same row, is
// the isolation level's answer. Unstated it is the engine's default -- which is
// not the same on every engine and is a setting an operator can change for a
// whole cluster -- so the level the guard was written against is named here
// instead of assumed.
//
// Read committed, and not something stricter. At this level the update
// re-evaluates its predicate against the row as the other transaction left it,
// which is exactly what "the balance has to still be enough at the moment of
// the write" means. A stricter level does not make the guard more correct: it
// makes the second transaction abort where it would have re-decided, which is a
// conflict to send again rather than an answer.
//
// It runs as the first statement of the transaction, because that is the only
// place an engine takes it, and it is skipped where this package joined a
// transaction the application had already opened: the level of that one is the
// application's, and changing it from inside would be this package deciding
// about statements it cannot see.
func (s *WalletService) isolate(ctx context.Context) error {
	if s.db.Dialect() != data.DialectPostgres {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED"); err != nil {
		return fmt.Errorf("wallet: naming the isolation level of the transaction: %w", err)
	}
	return nil
}

// commit records the operation and applies its movements, all or nothing, and
// sends it again where the engine refused it as a conflict.
//
// The operation row goes in first, and that ordering is the idempotency: the
// unique index on the tenant and the key is what refuses a second request under
// one name, so two identical calls arriving together cannot both reach the
// balance -- one of them fails at the index and rolls back with nothing
// written. The loser then finds the winner's operation and answers with it,
// which is why a failure here is looked up before it is reported.
//
// # Why sending it again is safe
//
// Everything one attempt writes is inside one transaction, so a failed attempt
// left nothing behind. What the next attempt is given is the same values: the
// operation and the movements arrive as data, and nothing in here asks a rate,
// a fee, a discount or a product again -- there is nowhere it could, because
// none of those seams is reachable from this call. So a second attempt cannot
// price the request differently from the first.
//
// And where an attempt did commit and the failure was in hearing so, the key is
// what settles it: the second attempt's operation row collides with the first
// one's on the unique index, and the lookup answers with what the first one
// did. That is the same path a second caller under a shared key takes, and it
// is checked before the error is classified, so an outcome nobody could observe
// is answered with the outcome rather than retried into a second movement.
//
// A conflict that survives the attempts is ErrConcurrencyConflict, which says
// exactly that: nothing was written, and the same request under the same key is
// still safe to send.
func (s *WalletService) commit(ctx context.Context, g security.Grant, op operation, movements []movement) (Receipt, error) {
	for attempt := 1; ; attempt++ {
		receipt, err := s.attempt(ctx, g, op, movements)
		if err == nil {
			// After the transaction, and never inside it. Everything above
			// either committed or left nothing behind, so what is told here is
			// what happened -- a listener told about a movement that was then
			// rolled back has told somebody about a thing that did not happen.
			s.notify(ctx, g, movedEvents(receipt.Operation, receipt.Entries, touched(movements))...)
			return receipt, nil
		}

		// A key already spent answers with what it did, whatever this attempt
		// failed on.
		if replayed, found, lookupErr := s.replay(ctx, g, op.key, op.kind); lookupErr == nil && found {
			return replayed, nil
		}
		if !conflicted(err) {
			return Receipt{}, err
		}
		if attempt >= maxCommitAttempts {
			return Receipt{}, fmt.Errorf("%w: %d attempts: %w", ErrConcurrencyConflict, attempt, err)
		}
		if waited := pause(ctx, commitBackoff(attempt)); waited != nil {
			return Receipt{}, waited
		}
	}
}

// attempt records the operation and applies its movements once.
func (s *WalletService) attempt(ctx context.Context, g security.Grant, op operation, movements []movement) (Receipt, error) {
	// Whether this call is the one opening the transaction, asked before it is
	// opened. An application that already had one is joined rather than
	// interrupted, and the level it chose stays its own.
	opening := !data.InTransaction(ctx, s.db)

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
	record.Meta = op.meta
	// An operation settles exactly one thing: the operation it reverses, the
	// operation it confirms, or itself. With the kind beside it in a unique
	// index, that one column carries the whole rule that an operation is undone
	// at most once and confirmed at most once, and a row that settles nothing
	// collides with nothing -- including with the reversal or the confirmation
	// that names it, which differ from it in kind.
	record.SettlesID = op.settles
	if record.SettlesID == "" {
		record.SettlesID = id
	}

	var entries []Entry
	var conversion *Conversion
	var charge *Charge
	var purchases []Purchase
	err = data.Transaction(ctx, s.db, func(ctx context.Context) error {
		if opening {
			if err := s.isolate(ctx); err != nil {
				return err
			}
		}
		if _, err := record.Save(ctx, g); err != nil {
			return err
		}
		// The rate goes in with the operation and before the money moves, so
		// there is no state in which an exchange settled and what it was worth
		// is missing. It is the same transaction as the entries: a conversion
		// nobody can read is a movement nobody can explain, and the two either
		// both exist or neither does.
		if op.rate != nil {
			quoted, err := s.recordConversion(ctx, g, id, *op.rate)
			if err != nil {
				return err
			}
			conversion = quoted
		}
		// And the charge with it, for the same reason: a receipt that says a
		// different number from the request has to say why in the same
		// transaction that moved the money, or there is a state in which the
		// payment settled and what it cost is missing.
		if op.charge != nil {
			taken, err := s.recordCharge(ctx, g, id, *op.charge)
			if err != nil {
				return err
			}
			charge = taken
		}
		written, err := s.apply(ctx, g, id, movements)
		if err != nil {
			return err
		}
		entries = written
		// And the lines with them, in the same transaction and after the
		// movements: the position a line takes in a ledger is decided by the
		// statement that moved the balance, and there is no state in which
		// money moved for a basket and what it bought is missing.
		bought, err := s.recordPurchases(ctx, g, id, op.lines, written)
		if err != nil {
			return err
		}
		purchases = bought
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}

	return Receipt{
		Operation:  *record,
		Entries:    entries,
		Conversion: conversion,
		Charge:     charge,
		Purchases:  purchases,
	}, nil
}

// touched is the wallets an operation moved, by identifier.
//
// They are read off the movements rather than the database, because the
// movements already hold them: an event says what currency and scale an amount
// is in, and a read to find that out again would be a statement per wallet after
// the transaction that changed it.
func touched(movements []movement) map[string]Wallet {
	out := make(map[string]Wallet, len(movements))
	for _, m := range movements {
		if m.wallet != nil {
			out[m.wallet.ID] = *m.wallet
		}
	}
	return out
}

// recordPurchases writes the lines of a basket, inside the caller's
// transaction.
//
// Every number comes off the value the caller already holds. Nothing here asks
// a seam again, and there is nowhere it could: the price, the discount and the
// schedule arrived as data, and a second lookup would be a second answer in one
// basket -- which is a receipt that says one thing and a ledger that did
// another.
//
// The rows go in one statement, for the reason the entries do: a basket of
// forty lines that wrote forty inserts would pay forty round trips for rows
// that were all decided before the first of them was sent.
func (s *WalletService) recordPurchases(ctx context.Context, g security.Grant, operationID string, lines []purchaseLine, entries []Entry) ([]Purchase, error) {
	if len(lines) == 0 {
		return nil, nil
	}

	written := make([]Purchase, 0, len(lines))
	rows := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		id, err := data.NewID()
		if err != nil {
			return nil, err
		}
		// The sequence is read off the movement this line produced, which is
		// the position it took in a ledger. A line naming no movement would be
		// a row nothing could order, so the index is checked rather than
		// trusted.
		if line.movement < 0 || line.movement >= len(entries) {
			return nil, fmt.Errorf("wallet: the line at position %d names no movement", line.position)
		}

		row := Purchase{
			ID:                   id,
			TenantID:             data.Tenant(g),
			OperationID:          operationID,
			Position:             line.position,
			PayerWalletID:        line.payer,
			OwnerWalletID:        line.owner,
			ReceiverWalletID:     line.receiver,
			ProductKey:           line.productKey,
			Quantity:             line.quantity,
			Currency:             line.currency,
			DecimalPlaces:        line.decimalPlaces,
			PricePerItem:         line.price,
			RequestedAmount:      line.requested,
			Discount:             line.discount,
			BaseAmount:           line.base,
			FeeNumerator:         line.schedule.Numerator,
			FeeDenominator:       line.schedule.Denominator,
			FeeMinimum:           line.schedule.Minimum,
			FeeMaximum:           line.schedule.Maximum,
			FeeDeductible:        Flag(line.schedule.Deductible),
			FeeAmount:            line.fee.Amount,
			FeeWalletID:          line.schedule.WalletID,
			Rounding:             RoundDown,
			RemainderNumerator:   line.fee.RemainderNumerator,
			RemainderDenominator: line.fee.RemainderDenominator,
			PaidAmount:           line.paid,
			CreditedAmount:       line.credited,
			Kind:                 line.kind,
			SettlesID:            line.settles,
			Sequence:             entries[line.movement].Sequence,
			CreatedAt:            time.Now().UTC(),
		}
		// A line settles exactly one thing: the line a refund gives back, or
		// itself. With the kind beside it in a unique index, that one column
		// carries the whole rule that a line is refunded at most once.
		if row.SettlesID == "" {
			row.SettlesID = id
		}
		written = append(written, row)
		rows = append(rows, purchaseRow(row))
	}

	if _, err := Purchases(s.db).NewQuery().Insert(ctx, g, rows...); err != nil {
		return nil, err
	}
	return written, nil
}

// purchaseRow is one purchase as the insert writes it.
//
// The columns are named here and nowhere else, so the batch that writes forty
// of them and the value a receipt answers with are the same fields: a column
// added to the entity and forgotten here would be a row that stored a default
// while the receipt reported what was charged.
func purchaseRow(row Purchase) map[string]any {
	return map[string]any{
		"id":                    row.ID,
		"operation_id":          row.OperationID,
		"position":              row.Position,
		"payer_wallet_id":       row.PayerWalletID,
		"owner_wallet_id":       row.OwnerWalletID,
		"receiver_wallet_id":    row.ReceiverWalletID,
		"product_key":           row.ProductKey,
		"quantity":              row.Quantity,
		"currency":              string(row.Currency),
		"decimal_places":        row.DecimalPlaces,
		"price_per_item":        int64(row.PricePerItem),
		"requested_amount":      int64(row.RequestedAmount),
		"discount":              int64(row.Discount),
		"base_amount":           int64(row.BaseAmount),
		"fee_numerator":         row.FeeNumerator,
		"fee_denominator":       row.FeeDenominator,
		"fee_minimum":           int64(row.FeeMinimum),
		"fee_maximum":           int64(row.FeeMaximum),
		"fee_deductible":        row.FeeDeductible,
		"fee_amount":            int64(row.FeeAmount),
		"fee_wallet_id":         row.FeeWalletID,
		"rounding":              string(row.Rounding),
		"remainder_numerator":   row.RemainderNumerator,
		"remainder_denominator": row.RemainderDenominator,
		"paid_amount":           int64(row.PaidAmount),
		"credited_amount":       int64(row.CreditedAmount),
		"kind":                  string(row.Kind),
		"settles_id":            row.SettlesID,
		"sequence":              row.Sequence,
		"created_at":            row.CreatedAt,
	}
}

// recordConversion writes the rate an operation converted at, inside the
// caller's transaction.
//
// Every number comes off the value the caller already holds. Nothing here asks
// the provider again, and there is nowhere it could: the rate arrived as data,
// and a second lookup would be a second rate in one operation -- which is a
// receipt that says one thing and a ledger that did another.
func (s *WalletService) recordConversion(ctx context.Context, g security.Grant, operationID string, applied appliedRate) (*Conversion, error) {
	id, err := data.NewID()
	if err != nil {
		return nil, err
	}
	instance, err := Conversions(s.db).NewInstance(nil, false)
	if err != nil {
		return nil, err
	}

	written := instance.Entity
	written.ID = id
	written.TenantID = data.Tenant(g)
	written.OperationID = operationID
	written.FromCurrency = applied.from.Currency
	written.FromDecimalPlaces = applied.from.DecimalPlaces
	written.FromAmount = applied.from.Amount
	written.ToCurrency = applied.converted.Money.Currency
	written.ToDecimalPlaces = applied.converted.Money.DecimalPlaces
	written.ToAmount = applied.converted.Money.Amount
	written.RateNumerator = applied.rate.Numerator
	written.RateDenominator = applied.rate.Denominator
	written.QuotedAt = applied.rate.QuotedAt.UTC()
	written.Rounding = RoundDown
	written.RemainderNumerator = applied.converted.RemainderNumerator
	written.RemainderDenominator = applied.converted.RemainderDenominator
	written.CreatedAt = time.Now().UTC()
	if _, err := written.Save(ctx, g); err != nil {
		return nil, err
	}
	return written, nil
}

// recordCharge writes what an operation charged, inside the caller's
// transaction.
//
// Every number comes off the value the caller already holds. Nothing here asks
// a provider again, and there is nowhere it could: the schedule and the discount
// arrived as data, and a second lookup would be a second answer in one
// operation -- which is a receipt that says one thing and a ledger that did
// another.
func (s *WalletService) recordCharge(ctx context.Context, g security.Grant, operationID string, applied appliedCharge) (*Charge, error) {
	id, err := data.NewID()
	if err != nil {
		return nil, err
	}
	instance, err := Charges(s.db).NewInstance(nil, false)
	if err != nil {
		return nil, err
	}

	written := instance.Entity
	written.ID = id
	written.TenantID = data.Tenant(g)
	written.OperationID = operationID
	written.Currency = applied.currency
	written.DecimalPlaces = applied.decimalPlaces
	written.RequestedAmount = applied.requested
	written.Discount = applied.discount
	written.BaseAmount = applied.base
	written.FeeNumerator = applied.schedule.Numerator
	written.FeeDenominator = applied.schedule.Denominator
	written.FeeMinimum = applied.schedule.Minimum
	written.FeeMaximum = applied.schedule.Maximum
	written.FeeDeductible = Flag(applied.schedule.Deductible)
	written.FeeAmount = applied.fee.Amount
	written.FeeWalletID = applied.schedule.WalletID
	written.Rounding = RoundDown
	written.RemainderNumerator = applied.fee.RemainderNumerator
	written.RemainderDenominator = applied.fee.RemainderDenominator
	written.CreatedAt = time.Now().UTC()
	if _, err := written.Save(ctx, g); err != nil {
		return nil, err
	}
	return written, nil
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
//
// The rows are appended in one statement rather than one each. A balance has to
// move a wallet at a time, because each carries its own guard and each answers
// separately; the ledger does not, and a basket of twenty lines that wrote
// twenty inserts would pay twenty round trips for rows that were all decided
// before the first of them was sent.
func (s *WalletService) apply(ctx context.Context, g security.Grant, operationID string, movements []movement) ([]Entry, error) {
	order := make([]int, len(movements))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return movements[order[a]].wallet.ID < movements[order[b]].wallet.ID
	})

	entries := make([]Entry, len(movements))
	rows := make([]map[string]any, 0, len(movements))
	for _, index := range order {
		entry, err := s.move(ctx, g, operationID, index, movements[index])
		if err != nil {
			return nil, err
		}
		entries[index] = entry
		rows = append(rows, entryRow(entry))
	}
	if len(rows) == 0 {
		return entries, nil
	}
	if _, err := Entries(s.db).NewQuery().Insert(ctx, g, rows...); err != nil {
		return nil, err
	}
	return entries, nil
}

// entryRow is one ledger row as the insert writes it.
//
// The columns are named here and nowhere else, so the batch that writes twenty
// of them and the value a receipt answers with are the same fields: a column
// added to the entity and forgotten here would be a row that stored a default
// while the receipt reported what the caller asked for.
func entryRow(entry Entry) map[string]any {
	return map[string]any{
		"id":            entry.ID,
		"operation_id":  entry.OperationID,
		"wallet_id":     entry.WalletID,
		"kind":          string(entry.Kind),
		"sequence":      entry.Sequence,
		"position":      entry.Position,
		"amount":        int64(entry.Amount),
		"balance_after": int64(entry.BalanceAfter),
		"settled":       entry.Settled,
		"meta":          entry.Meta,
		"created_at":    entry.CreatedAt,
	}
}

// move applies one movement: the guarded balance update, then the row it
// produced.
//
// It does not write the row. What comes back is the entry the batch above
// appends, so that the statement which moves a balance and the statement which
// records that it moved stay one per wallet and one per operation.
func (s *WalletService) move(ctx context.Context, g security.Grant, operationID string, position int, m movement) (Entry, error) {
	rows := Wallets(s.db)

	var affected int64
	var err error
	switch {
	case m.adjust:
		// The one statement that writes while a wallet is frozen, because it is
		// what lifts the freeze. It moves no balance: the column already holds
		// the number and what was missing is the row explaining it.
		//
		// What it guards on is that nothing has moved since the difference was
		// measured. The balance and the position are both named, so a movement
		// that got in between leaves this matching no row, and the whole
		// transaction rolls back rather than writing an adjustment computed
		// from numbers that have changed.
		affected, err = rows.NewQuery().
			WhereKey(m.wallet.ID).
			Where("frozen", "=", int64(1)).
			Where("balance", "=", int64(m.wallet.Balance)).
			Where("last_sequence", "=", m.wallet.LastSequence).
			Update(ctx, g, released())
	case m.pending:
		// A movement that has not settled moves no balance, so its statement
		// touches none. It still takes a position in the ledger, because the
		// row it writes is part of that wallet's history and a history is read
		// in order -- and the position has to be one nothing else can be
		// holding, which is why it comes from the statement rather than from a
		// number read here.
		affected, err = rows.NewQuery().
			WhereKey(m.wallet.ID).
			Where("frozen", "=", int64(0)).
			Update(ctx, g, stepped())
	case m.kind == EntryWithdraw:
		// The balance has to still be enough at the moment of the write, and
		// what "enough" is comes off the same row in the same statement. The
		// freeze is in the same predicate for the same reason: a wallet whose
		// ledger stopped explaining its balance is refused by the write, not by
		// a flag somebody read before it.
		affected, err = rows.NewQuery().
			WhereKey(m.wallet.ID).
			Where("frozen", "=", int64(0)).
			Where("balance", ">=", withdrawalFloor(m.amount, m.force)).
			Decrement(ctx, g, "balance", int64(m.amount), stepped())
	case m.kind == EntryDeposit:
		// And it has to still have room, or the column wraps into a negative
		// balance that no rule in this package would ever have allowed.
		affected, err = rows.NewQuery().
			WhereKey(m.wallet.ID).
			Where("frozen", "=", int64(0)).
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
		switch {
		case m.adjust:
			// The row is still there and it did not match, so either the wallet
			// moved since the difference was measured or somebody lifted the
			// freeze. Either way the number this adjustment carries is about a
			// state the wallet has left.
			return Entry{}, ErrLedgerMoved
		case bool(after.Frozen):
			// Read off the row this transaction just failed to update, so the
			// answer is the column's and not a flag from before the statement.
			return Entry{}, ErrWalletFrozen
		case m.pending:
			// Nothing about the money can have refused this one, so the row is
			// gone: another statement in this transaction would have read it.
			return Entry{}, ErrNotFound
		case m.kind == EntryWithdraw:
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
	written.Settled = Flag(!m.pending)
	written.Meta = m.meta
	written.CreatedAt = time.Now().UTC()
	return *written, nil
}

// replay answers with what this idempotency key already did, if anything.
//
// A key that names an operation of another kind is a conflict rather than a
// replay: one key cannot be the name of two different requests, and answering
// the deposit's receipt to a withdrawal would be answering a question nobody
// asked.
//
// A replayed exchange answers with the rate the first call was quoted, read
// back off the row rather than asked for again. That is what makes idempotency
// mean the same thing for a conversion as for anything else: the same key twice
// converts once, at one rate, and the second answer is the first answer.
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

	// Only an exchange has one, so only an exchange is asked for one. A read
	// on every replay would be a statement per deposit that answers nothing.
	var conversion *Conversion
	if record.Kind == OperationExchange {
		conversion, err = Conversions(s.db).NewQuery().
			Where("operation_id", "=", record.ID).
			First(ctx, g)
		if err != nil {
			return Receipt{}, false, err
		}
	}

	// And only a payment between two wallets can have been charged, so only one
	// is asked. A replayed payment answers with what the first call charged,
	// read back off the row rather than decided again: the same key twice pays
	// once, at one price.
	var charge *Charge
	if record.Kind == OperationTransfer || record.Kind == OperationExchange {
		charge, err = Charges(s.db).NewQuery().
			Where("operation_id", "=", record.ID).
			First(ctx, g)
		if err != nil {
			return Receipt{}, false, err
		}
	}
	// And only a basket has lines, so only a basket is asked for them. A
	// replayed purchase answers with what the first call bought, read back off
	// the rows rather than priced again: the same key twice pays once, at one
	// price.
	var purchases []Purchase
	if record.Kind == OperationPurchase || record.Kind == OperationRefund {
		lines, err := Purchases(s.db).NewQuery().
			Where("operation_id", "=", record.ID).
			OrderBy("position").
			Get(ctx, g)
		if err != nil {
			return Receipt{}, false, err
		}
		for _, line := range lines {
			if line != nil {
				purchases = append(purchases, *line)
			}
		}
	}
	return Receipt{
		Operation:  *record,
		Entries:    entries,
		Conversion: conversion,
		Charge:     charge,
		Purchases:  purchases,
		Replayed:   true,
	}, true, nil
}

// reversed reports whether an operation has already been undone.
func (s *WalletService) reversed(ctx context.Context, g security.Grant, operationID string) (bool, error) {
	return Operations(s.db).NewQuery().
		Where("reverses_id", "=", operationID).
		Where("kind", "=", string(OperationReversal)).
		Exists(ctx, g)
}

// mirror turns the settled entries of an operation into the movements that undo
// it, and asks the policy about every wallet they touch.
//
// The question is asked per wallet and not once for the operation, because a
// transfer's two entries are two people's money and a reversal moves both.
//
// Only the entries that settled are mirrored, and an operation that settled
// none has nothing to undo: what a pending operation wrote is a record of what
// was proposed, and appending its opposite would take out money that was never
// put in. That is one rule and not a second path -- you undo the operation that
// moved the money, which for a movement that waited is the confirmation and not
// the request.
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
		if entry == nil || !entry.Settled {
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
	if len(movements) == 0 {
		return nil, ErrNotSettled
	}
	return movements, nil
}

// settle turns the pending entries of an operation into the movements that make
// them count, and asks the policy about every wallet they touch.
//
// Each movement is the pending one again, in the same direction and for the same
// amount: what is being confirmed is what was written down, so nothing here
// recomputes it. An exchange is not re-quoted either -- the rate belongs to the
// operation that quoted it and is on the record beside it, and asking again
// would settle at a number nobody was told.
//
// The amount is read off the entry rather than from the wallet, and the guard
// runs at this write. A pending withdrawal holds nothing, so the balance that
// decides is the balance now.
func (s *WalletService) settle(ctx context.Context, g security.Grant, actor security.Subject, in ConfirmRequest, operationID string) ([]movement, error) {
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
		if entry == nil || entry.Settled {
			continue
		}
		holder, err := rows.NewQuery().WhereKey(entry.WalletID).First(ctx, g)
		if err != nil {
			return nil, err
		}
		if holder == nil {
			return nil, ErrNotFound
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletConfirm, *holder); err != nil {
			return nil, err
		}
		if err := s.allowForce(ctx, actor, in.Force, *holder); err != nil {
			return nil, err
		}
		movements = append(movements, movement{
			wallet: holder, kind: entry.Kind, amount: entry.Amount, force: in.Force,
			// The same facts as the movement being settled. It is the same
			// movement, now counting, so a receipt that lost what it was about
			// would be a receipt about a different payment.
			meta: entry.Meta,
		})
	}
	if len(movements) == 0 {
		return nil, ErrNotPending
	}
	return movements, nil
}

// confirmed reports whether an operation has already been made to count.
func (s *WalletService) confirmed(ctx context.Context, g security.Grant, operationID string) (bool, error) {
	return Operations(s.db).NewQuery().
		Where("reverses_id", "=", operationID).
		Where("kind", "=", string(OperationConfirmation)).
		Exists(ctx, g)
}

// convert is how much arrives in the target wallet, and the rate that decided
// it.
//
// The same number and no rate when both wallets count the same thing at the
// same scale. Otherwise the provider is asked once, and the rate it answers
// with is the value everything downstream uses: the multiplication here, the
// row commit writes, and the receipt the caller reads. There is one call and
// one variable, which is the whole of "the rate does not change in the middle
// of the operation" -- a second lookup could answer differently, and then the
// money that moved and the rate on the record would be two different stories
// about one payment.
//
// The provider is not asked to convert, only to quote. What the rate does to
// an amount is this package's arithmetic, under this package's one rounding
// rule, so two conversions of the same amount at the same rate are the same
// number wherever the rate came from.
func (s *WalletService) convert(ctx context.Context, g security.Grant, source, target Wallet, debited Amount) (Amount, *appliedRate, error) {
	if !converts(source, target) {
		return debited, nil, nil
	}
	if s.rates == nil {
		return 0, nil, ErrCurrencyMismatch
	}

	rate, err := s.rates.Rate(ctx, g, source.Currency, target.Currency)
	if err != nil {
		return 0, nil, err
	}

	from := source.Money(debited)
	converted, err := rate.Convert(from, target.Currency, target.DecimalPlaces)
	if err != nil {
		return 0, nil, err
	}
	return converted.Money.Amount, &appliedRate{rate: rate, from: from, converted: converted}, nil
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

// MaxPurchaseScan is how many purchase rows one batch question reads.
//
// A bound rather than none, for the reason a page has one: an unbounded read is
// how one call takes a production database down on the day a customer has a
// long history. It is stated rather than hidden because it is a real limit --
// a question about a wallet with more recent purchases than this, from the
// wallets named beside it, is answered from what the scan reached.
const MaxPurchaseScan = 2000

// MaxPurchaseQuestions is how many questions one batch may carry.
const MaxPurchaseQuestions = 100

// Pay buys a basket with one wallet's money.
//
// Every line is one movement out of the payer and one into the wallet that
// sells it, plus a third into whoever collects the fee, and all of them are one
// operation and one transaction. There is no state in which half a basket was
// paid for: a line the application refuses, a price that does not fit or a
// balance that runs out on the fourth of six leaves nothing written at all.
//
// The prices, the discounts and the fees are the application's, through the
// seams it already supplies. What this package owns is the arithmetic and the
// record: what was asked for, what was taken off, what the fee was computed
// from and what actually left and arrived are all on the line's own row, so a
// receipt that says a different number from the catalogue says why.
//
// A basket crosses no rate. Every wallet it names has to be counted the way the
// payer's is, because a basket that converted line by line would round once per
// line and the total would not be the total of anything -- an application
// selling in another currency prices the line in the payer's money, which is
// what Product.Price is asked for.
//
// A line bought for somebody else is a gift: the money still leaves the payer
// and still arrives at the seller, and the record says the beneficiary bought
// it. That is the whole of what a gift changes, and it is what "has this person
// already got one" reads afterwards.
func (s *WalletService) Pay(ctx context.Context, actor security.Subject, in PayRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletPay, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationPurchase); err != nil || found {
		return receipt, err
	}

	payer, err := Wallets(s.db).NewQuery().WhereKey(in.PayerWalletID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if payer == nil {
		return Receipt{}, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletPay, *payer); err != nil {
		return Receipt{}, err
	}
	if err := s.allowForce(ctx, actor, in.Force, *payer); err != nil {
		return Receipt{}, err
	}

	priced, err := s.priceBasket(ctx, g, *payer, in.Cart)
	if err != nil {
		return Receipt{}, err
	}

	return s.commit(ctx, g, operation{
		key:   in.IdempotencyKey,
		kind:  OperationPurchase,
		meta:  in.Cart.Meta(),
		lines: priced.lines,
	}, priced.movements)
}

// basket is a priced cart: the lines as they will be recorded and the movements
// that pay for them, in step.
type basket struct {
	lines     []purchaseLine
	movements []movement
}

// priceBasket asks the application what the basket costs and turns it into
// movements.
//
// Every wallet the basket names is read in two statements rather than one per
// line: the sellers and the beneficiaries together, and then the wallets that
// collect fees, which are not known until the schedules have been answered. A
// basket of forty lines therefore costs the same reads as a basket of two, and
// the number of statements does not depend on what somebody put in it.
//
// Nothing here writes. The application is asked about stock before any money is
// judged and about price before any is moved, so a line it refuses is a refusal
// with an empty ledger behind it.
func (s *WalletService) priceBasket(ctx context.Context, g security.Grant, payer Wallet, cart Cart) (basket, error) {
	items := cart.Items()
	named := make([]any, 0, 2*len(items))
	seen := make(map[string]bool, 2*len(items))
	for _, item := range items {
		for _, id := range []string{item.receiver(), item.BeneficiaryWalletID} {
			if id == "" || id == payer.ID || seen[id] {
				continue
			}
			seen[id] = true
			named = append(named, id)
		}
	}

	wallets, err := s.walletsByID(ctx, g, named)
	if err != nil {
		return basket{}, err
	}
	wallets[payer.ID] = payer

	// What the fee is a share of is decided first, for every line, because the
	// wallets that collect those fees are read together afterwards.
	priced := make([]purchaseLine, 0, len(items))
	collectors := make([]any, 0, len(items))
	wanted := make(map[string]bool, len(items))
	for position, item := range items {
		line, err := s.priceLine(ctx, g, payer, wallets, position, item)
		if err != nil {
			return basket{}, err
		}
		priced = append(priced, line)
		if id := line.schedule.WalletID; id != "" && !wanted[id] {
			wanted[id] = true
			collectors = append(collectors, id)
		}
	}

	collecting, err := s.walletsByID(ctx, g, collectors)
	if err != nil {
		return basket{}, err
	}

	out := basket{lines: make([]purchaseLine, 0, len(priced)), movements: make([]movement, 0, 3*len(priced))}
	for _, line := range priced {
		var collector *Wallet
		if line.schedule.Charges() {
			held, known := collecting[line.schedule.WalletID]
			if !known {
				return basket{}, fmt.Errorf("%w: %s is not a wallet of this customer", ErrFeeWallet, line.schedule.WalletID)
			}
			if converts(payer, held) {
				return basket{}, fmt.Errorf("%w: the line is %s and the fee would be credited in %s",
					ErrFeeCurrencyMismatch, payer.Money(line.base), held.Money(0))
			}
			collector = &held
		}

		receiver := wallets[line.receiver]
		line.movement = len(out.movements)
		out.movements = append(out.movements, movement{
			wallet: &receiver, kind: EntryDeposit, amount: line.credited, meta: line.meta,
		})
		out.movements = append(out.movements, movement{
			wallet: &payer, kind: EntryWithdraw, amount: line.paid, force: line.force, meta: line.meta,
		})
		if collector != nil {
			out.movements = append(out.movements, movement{
				wallet: collector, kind: EntryDeposit, amount: line.fee.Amount,
			})
		}
		out.lines = append(out.lines, line)
	}
	return out, nil
}

// priceLine is one line of a basket, priced and checked but not yet paid for.
func (s *WalletService) priceLine(ctx context.Context, g security.Grant, payer Wallet, wallets map[string]Wallet, position int, item CartItem) (purchaseLine, error) {
	receiver, known := wallets[item.receiver()]
	if !known {
		return purchaseLine{}, ErrNotFound
	}
	if receiver.ID == payer.ID {
		return purchaseLine{}, ErrPaysItself
	}
	if converts(payer, receiver) {
		return purchaseLine{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch,
			payer.Money(0), receiver.Money(0))
	}

	// Whose purchase it is. The money is the payer's either way; what this
	// decides is who the record says bought the thing, which is what the
	// application asks about when it wants to know whether to sell it again.
	owner := payer
	if item.BeneficiaryWalletID != "" {
		held, present := wallets[item.BeneficiaryWalletID]
		if !present {
			return purchaseLine{}, ErrNotFound
		}
		owner = held
	}

	// The stock, before any money is judged. An application that answers no
	// here answers before a single balance has been touched.
	if limited, keeps := item.Product.(LimitedProduct); keeps {
		if err := limited.CanBuy(ctx, g, owner, item.quantity()); err != nil {
			return purchaseLine{}, fmt.Errorf("%w: %s: %w", ErrProductStock, item.Product.ProductKey(), err)
		}
	}

	price, err := s.priceOf(ctx, g, payer, owner, item)
	if err != nil {
		return purchaseLine{}, err
	}
	requested, err := price.Times(item.quantity())
	if err != nil {
		return purchaseLine{}, err
	}

	// What the payer is charged less comes off first, because everything after
	// it is a share of what is being paid rather than of what was asked for.
	discount, err := s.discount(ctx, g, payer, receiver, requested)
	if err != nil {
		return purchaseLine{}, err
	}
	base, err := requested.Sub(discount)
	if err != nil {
		return purchaseLine{}, err
	}
	if base <= 0 {
		return purchaseLine{}, ErrAmountNotPositive
	}

	line := purchaseLine{
		position:      position,
		payer:         payer.ID,
		owner:         owner.ID,
		receiver:      receiver.ID,
		productKey:    item.Product.ProductKey(),
		quantity:      item.quantity(),
		currency:      payer.Currency,
		decimalPlaces: payer.DecimalPlaces,
		price:         price,
		requested:     requested,
		discount:      discount,
		base:          base,
		paid:          base,
		credited:      base,
		kind:          PurchasePaid,
		meta:          item.Meta,
	}
	if owner.ID != payer.ID {
		line.kind = PurchaseGift
	}

	if s.fees == nil {
		return line, nil
	}
	schedule, err := s.fees.Fee(ctx, g, receiver, payer.Money(base))
	if err != nil {
		return purchaseLine{}, err
	}
	if !schedule.Charges() {
		return line, nil
	}
	if err := schedule.Validate(); err != nil {
		return purchaseLine{}, err
	}
	if schedule.WalletID == payer.ID || schedule.WalletID == receiver.ID {
		return purchaseLine{}, fmt.Errorf("%w: it names one of the two wallets the line is between", ErrFeeWallet)
	}
	fee, err := schedule.Fee(payer.Money(base))
	if err != nil {
		return purchaseLine{}, err
	}
	line.schedule = schedule
	line.fee = fee

	// Who pays the fee is the only thing the schedule changes about the line:
	// on top of it, or out of what arrives. Either way what leaves is what
	// arrives plus the fee, exactly.
	if schedule.Deductible {
		credited, err := base.Sub(fee.Amount)
		if err != nil {
			return purchaseLine{}, err
		}
		if credited <= 0 {
			return purchaseLine{}, fmt.Errorf("%w: %s of %s", ErrFeeExceedsAmount,
				payer.Money(fee.Amount), payer.Money(base))
		}
		line.credited = credited
		return line, nil
	}
	paid, err := base.Add(fee.Amount)
	if err != nil {
		return purchaseLine{}, err
	}
	line.paid = paid
	return line, nil
}

// priceOf is what one of a product costs, read at the payer's scale.
//
// A price written on the line wins over the one the product answers, because a
// caller that says what something costs has already decided; asking the product
// as well would be asking a question whose answer is thrown away.
func (s *WalletService) priceOf(ctx context.Context, g security.Grant, payer, owner Wallet, item CartItem) (Amount, error) {
	if item.PricePerItem != "" {
		return positiveAmount(item.PricePerItem, payer.DecimalPlaces)
	}
	price, err := item.Product.Price(ctx, g, owner)
	if err != nil {
		return 0, err
	}
	if price <= 0 {
		return 0, fmt.Errorf("%w: %s costs %s", ErrAmountNotPositive,
			item.Product.ProductKey(), payer.Money(price))
	}
	return price, nil
}

// walletsByID reads a set of wallets in one statement, keyed by identifier.
//
// One statement and not one per identifier: a basket names as many wallets as
// it has lines, and a read per line is what makes a long basket slow on exactly
// the customers who buy the most. It is read through the Model with the Grant,
// so what comes back is this customer's and a wallet that is missing from the
// answer is a wallet that does not exist here.
func (s *WalletService) walletsByID(ctx context.Context, g security.Grant, ids []any) (map[string]Wallet, error) {
	out := make(map[string]Wallet, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := Wallets(s.db).NewQuery().WhereIn("id", ids).Get(ctx, g)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row != nil {
			out[row.ID] = *row
		}
	}
	return out, nil
}

// Refund gives back some of the lines of a purchase.
//
// It moves back exactly what moved, on each side, read off the line's own row:
// what left the payer goes back to the payer, what reached the seller leaves the
// seller, and a fee that was taken leaves whoever collected it. Nothing is
// recomputed -- a schedule that answers differently today would otherwise make
// a refund of last month's purchase a different number from the purchase.
//
// Nothing already written changes. The line that was bought stays on the record
// exactly as it was bought, and a second row appears beside it naming the one it
// settles -- which is why a basket half of which was given back cannot be given
// back whole, and why a statement afterwards reads as what was bought and then
// what came back.
//
// A line is given back once. The second attempt answers ErrAlreadyRefunded, and
// the refusal is a unique index rather than a check, so two refunds arriving
// together cannot both be the one that succeeds.
func (s *WalletService) Refund(ctx context.Context, actor security.Subject, in RefundRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletRefund, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationRefund); err != nil || found {
		return receipt, err
	}

	ids := make([]any, 0, len(in.PurchaseIDs))
	for _, id := range in.PurchaseIDs {
		ids = append(ids, id)
	}
	rows, err := Purchases(s.db).NewQuery().WhereIn("id", ids).OrderBy("position").Get(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if len(rows) != len(in.PurchaseIDs) {
		return Receipt{}, ErrNotFound
	}

	lines := make([]Purchase, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			return Receipt{}, ErrNotFound
		}
		if row.Kind == PurchaseRefund {
			return Receipt{}, ErrNotRefundable
		}
		lines = append(lines, *row)
	}
	if given, err := s.refunded(ctx, g, lines); err != nil {
		return Receipt{}, err
	} else if given {
		return Receipt{}, ErrAlreadyRefunded
	}

	priced, err := s.reverseLines(ctx, g, actor, in, lines)
	if err != nil {
		return Receipt{}, err
	}

	receipt, err := s.commit(ctx, g, operation{
		key:    in.IdempotencyKey,
		kind:   OperationRefund,
		reason: in.Reason,
		meta:   in.Meta,
		lines:  priced.lines,
	}, priced.movements)
	if err != nil && !errors.Is(err, ErrInsufficientFunds) {
		// The unique index on the line being settled is what refuses a second
		// refund, and this turns its answer into ours. Read after the failure,
		// never instead of it: a check that ran before is a check two concurrent
		// refunds both passed.
		if given, lookupErr := s.refunded(ctx, g, lines); lookupErr == nil && given {
			return Receipt{}, ErrAlreadyRefunded
		}
	}
	return receipt, err
}

// reverseLines turns purchased lines into the movements that give them back,
// and asks the policy about every wallet they touch.
//
// The question is asked per wallet and not once for the request, because the
// lines of one refund are several people's money and every one of them is
// moved.
func (s *WalletService) reverseLines(ctx context.Context, g security.Grant, actor security.Subject, in RefundRequest, lines []Purchase) (basket, error) {
	named := make([]any, 0, 3*len(lines))
	seen := make(map[string]bool, 3*len(lines))
	for _, line := range lines {
		for _, id := range []string{line.PayerWalletID, line.ReceiverWalletID, line.FeeWalletID} {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			named = append(named, id)
		}
	}
	wallets, err := s.walletsByID(ctx, g, named)
	if err != nil {
		return basket{}, err
	}
	for id := range seen {
		held, known := wallets[id]
		if !known {
			return basket{}, ErrNotFound
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletRefund, held); err != nil {
			return basket{}, err
		}
		if err := s.allowForce(ctx, actor, in.Force, held); err != nil {
			return basket{}, err
		}
	}

	out := basket{lines: make([]purchaseLine, 0, len(lines)), movements: make([]movement, 0, 3*len(lines))}
	for position, line := range lines {
		payer := wallets[line.PayerWalletID]
		receiver := wallets[line.ReceiverWalletID]

		given := purchaseLine{
			position:      position,
			payer:         line.PayerWalletID,
			owner:         line.OwnerWalletID,
			receiver:      line.ReceiverWalletID,
			productKey:    line.ProductKey,
			quantity:      line.Quantity,
			currency:      line.Currency,
			decimalPlaces: line.DecimalPlaces,
			price:         line.PricePerItem,
			requested:     line.RequestedAmount,
			discount:      line.Discount,
			base:          line.BaseAmount,
			schedule:      line.Schedule(),
			fee:           Fee{Amount: line.FeeAmount, RemainderNumerator: line.RemainderNumerator, RemainderDenominator: line.RemainderDenominator},
			paid:          line.PaidAmount,
			credited:      line.CreditedAmount,
			kind:          PurchaseRefund,
			settles:       line.ID,
			force:         in.Force,
			meta:          in.Meta,
		}
		given.movement = len(out.movements)
		out.movements = append(out.movements, movement{
			wallet: &receiver, kind: EntryWithdraw, amount: line.CreditedAmount, force: in.Force, meta: in.Meta,
		})
		out.movements = append(out.movements, movement{
			wallet: &payer, kind: EntryDeposit, amount: line.PaidAmount, meta: in.Meta,
		})
		if line.FeeAmount > 0 && line.FeeWalletID != "" {
			collector := wallets[line.FeeWalletID]
			out.movements = append(out.movements, movement{
				wallet: &collector, kind: EntryWithdraw, amount: line.FeeAmount, force: in.Force,
			})
		}
		out.lines = append(out.lines, given)
	}
	return out, nil
}

// refunded reports whether any of these lines has already been given back.
func (s *WalletService) refunded(ctx context.Context, g security.Grant, lines []Purchase) (bool, error) {
	ids := make([]any, 0, len(lines))
	for _, line := range lines {
		ids = append(ids, line.ID)
	}
	if len(ids) == 0 {
		return false, nil
	}
	return Purchases(s.db).NewQuery().
		WhereIn("settles_id", ids).
		Where("kind", "=", string(PurchaseRefund)).
		Exists(ctx, g)
}

// Bought answers, for each question, the line that already bought it -- and nil
// where nothing did.
//
// One statement for the whole set rather than one per question. The rows that
// could answer any of them are read together and matched in memory, so a shop
// checking forty products against one customer makes one read and not forty --
// which is the difference between a page that loads and a page that times out on
// the customers who buy the most.
//
// A line that was given back does not answer. The refund is a row of its own
// naming the line it settles, so what is asked here is "bought and not given
// back", which is what a shop deciding whether to sell something again means.
//
// It is bounded by MaxPurchaseScan, and that bound is real: a question about a
// wallet with more recent purchases than that, among the wallets named beside
// it, is answered from what the scan reached.
func (s *WalletService) Bought(ctx context.Context, actor security.Subject, questions []PurchaseQuery) ([]*Purchase, error) {
	if len(questions) == 0 {
		return nil, nil
	}
	if len(questions) > MaxPurchaseQuestions {
		return nil, fmt.Errorf("wallet: %d questions were asked at once, and the most is %d",
			len(questions), MaxPurchaseQuestions)
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletPurchases, Wallet{})
	if err != nil {
		return nil, err
	}

	owners := make([]any, 0, len(questions))
	receivers := make([]any, 0, len(questions))
	products := make([]any, 0, len(questions))
	seen := map[string]bool{}
	for _, question := range questions {
		if question.OwnerWalletID == "" || question.ReceiverWalletID == "" || question.ProductKey == "" {
			return nil, fmt.Errorf("wallet: a question needs an owner, a receiver and a product")
		}
		collect(&owners, seen, "o:"+question.OwnerWalletID, question.OwnerWalletID)
		collect(&receivers, seen, "r:"+question.ReceiverWalletID, question.ReceiverWalletID)
		collect(&products, seen, "p:"+question.ProductKey, question.ProductKey)
	}

	// Every wallet whose purchases would be read is asked about, because a read
	// is a decision like any other: a page of what somebody bought is a page of
	// what they spend their money on.
	held, err := s.walletsByID(ctx, g, owners)
	if err != nil {
		return nil, err
	}
	for _, id := range owners {
		record, known := held[id.(string)]
		if !known {
			return nil, ErrNotFound
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletPurchases, record); err != nil {
			return nil, err
		}
	}

	rows, err := Purchases(s.db).NewQuery().
		WhereIn("owner_wallet_id", owners).
		WhereIn("receiver_wallet_id", receivers).
		WhereIn("product_key", products).
		OrderByDesc("sequence").
		OrderByDesc("id").
		Limit(MaxPurchaseScan).
		Get(ctx, g)
	if err != nil {
		return nil, err
	}

	// The refunds first, because a line that was given back answers nothing and
	// the row that says so can be anywhere in the page.
	given := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row != nil && row.Kind == PurchaseRefund {
			given[row.SettlesID] = true
		}
	}

	answers := make([]*Purchase, len(questions))
	for i, question := range questions {
		for _, row := range rows {
			if row == nil || given[row.ID] {
				continue
			}
			if row.OwnerWalletID != question.OwnerWalletID ||
				row.ReceiverWalletID != question.ReceiverWalletID ||
				row.ProductKey != question.ProductKey {
				continue
			}
			if row.Kind == PurchasePaid || (question.IncludeGifts && row.Kind == PurchaseGift) {
				answers[i] = row
				break
			}
		}
	}
	return answers, nil
}

// collect adds a value to a set of query arguments, once.
func collect(into *[]any, seen map[string]bool, key, value string) {
	if seen[key] {
		return
	}
	seen[key] = true
	*into = append(*into, value)
}

// PurchasesOf returns a page of what one wallet bought, newest first.
//
// It is a read, and it asks the policy the same two questions a read of the
// wallet itself asks: whether this subject reads purchases, and whether they
// read this wallet's. What somebody buys is at least as private as what they
// hold, so a path to it that skipped the second question would be the widest
// read in the package.
func (s *WalletService) PurchasesOf(ctx context.Context, actor security.Subject, walletID string, page data.Query) ([]*Purchase, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletPurchases, Wallet{})
	if err != nil {
		return nil, err
	}

	owner, err := Wallets(s.db).NewQuery().WhereKey(walletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletPurchases, *owner); err != nil {
		return nil, err
	}

	rows := Purchases(s.db)
	query := rows.NewQuery().Where("owner_wallet_id", "=", owner.ID)
	if page.Cursor != "" {
		anchor, err := rows.NewQuery().WhereKey(page.Cursor).Value(ctx, g, "sequence")
		if err != nil {
			return nil, err
		}
		if anchor == nil {
			return nil, nil
		}
		query = query.Where("sequence", "<", anchor)
	}
	return query.OrderByDesc("sequence").OrderByDesc("id").Limit(boundedLimit(page.Limit)).Get(ctx, g)
}

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

// Balanced reports that the ledger and the balance column agree.
func (r Reconciliation) Balanced() bool {
	if r.Settled != r.Wallet.Balance {
		return false
	}
	return r.Entries == 0 || r.LastBalanceAfter == r.Wallet.Balance
}

// Difference is what the balance column holds beyond what the ledger explains,
// and zero where the two agree.
func (r Reconciliation) Difference() Amount { return r.Wallet.Balance - r.Settled }

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

// walk sums one wallet's ledger up to the position the wallet held when it was
// read.
//
// The bound is the whole of why the answer means anything. Without it the sum
// would include rows written while the pages were being read and would be
// compared against a balance from before them, so a busy wallet would report a
// difference that is only the reading.
func (s *WalletService) walk(ctx context.Context, g security.Grant, holder Wallet) (Reconciliation, error) {
	out := Reconciliation{Wallet: holder, Frozen: bool(holder.Frozen)}
	entries := Entries(s.db)
	after := int64(0)
	expected := int64(1)
	for {
		page, err := entries.NewQuery().
			Where("wallet_id", "=", holder.ID).
			Where("sequence", ">", after).
			Where("sequence", "<=", holder.LastSequence).
			OrderBy("sequence").
			Limit(maxLimit).
			Get(ctx, g)
		if err != nil {
			return Reconciliation{}, err
		}
		if len(page) == 0 {
			return out, nil
		}
		for _, entry := range page {
			if entry == nil {
				continue
			}
			if entry.Sequence != expected {
				out.Gaps++
			}
			expected = entry.Sequence + 1

			if entry.Settled {
				sum, err := out.Settled.Add(entry.Signed())
				if err != nil {
					return Reconciliation{}, err
				}
				out.Settled = sum
			} else {
				direction := entry.Amount
				if entry.Kind == EntryWithdraw {
					direction = -entry.Amount
				}
				sum, err := out.Proposed.Add(direction)
				if err != nil {
					return Reconciliation{}, err
				}
				out.Proposed = sum
			}

			out.Entries++
			out.LastBalanceAfter = entry.BalanceAfter
			out.LastSequence = entry.Sequence
			after = entry.Sequence
		}
		if len(page) < maxLimit {
			return out, nil
		}
	}
}

// freeze stops a wallet being served.
//
// One column and one statement. It is not guarded on the balance the scan saw,
// and that is deliberate: a movement arriving after the scan carries the
// difference forward unchanged, because it adds the same amount to the column
// and to the ledger. So the conclusion stays true of the wallet as it is now,
// and refusing to freeze because it moved would leave a wallet unserved by
// nothing while the difference is still there.
func (s *WalletService) freeze(ctx context.Context, g security.Grant, walletID string) error {
	affected, err := Wallets(s.db).NewQuery().
		WhereKey(walletID).
		Update(ctx, g, map[string]any{"frozen": int64(1)})
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
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

// Validate reports the errors per field.
func (r RebuildRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "wallet_id", r.WalletID)
	validation.MaxLen(e, "wallet_id", r.WalletID, maxIdentifierLen)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	checkMeta(e, "meta", r.Meta)
	return e
}

// Compile-time proof that the request honors the validation contract.
var _ validation.Validatable = RebuildRequest{}

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

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationAdjustment); err != nil || found {
		return receipt, err
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
