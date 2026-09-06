package wallet

import (
	"context"
	"fmt"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
	"github.com/arandu-io/hesape/database/model"
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

// ListRequest is what paging through wallets takes.
type ListRequest struct {
	// Query is the page and the ordering.
	Query data.Query
	// HolderID narrows the page to one holder. It is a filter and not a
	// permission: a subject who is not an operator has it replaced by their own
	// identifier, so what they asked for cannot widen what they get.
	HolderID string
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

// CloseRequest is what taking a wallet out of service takes.
type CloseRequest struct {
	// WalletID is the wallet being closed.
	WalletID string
	// Reason is why it was closed. It is required and it is carried on the
	// event rather than on the row: what this package keeps about a wallet is
	// what decides about its money, and why somebody stopped using it is the
	// application's record to write where it writes the rest of its history.
	Reason string
}

// Close takes a wallet out of service.
//
// The row stays and the ledger stays readable, which is the difference between
// this and deleting: a wallet whose row was removed takes the meaning of its own
// statement with it, and every entry naming it becomes a movement nobody can
// place. What changes is one column, and every statement that moves a balance
// carries it -- see servable -- so a wallet closed between a read and a write is
// refused at the write.
//
// It requires the balance to be zero, and the requirement is a predicate on the
// statement rather than a check before it: a wallet closed with money in it is
// money nothing can reach afterwards, and a balance read a moment earlier is a
// balance a concurrent deposit has already changed. Where it does not hold, the
// answer is ErrWalletHoldsMoney and nothing changed.
//
// A frozen wallet can be closed. The freeze says this package cannot explain the
// balance; if that balance is zero, closing it is a decision somebody is
// entitled to make, and the ledger stays exactly as readable afterwards.
func (s *WalletService) Close(ctx context.Context, actor security.Subject, in CloseRequest) (*Wallet, error) {
	if errs := in.Validate(); errs.Any() {
		return nil, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletClose, Wallet{})
	if err != nil {
		return nil, err
	}

	record, err := Wallets(s.db).NewQuery().WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletClose, *record); err != nil {
		return nil, err
	}
	return s.setClosed(ctx, g, in.WalletID, true)
}

// Reopen puts a closed wallet back in service.
//
// It exists because closing is a decision and decisions are made wrongly. It
// changes the one column back and nothing else: the ledger was never touched,
// so a reopened wallet is the wallet it was, with the balance it had -- which is
// zero, because that is what closing required.
//
// It is the same action as closing. Deciding that a wallet is out of service and
// deciding that it is back are the same authority over the same fact, and a
// separate action would let somebody hold one half of it.
func (s *WalletService) Reopen(ctx context.Context, actor security.Subject, walletID string) (*Wallet, error) {
	e := validation.Errors{}
	validation.Required(e, "wallet_id", walletID)
	validation.MaxLen(e, "wallet_id", walletID, maxIdentifierLen)
	if e.Any() {
		return nil, e
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletClose, Wallet{})
	if err != nil {
		return nil, err
	}

	record, err := Wallets(s.db).NewQuery().WhereKey(walletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletClose, *record); err != nil {
		return nil, err
	}
	return s.setClosed(ctx, g, walletID, false)
}

// setClosed is the guarded write both Close and Reopen make, and nothing else.
//
// The authorization is not in here, and that is deliberate rather than an
// oversight: a Service method whose policy call sits behind a helper is a method
// tests/Unit/audit_test.go cannot see the boundary of, and an audit that reads
// syntax has to be able to read it. So each exported method asks, and this
// writes.
func (s *WalletService) setClosed(ctx context.Context, g security.Grant, walletID string, closing bool) (*Wallet, error) {
	rows := Wallets(s.db)

	// The state being left is in the predicate as well as the state being
	// reached, so closing a wallet twice writes once and the second call is
	// told which of the two things happened.
	was, becomes := int64(0), int64(1)
	if !closing {
		was, becomes = 1, 0
	}
	page := rows.NewQuery().WhereKey(walletID).Where("closed", "=", was)
	if closing {
		page = page.Where("balance", "=", int64(0))
	}
	affected, err := page.Update(ctx, g, map[string]any{"closed": becomes})
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, s.whyNotClosed(ctx, g, walletID, closing)
	}

	written, err := rows.NewQuery().WhereKey(walletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if written == nil {
		return nil, ErrNotFound
	}

	kind := WalletWasClosed
	if !closing {
		kind = WalletWasReopened
	}
	s.notify(ctx, g, Event{
		Kind:          kind,
		WalletID:      written.ID,
		Currency:      written.Currency,
		DecimalPlaces: written.DecimalPlaces,
		Balance:       written.Balance,
	})
	return written, nil
}

// whyNotClosed reads back the row a close or a reopen did not match, and says
// which of the two things stopped it.
//
// After the statement and never instead of it. The write is what decided; this
// only turns "no rows" into a sentence, and it reads the row again because the
// answer depends on what the row says now rather than on what it said when the
// wallet was loaded.
func (s *WalletService) whyNotClosed(ctx context.Context, g security.Grant, walletID string, closing bool) error {
	record, err := Wallets(s.db).NewQuery().WhereKey(walletID).First(ctx, g)
	if err != nil {
		return err
	}
	if record == nil {
		return ErrNotFound
	}
	switch {
	case closing && bool(record.Closed):
		// Already out of service, which is what was asked for.
		return ErrWalletClosed
	case closing:
		return ErrWalletHoldsMoney
	}
	// A reopen matched nothing and the wallet is not closed, so it was already
	// in service.
	return ErrNotFound
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
