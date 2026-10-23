package wallet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
	"github.com/arandu-io/hesape/database/model"

	"github.com/hyz-is/arandu-wallet/internal/everytenant"
)

// MaxListingValues is how many values one filter of a listing may name: wallets,
// tenants, kinds or statuses. A filter longer than that is a filter somebody
// built from a table rather than chose, and its statement grows with it.
const MaxListingValues = 100

// maxListingPage is the furthest numbered page a listing reads. Past it the
// offset is a scan nobody waits for; a cursor costs the same on every page.
const maxListingPage = 1000000

// OperationStatus is where an operation stands, as a listing reads it off the
// ledger. It is derived and never stored: a confirmation, a void and a reversal
// are operations of their own, appended beside the one they settle, and the
// status is what those rows say about it.
type OperationStatus string

// The statuses a listing answers and filters by. A row is in exactly one.
const (
	// OperationPending was recorded without counting, and nothing has settled
	// it yet.
	OperationPending OperationStatus = "pending"
	// OperationConfirmed was recorded without counting, and a confirmation
	// made it count. The money it proposed moved under the confirmation.
	OperationConfirmed OperationStatus = "confirmed"
	// OperationVoided was recorded without counting and withdrawn, so it never
	// will; a void's own rows read the same.
	OperationVoided OperationStatus = "voided"
	// OperationSettled counted when it was written, and nothing has been given
	// back of it.
	OperationSettled OperationStatus = "settled"
	// OperationPartiallyReversed counted, and partial reversals gave back
	// some, not all, of what this row moved.
	OperationPartiallyReversed OperationStatus = "partially_reversed"
	// OperationReversed counted, and was undone: whole, by a reversal, or
	// in parts that add up to what this row moved.
	OperationReversed OperationStatus = "reversed"
)

// operationStatuses is every status, in the order a screen offers them.
var operationStatuses = []OperationStatus{
	OperationPending, OperationConfirmed, OperationVoided,
	OperationSettled, OperationPartiallyReversed, OperationReversed,
}

// operationKinds is every kind an operation can be.
var operationKinds = []OperationKind{
	OperationDeposit, OperationWithdraw, OperationTransfer, OperationExchange,
	OperationReversal, OperationConfirmation, OperationPurchase, OperationRefund,
	OperationAdjustment, OperationVoid, OperationPartialReversal,
}

// OperationSort is the order a listing is read in. It is one of four, and the
// four are the whole list: a column a caller could name is a column the
// statement sorts by without an index behind it.
type OperationSort string

const (
	// OperationsNewest reads the most recent movement first. It is the zero
	// value.
	OperationsNewest OperationSort = ""
	// OperationsOldest reads the earliest first.
	OperationsOldest OperationSort = "oldest"
	// OperationsLargest reads the largest amount first.
	OperationsLargest OperationSort = "largest"
	// OperationsSmallest reads the smallest amount first.
	OperationsSmallest OperationSort = "smallest"
)

// OperationSortFor is the order a data table asks for by a column and a
// direction -- "created_at" or "amount", and "asc" or "desc" -- and false for
// anything else, so a column the request named and this package does not sort
// by is refused rather than guessed at.
func OperationSortFor(column, direction string) (OperationSort, bool) {
	desc := false
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "", "desc":
		desc = true
	case "asc":
	default:
		return OperationsNewest, false
	}
	switch strings.ToLower(strings.TrimSpace(column)) {
	case "", "created_at", "date":
		if desc {
			return OperationsNewest, true
		}
		return OperationsOldest, true
	case "amount":
		if desc {
			return OperationsLargest, true
		}
		return OperationsSmallest, true
	}
	return OperationsNewest, false
}

// keyset is the column a sort reads and whether it reads it descending.
func (o OperationSort) keyset() (column string, desc bool, ok bool) {
	switch o {
	case OperationsNewest:
		return "created_at", true, true
	case OperationsOldest:
		return "created_at", false, true
	case OperationsLargest:
		return "amount", true, true
	case OperationsSmallest:
		return "amount", false, true
	}
	return "", false, false
}

// OperationFilter narrows a listing of operations and pages it: what a
// server-side data table asks for.
//
// A row of the listing is an operation as it moved one wallet -- an entry of
// the ledger and the operation it was written under -- so a deposit is one
// row, and a transfer between two wallets the listing reads is two, one on
// each side, as a statement shows them. Direction keeps one side.
//
// Every filter left empty keeps everything. The page is a keyset by default
// (Cursor), which costs the same on the thousandth page as on the first; Page
// asks for a numbered page instead, read by offset, for a table that draws
// page numbers.
type OperationFilter struct {
	// WalletIDs keeps the movements of these wallets. Each one is asked of the
	// policy, so a holder names only their own.
	WalletIDs []string
	// HolderID keeps the movements of this holder's wallets. A subject who is
	// not an operator reads only their own, whatever this says.
	HolderID string
	// Kinds keeps operations of these kinds.
	Kinds []OperationKind
	// Direction keeps one side: EntryDeposit, money in, or EntryWithdraw,
	// money out. Empty keeps both.
	Direction EntryKind
	// Statuses keeps rows in these statuses.
	Statuses []OperationStatus
	// From, inclusive, and Until, exclusive, keep a period: by when each
	// movement was recorded, or with Basis DateAccrued by the date its
	// operation said the money is about, and when it was recorded where it
	// said none. A zero bound is no bound.
	From  time.Time
	Until time.Time
	Basis DateBasis
	// Meta keeps operations whose metadata holds every one of these names with
	// exactly this value. It reads the operation's metadata, which is where a
	// movement with one leg carries its facts.
	Meta Meta
	// Search keeps rows whose operation or entry identifier or idempotency key
	// is this text, or whose reason or metadata contains it, without regard to
	// the case of ASCII letters. It reads the metadata as it is stored, names
	// included, so "reason" finds every operation with a fact of that name.
	Search string
	// Sort is the order; the zero value is newest first.
	Sort OperationSort
	// Cursor is the NextCursor or PrevCursor of a page read before, and empty
	// for the first page. It is opaque.
	Cursor string
	// Page asks for a numbered page, counting from one, read by offset; zero
	// pages by Cursor. The two are not asked for together.
	Page int
	// Limit is how many rows a page holds: zero is fifty, and more than
	// MaxPageSize is refused.
	Limit int
	// Count asks for Total, the number of rows the filter keeps. It is one
	// more statement, and it grows with the ledger, so it is asked for rather
	// than always paid.
	Count bool
}

// metaFilterName is what a name of OperationFilter.Meta may be: what a JSON
// path names without quoting anything inside it.
var metaFilterName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// Validate reports the errors per field.
func (f OperationFilter) Validate() validation.Errors {
	e := validation.Errors{}
	if len(f.WalletIDs) > MaxListingValues {
		e.Add("wallet_ids", fmt.Sprintf("name at most %d wallets", MaxListingValues))
	}
	for _, id := range f.WalletIDs {
		if strings.TrimSpace(id) == "" || len(id) > maxIdentifierLen {
			e.Add("wallet_ids", "name each wallet by its identifier")
			break
		}
	}
	validation.MaxLen(e, "holder_id", f.HolderID, maxIdentifierLen)
	if len(f.Kinds) > MaxListingValues {
		e.Add("kinds", fmt.Sprintf("name at most %d kinds", MaxListingValues))
	}
	for _, kind := range f.Kinds {
		if !knownKind(kind) {
			e.Add("kinds", fmt.Sprintf("%q is not a kind of operation", kind))
			break
		}
	}
	if f.Direction != "" && f.Direction != EntryDeposit && f.Direction != EntryWithdraw {
		e.Add("direction", "keep money in, money out, or both")
	}
	if len(f.Statuses) > MaxListingValues {
		e.Add("statuses", fmt.Sprintf("name at most %d statuses", MaxListingValues))
	}
	for _, status := range f.Statuses {
		if !knownStatus(status) {
			e.Add("statuses", fmt.Sprintf("%q is not a status of an operation", status))
			break
		}
	}
	for field, messages := range (HistoryRequest{From: f.From, Until: f.Until, Basis: f.Basis}).period() {
		for _, message := range messages {
			e.Add(field, message)
		}
	}
	if len(f.Meta) > MaxMetaKeys {
		e.Add("meta", fmt.Sprintf("match at most %d names", MaxMetaKeys))
	}
	for name, value := range f.Meta {
		if !metaFilterName.MatchString(name) {
			e.Add("meta", fmt.Sprintf("%q cannot be matched: a name is letters, digits, _ . : and -, up to 64", name))
			break
		}
		if len(value) > MaxMetaBytes {
			e.Add("meta", fmt.Sprintf("a value to match is at most %d bytes", MaxMetaBytes))
			break
		}
	}
	validation.MaxLen(e, "search", f.Search, maxNameLen)
	if _, _, ok := f.Sort.keyset(); !ok {
		e.Add("sort", "sort by the date or the amount, either way")
	}
	if f.Cursor != "" {
		if _, _, err := decodeCursor(f.Cursor); err != nil {
			e.Add("cursor", "the cursor is not one a page answered with")
		}
	}
	if f.Page < 0 || f.Page > maxListingPage {
		e.Add("page", fmt.Sprintf("a page is numbered from 1 to %d, and 0 pages by cursor", maxListingPage))
	}
	if f.Page > 0 && f.Cursor != "" {
		e.Add("page", "ask for a numbered page or a cursor, not both")
	}
	if f.Limit < 0 || f.Limit > MaxPageSize {
		e.Add("limit", fmt.Sprintf("a page holds between 1 and %d rows, and 0 is %d", MaxPageSize, defaultLimit))
	}
	return e
}

var _ validation.Validatable = OperationFilter{}

// PlatformOperationFilter is what the platform's listing reads: the filter of
// one tenant's, and the tenants to keep.
type PlatformOperationFilter struct {
	OperationFilter
	// Tenants keeps the movements of these tenants. Empty keeps every
	// tenant's.
	Tenants []string
}

// Validate reports the errors per field.
func (f PlatformOperationFilter) Validate() validation.Errors {
	e := f.OperationFilter.Validate()
	if len(f.Tenants) > MaxListingValues {
		e.Add("tenants", fmt.Sprintf("name at most %d tenants", MaxListingValues))
	}
	for _, tenant := range f.Tenants {
		if !security.ValidTenant(tenant) {
			e.Add("tenants", fmt.Sprintf("%q cannot be a tenant", tenant))
			break
		}
	}
	return e
}

var _ validation.Validatable = PlatformOperationFilter{}

func knownKind(kind OperationKind) bool {
	for _, known := range operationKinds {
		if kind == known {
			return true
		}
	}
	return false
}

func knownStatus(status OperationStatus) bool {
	for _, known := range operationStatuses {
		if status == known {
			return true
		}
	}
	return false
}

// OperationRow is one row of a listing: an operation as it moved one wallet.
//
// It is a snapshot and holds no connection, so a row read across tenants cannot
// be written through. Everything a data table draws is on it -- the wallet's
// currency and scale with the amount, the operation's kind, metadata and
// status with the movement -- so a page needs no second question.
type OperationRow struct {
	// TenantID is the customer the row belongs to: the platform's listing reads
	// it per row, and a tenant's listing answers its own.
	TenantID string
	// EntryID identifies the row: the movement in the ledger.
	EntryID string
	// OperationID is the operation the movement was written under.
	OperationID string
	// Kind is what the operation was asked to do.
	Kind OperationKind
	// Status is where it stands.
	Status OperationStatus
	// WalletID, HolderID, WalletSlug and WalletName name the wallet that moved.
	WalletID   string
	HolderID   string
	WalletSlug string
	WalletName string
	// Currency and DecimalPlaces are how the wallet counts, which is what
	// Amount and BalanceAfter mean.
	Currency      Currency
	DecimalPlaces int
	// Direction is EntryDeposit for money in and EntryWithdraw for money out.
	Direction EntryKind
	// Amount is how much moved, or was proposed to, in minor units; always
	// positive.
	Amount Amount
	// BalanceAfter is what the wallet held once the movement was applied.
	BalanceAfter Amount
	// Settled says the movement counted when it was written.
	Settled bool
	// IdempotencyKey is what the caller named the request.
	IdempotencyKey string
	// Reason is what was said about a reversal or a void.
	Reason string
	// RefersTo is the operation this one reverses, voids or confirms, and empty
	// where it settles nothing.
	RefersTo string
	// ResolvedBy is the confirmation or the void that settled a pending row.
	ResolvedBy string
	// ReversedAmount is how much of the operation partial reversals gave back.
	ReversedAmount Amount
	// Meta is the operation's metadata, and EntryMeta what this leg carried of
	// its own.
	Meta      Meta
	EntryMeta Meta
	// AccruedAt is the date the operation said the money is about, if any.
	AccruedAt *time.Time
	// CreatedAt is when the movement was recorded, in UTC.
	CreatedAt time.Time
}

// Money is the amount at the wallet's scale and in its currency.
func (r OperationRow) Money() Money {
	return Money{Amount: r.Amount, Currency: r.Currency, DecimalPlaces: r.DecimalPlaces}
}

// Signed is the movement as it adds to the balance: positive in, negative out,
// and zero where it did not count.
func (r OperationRow) Signed() Amount {
	if !r.Settled {
		return 0
	}
	if r.Direction == EntryWithdraw {
		return -r.Amount
	}
	return r.Amount
}

// OperationPage is one page of a listing, in the order asked for.
type OperationPage struct {
	// Rows are the page.
	Rows []OperationRow
	// NextCursor reads the page after this one, and is empty on the last.
	NextCursor string
	// PrevCursor reads the page before this one, and is empty on the first.
	PrevCursor string
	// Page is the number of a numbered page, and zero for a page read by
	// cursor.
	Page int
	// Limit is how many rows a page holds.
	Limit int
	// Total is how many rows the filter keeps, when Counted.
	Total int64
	// Counted says Total was counted, which the filter asked for.
	Counted bool
}

// HasMore reports that a page follows this one.
func (p OperationPage) HasMore() bool { return p.NextCursor != "" }

// Pages is how many pages of Limit rows the filter keeps, when Counted, and
// zero otherwise.
func (p OperationPage) Pages() int {
	if !p.Counted || p.Limit <= 0 {
		return 0
	}
	return int((p.Total + int64(p.Limit) - 1) / int64(p.Limit))
}

// ListOperations pages through the operations of the subject's tenant: what a
// statement screen, a payments table or an export reads, narrowed and ordered
// in the database rather than after a page was fetched.
//
// It asks the policy what History asks -- WalletHistory, first about the kind of
// thing, then about every wallet the filter names -- so a holder reads their
// own wallets and an operator every wallet of the tenant. A subject who is not
// an operator is narrowed to their own wallets whatever the filter says, and a
// wallet named that is not in the tenant is ErrNotFound. The tenant is the
// Grant's.
//
// The keyset is on the sort column, the wallet and the entry's position in its
// ledger -- an order no two rows share -- so no row is repeated or skipped while
// money moves between two reads. A movement written meanwhile lands where its
// values put it: newest first, before the first page.
func (s *WalletService) ListOperations(ctx context.Context, actor security.Subject, in OperationFilter) (OperationPage, error) {
	if errs := in.Validate(); errs.Any() {
		return OperationPage{}, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletHistory, Wallet{})
	if err != nil {
		return OperationPage{}, err
	}

	holder := strings.TrimSpace(in.HolderID)
	if !IsOperator(actor) {
		// Not a filter the caller chose: whoever is not an operator reads
		// their own wallets, whatever they asked for.
		holder = actor.ID
	}
	if holder != "" {
		if _, err := security.Authorize(ctx, s.policy, actor, WalletHistory, Wallet{HolderID: holder}); err != nil {
			return OperationPage{}, err
		}
	}
	named := distinct(in.WalletIDs)
	if len(named) > 0 {
		found, err := Wallets(s.db).WhereIn("id", named).Get(ctx, g)
		if err != nil {
			return OperationPage{}, err
		}
		if len(found) != len(named) {
			return OperationPage{}, ErrNotFound
		}
		for _, held := range found {
			if _, err := security.Authorize(ctx, s.policy, actor, WalletHistory, *held); err != nil {
				return OperationPage{}, err
			}
		}
	}

	source := listingSource{
		entries:    func() *model.Builder { return Entries(s.db).Base() },
		operations: func() *model.Builder { return Operations(s.db).Base() },
		wallets:    func() *model.Builder { return Wallets(s.db).Base() },
		entry: func(e model.Entity) (Entry, bool) {
			row, ok := e.(*Entry)
			if !ok || row == nil {
				return Entry{}, false
			}
			return *row, true
		},
		operation: func(e model.Entity) (Operation, bool) {
			row, ok := e.(*Operation)
			if !ok || row == nil {
				return Operation{}, false
			}
			return *row, true
		},
		wallet: func(e model.Entity) (Wallet, bool) {
			row, ok := e.(*Wallet)
			if !ok || row == nil {
				return Wallet{}, false
			}
			return *row, true
		},
	}
	return s.listOperations(ctx, g, source, in, listingScope{holder: holder, wallets: named})
}

// ListAllOperations pages through the operations of every tenant, or of the
// tenants the filter names: the platform's console, which reads the money of
// every customer where a tenant's listing reads one.
//
// It asks the policy for WalletOperationsAll, which only a platform operator
// holds (IsPlatformOperator): PlatformRole, or that action granted through
// arandu-permission. A tenant's operator is refused -- the operator's reach
// ends at its tenant -- and so is a subject inside somebody else's account.
// Nothing else is asked: the wallets it names may be any tenant's, and each row
// says whose it is. It reads, and the rows it answers hold no connection.
func (s *WalletService) ListAllOperations(ctx context.Context, actor security.Subject, in PlatformOperationFilter) (OperationPage, error) {
	if errs := in.Validate(); errs.Any() {
		return OperationPage{}, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletOperationsAll, Wallet{})
	if err != nil {
		return OperationPage{}, err
	}

	source := listingSource{
		entries:    func() *model.Builder { return everytenant.Entries(s.db).Base() },
		operations: func() *model.Builder { return everytenant.Operations(s.db).Base() },
		wallets:    func() *model.Builder { return everytenant.Wallets(s.db).Base() },
		entry: func(e model.Entity) (Entry, bool) {
			row, ok := e.(*everytenant.Entry)
			if !ok || row == nil {
				return Entry{}, false
			}
			var meta Meta
			if err := meta.Scan(row.Meta); err != nil {
				return Entry{}, false
			}
			return Entry{
				ID: row.ID, TenantID: row.TenantID, OperationID: row.OperationID, WalletID: row.WalletID,
				Kind: EntryKind(row.Kind), Sequence: int64(row.Sequence), Position: int(row.Position),
				Amount: Amount(row.Amount), BalanceAfter: Amount(row.BalanceAfter), Settled: row.Settled != 0,
				Meta: meta, CreatedAt: row.CreatedAt,
			}, true
		},
		operation: func(e model.Entity) (Operation, bool) {
			row, ok := e.(*everytenant.Operation)
			if !ok || row == nil {
				return Operation{}, false
			}
			var meta Meta
			if err := meta.Scan(row.Meta); err != nil {
				return Operation{}, false
			}
			return Operation{
				ID: row.ID, TenantID: row.TenantID, IdempotencyKey: row.IdempotencyKey, Kind: OperationKind(row.Kind),
				SettlesID: row.SettlesID, Reason: row.Reason, Meta: meta, RefersID: row.RefersID,
				ReversedAmount: Amount(row.ReversedAmount), ResolvedBy: row.ResolvedBy, AccruedAt: row.AccruedAt,
				CreatedAt: row.CreatedAt,
			}, true
		},
		wallet: func(e model.Entity) (Wallet, bool) {
			row, ok := e.(*everytenant.Wallet)
			if !ok || row == nil {
				return Wallet{}, false
			}
			return Wallet{
				ID: row.ID, TenantID: row.TenantID, HolderID: row.HolderID, Slug: row.Slug, Name: row.Name,
				Currency: Currency(row.Currency), DecimalPlaces: int(row.DecimalPlaces),
			}, true
		},
	}
	return s.listOperations(ctx, g, source, in.OperationFilter, listingScope{
		holder:  strings.TrimSpace(in.HolderID),
		wallets: distinct(in.WalletIDs),
		tenants: distinct(in.Tenants),
	})
}

// listingSource is where a listing reads its three tables: through the
// tenant-scoped Models for a tenant's listing, and through the Global ones of
// everytenant for the platform's. Everything else about a listing is the same
// code, so the two cannot answer a filter differently.
type listingSource struct {
	entries    func() *model.Builder
	operations func() *model.Builder
	wallets    func() *model.Builder
	entry      func(model.Entity) (Entry, bool)
	operation  func(model.Entity) (Operation, bool)
	wallet     func(model.Entity) (Wallet, bool)
}

// listingScope is what the service decided on top of the filter: the holder a
// subject is narrowed to, the wallets it was allowed, and the tenants the
// platform asked for.
type listingScope struct {
	holder  string
	wallets []any
	tenants []any
}

// listOperations reads one page. The caller has authorized it and decided its
// scope; the Grant it holds is the one every statement here runs under.
func (s *WalletService) listOperations(ctx context.Context, g security.Grant, src listingSource, in OperationFilter, scope listingScope) (OperationPage, error) {
	limit := boundedLimit(in.Limit)
	out := OperationPage{Page: in.Page, Limit: limit}
	column, desc, _ := in.Sort.keyset()

	filtered := src.entries()
	s.narrowListing(filtered, in, scope)

	if in.Count {
		total, err := filtered.Clone().Count(ctx, g)
		if err != nil {
			return OperationPage{}, err
		}
		out.Total, out.Counted = total, true
	}

	page := filtered.Clone()
	backward := false
	if in.Cursor != "" {
		before, anchorID, _ := decodeCursor(in.Cursor)
		found, err := src.entries().WhereKey(anchorID).First(ctx, g)
		if err != nil {
			return OperationPage{}, err
		}
		anchor, ok := Entry{}, false
		if found != nil {
			anchor, ok = src.entry(found)
		}
		if !ok {
			// The row the cursor named is not one this reader sees. Nothing is
			// ever removed from the ledger, so the cursor was not one a page
			// of this listing answered with, and there is nothing after it.
			return out, nil
		}
		backward = before
		// Walking forward in a descending order is walking towards smaller
		// values; walking backward turns both around.
		op := ">"
		if desc != backward {
			op = "<"
		}
		var value any = anchor.CreatedAt
		if column == "amount" {
			value = int64(anchor.Amount)
		}
		page.Where(func(after *model.Builder) {
			after.Where(column, op, value).
				OrWhere(func(tie *model.Builder) {
					tie.Where(column, "=", value).Where(func(next *model.Builder) {
						next.Where("wallet_id", op, anchor.WalletID).
							OrWhere(func(same *model.Builder) {
								same.Where("wallet_id", "=", anchor.WalletID).Where("sequence", op, anchor.Sequence)
							})
					})
				})
		})
	}

	direction := "asc"
	if desc != backward {
		direction = "desc"
	}
	// The order is total: the sort column, then the wallet and the entry's
	// position in it, which no two entries share. Within one wallet the
	// position is the order the ledger was written in, so rows recorded in
	// the same instant still read in that order.
	page.OrderBy(column, direction).OrderBy("wallet_id", direction).OrderBy("sequence", direction)
	if in.Page > 0 {
		page.Offset((in.Page - 1) * limit)
	}
	found, err := page.Limit(limit+1).Get(ctx, g)
	if err != nil {
		return OperationPage{}, err
	}

	entries := make([]Entry, 0, min(len(found), limit))
	for _, row := range found {
		if entry, ok := src.entry(row); ok {
			entries = append(entries, entry)
		}
	}
	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}
	if backward {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	if len(entries) > 0 {
		first, last := entries[0].ID, entries[len(entries)-1].ID
		switch {
		case backward:
			// Read towards the start: the page after this one is the one the
			// cursor came from, and one before it exists when more was read.
			out.NextCursor = encodeCursor(false, last)
			if more {
				out.PrevCursor = encodeCursor(true, first)
			}
		default:
			if more {
				out.NextCursor = encodeCursor(false, last)
			}
			if in.Cursor != "" || in.Page > 1 {
				out.PrevCursor = encodeCursor(true, first)
			}
		}
	}

	out.Rows, err = s.describeRows(ctx, g, src, entries)
	if err != nil {
		return OperationPage{}, err
	}
	return out, nil
}

// describeRows turns a page of entries into rows, reading what is behind them
// in three statements for the whole page: the operations, the wallets, and the
// confirmations, voids and reversals naming those operations.
func (s *WalletService) describeRows(ctx context.Context, g security.Grant, src listingSource, entries []Entry) ([]OperationRow, error) {
	if len(entries) == 0 {
		return []OperationRow{}, nil
	}
	operationIDs := make([]any, 0, len(entries))
	walletIDs := make([]any, 0, len(entries))
	seenOperation := make(map[string]bool, len(entries))
	seenWallet := make(map[string]bool, len(entries))
	for _, entry := range entries {
		collect(&operationIDs, seenOperation, entry.OperationID, entry.OperationID)
		collect(&walletIDs, seenWallet, entry.WalletID, entry.WalletID)
	}

	operations := make(map[string]Operation, len(operationIDs))
	rows, err := src.operations().WhereIn("id", operationIDs).Get(ctx, g)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if op, ok := src.operation(row); ok {
			operations[op.TenantID+"/"+op.ID] = op
		}
	}

	wallets := make(map[string]Wallet, len(walletIDs))
	rows, err = src.wallets().WhereIn("id", walletIDs).Get(ctx, g)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if held, ok := src.wallet(row); ok {
			wallets[held.TenantID+"/"+held.ID] = held
		}
	}

	settlers := make(map[string]map[OperationKind]string, len(operationIDs))
	rows, err = src.operations().
		WhereIn("reverses_id", operationIDs).
		WhereIn("kind", []any{string(OperationConfirmation), string(OperationVoid), string(OperationReversal)}).
		Get(ctx, g)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		settler, ok := src.operation(row)
		if !ok || settler.SettlesID == settler.ID {
			continue
		}
		key := settler.TenantID + "/" + settler.SettlesID
		if settlers[key] == nil {
			settlers[key] = map[OperationKind]string{}
		}
		settlers[key][settler.Kind] = settler.ID
	}

	out := make([]OperationRow, 0, len(entries))
	for _, entry := range entries {
		op := operations[entry.TenantID+"/"+entry.OperationID]
		held := wallets[entry.TenantID+"/"+entry.WalletID]
		by := settlers[entry.TenantID+"/"+entry.OperationID]
		row := OperationRow{
			TenantID: entry.TenantID, EntryID: entry.ID, OperationID: entry.OperationID,
			Kind:     op.Kind,
			Status:   operationStatus(bool(entry.Settled), op, entry.Amount, by),
			WalletID: entry.WalletID, HolderID: held.HolderID, WalletSlug: held.Slug, WalletName: held.Name,
			Currency: held.Currency, DecimalPlaces: held.DecimalPlaces,
			Direction: entry.Kind, Amount: entry.Amount, BalanceAfter: entry.BalanceAfter,
			Settled:        bool(entry.Settled),
			IdempotencyKey: op.IdempotencyKey, Reason: op.Reason,
			ResolvedBy: op.ResolvedBy, ReversedAmount: op.ReversedAmount,
			Meta: op.Meta, EntryMeta: entry.Meta, AccruedAt: op.AccruedAt,
			CreatedAt: entry.CreatedAt.UTC(),
		}
		for _, refers := range []string{op.Reverses(), op.Voids(), op.Confirms()} {
			if refers != "" {
				row.RefersTo = refers
				break
			}
		}
		if row.ResolvedBy == "" && !row.Settled {
			// An operation confirmed before the column existed is named by
			// the confirmation instead.
			if id := by[OperationConfirmation]; id != "" {
				row.ResolvedBy = id
			} else if id := by[OperationVoid]; id != "" {
				row.ResolvedBy = id
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// operationStatus is where a row stands. It is the same decision statusClause
// writes as SQL, and the two are kept beside each other so that a row a status
// filter keeps is a row that answers that status.
func operationStatus(settled bool, op Operation, amount Amount, by map[OperationKind]string) OperationStatus {
	if !settled {
		switch {
		case by[OperationConfirmation] != "":
			return OperationConfirmed
		case op.Kind == OperationVoid || by[OperationVoid] != "":
			return OperationVoided
		}
		return OperationPending
	}
	if by[OperationReversal] != "" || (op.ReversedAmount > 0 && op.ReversedAmount >= amount) {
		return OperationReversed
	}
	if op.ReversedAmount > 0 {
		return OperationPartiallyReversed
	}
	return OperationSettled
}

// narrowListing writes the filter onto a query of the ledger.
//
// The filters on a movement's own columns are where clauses on the entries
// table. The ones on its operation and its wallet are correlated subqueries
// tied to the entry's own tenant -- `exists (select 1 from wallet_operations o
// where o.id = entry.operation_id and o.tenant_id = entry.tenant_id and ...)`
// -- rather than joins: the platform's listing reads the ledger of every tenant
// and has to read each entry's operation in that entry's tenant, which a join
// filtered by the Grant's tenant cannot say. A tenant's listing reads the same
// statement under its tenant filter, so the two answer one filter the same way.
func (s *WalletService) narrowListing(b *model.Builder, in OperationFilter, scope listingScope) {
	l := newListingSQL(s.db.Dialect())

	if len(scope.tenants) > 0 {
		b.WhereIn("tenant_id", scope.tenants)
	}
	if len(scope.wallets) > 0 {
		b.WhereIn("wallet_id", scope.wallets)
	}
	if in.Direction != "" {
		b.Where("kind", "=", string(in.Direction))
	}
	if in.Basis != DateAccrued {
		if !in.From.IsZero() {
			b.Where("created_at", ">=", in.From.UTC())
		}
		if !in.Until.IsZero() {
			b.Where("created_at", "<", in.Until.UTC())
		}
	}

	raw := b.GetQuery()
	if scope.holder != "" {
		raw.WhereRaw(l.walletExists(l.c("w", "holder_id")+" = ?"), scope.holder)
	}

	var conditions []string
	var args []any
	if len(in.Kinds) > 0 {
		kinds := make([]any, 0, len(in.Kinds))
		for _, kind := range in.Kinds {
			kinds = append(kinds, string(kind))
		}
		conditions = append(conditions, l.c("o", "kind")+" in ("+placeholders(len(kinds))+")")
		args = append(args, kinds...)
	}
	if in.Basis == DateAccrued && (!in.From.IsZero() || !in.Until.IsZero()) {
		bounded := func(column string) (string, []any) {
			var parts []string
			var bound []any
			if !in.From.IsZero() {
				parts = append(parts, l.c("o", column)+" >= ?")
				bound = append(bound, in.From.UTC())
			}
			if !in.Until.IsZero() {
				parts = append(parts, l.c("o", column)+" < ?")
				bound = append(bound, in.Until.UTC())
			}
			return strings.Join(parts, " and "), bound
		}
		accrued, accruedArgs := bounded("accrued_at")
		recorded, recordedArgs := bounded("created_at")
		conditions = append(conditions, "(("+l.c("o", "accrued_at")+" is not null and "+accrued+") or ("+
			l.c("o", "accrued_at")+" is null and "+recorded+"))")
		args = append(append(args, accruedArgs...), recordedArgs...)
	}
	if len(in.Meta) > 0 {
		condition, metaArgs := l.metaMatches(in.Meta)
		conditions = append(conditions, condition)
		args = append(args, metaArgs...)
	}
	if len(conditions) > 0 {
		raw.WhereRaw(l.operationExists(strings.Join(conditions, " and ")), args...)
	}

	if len(in.Statuses) > 0 {
		var groups []string
		var statusArgs []any
		for _, status := range distinctStatuses(in.Statuses) {
			group, groupArgs := l.statusClause(status)
			groups = append(groups, "("+group+")")
			statusArgs = append(statusArgs, groupArgs...)
		}
		raw.WhereRaw("("+strings.Join(groups, " or ")+")", statusArgs...)
	}

	if term := strings.TrimSpace(in.Search); term != "" {
		// The metadata is stored as JSON, which escapes some characters a
		// person types -- so the text looked for in it is the term as the
		// encoder writes it, without the quotes around it.
		encoded, _ := json.Marshal(term)
		needle := strings.ToLower(strings.Trim(string(encoded), `"`))
		lowered := strings.ToLower(term)
		raw.WhereRaw("("+l.c(entriesTable, "id")+" = ? or "+l.c(entriesTable, "operation_id")+" = ? or "+
			l.operationExists(l.c("o", "idempotency_key")+" = ? or "+
				l.contains(l.c("o", "reason"))+" or "+l.contains(l.c("o", "meta")))+")",
			term, term, term, lowered, needle)
	}
}

// listingSQL spells the raw fragments of a listing for one engine. Every
// identifier goes through the engine's own grammar, and every value is a
// binding.
type listingSQL struct {
	dialect data.Dialect
	q       quoter
}

func newListingSQL(dialect data.Dialect) listingSQL {
	return listingSQL{dialect: dialect, q: quoterFor(dialect)}
}

// c names a column of a table or an alias.
func (l listingSQL) c(table, column string) string { return l.q(table) + "." + l.q(column) }

// operationExists is "the entry's operation, in the entry's tenant, satisfies
// cond", with the operation aliased o.
func (l listingSQL) operationExists(cond string) string {
	return "exists (select 1 from " + l.q(operationsTable) + " as " + l.q("o") +
		" where " + l.c("o", "id") + " = " + l.c(entriesTable, "operation_id") +
		" and " + l.c("o", "tenant_id") + " = " + l.c(entriesTable, "tenant_id") +
		" and (" + cond + "))"
}

// walletExists is "the entry's wallet, in the entry's tenant, satisfies cond",
// with the wallet aliased w.
func (l listingSQL) walletExists(cond string) string {
	return "exists (select 1 from " + l.q(walletsTable) + " as " + l.q("w") +
		" where " + l.c("w", "id") + " = " + l.c(entriesTable, "wallet_id") +
		" and " + l.c("w", "tenant_id") + " = " + l.c(entriesTable, "tenant_id") +
		" and (" + cond + "))"
}

// settledBy is "an operation of one of these kinds settles o": the
// confirmation, the void or the reversal naming it. It reads the unique index
// on the tenant, the kind and the operation settled.
func (l listingSQL) settledBy(kinds ...OperationKind) (string, []any) {
	args := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		args = append(args, string(kind))
	}
	return "exists (select 1 from " + l.q(operationsTable) + " as " + l.q("s") +
		" where " + l.c("s", "tenant_id") + " = " + l.c("o", "tenant_id") +
		" and " + l.c("s", "reverses_id") + " = " + l.c("o", "id") +
		" and " + l.c("s", "id") + " <> " + l.c("o", "id") +
		" and " + l.c("s", "kind") + " in (" + placeholders(len(kinds)) + "))", args
}

// statusClause is the SQL of one status: operationStatus, written for the
// database.
func (l listingSQL) statusClause(status OperationStatus) (string, []any) {
	settled := l.c(entriesTable, "settled")
	reversedAmount := l.c("o", "reversed_amount")
	amount := l.c(entriesTable, "amount")
	confirmed, confirmedArgs := l.settledBy(OperationConfirmation)
	voided, voidedArgs := l.settledBy(OperationVoid)
	reversed, reversedArgs := l.settledBy(OperationReversal)

	join := func(parts ...[]any) []any {
		var out []any
		for _, part := range parts {
			out = append(out, part...)
		}
		return out
	}
	switch status {
	case OperationPending:
		return settled + " = 0 and " + l.operationExists(l.c("o", "kind")+" <> ? and not "+confirmed+" and not "+voided),
			join([]any{string(OperationVoid)}, confirmedArgs, voidedArgs)
	case OperationConfirmed:
		return settled + " = 0 and " + l.operationExists(confirmed), confirmedArgs
	case OperationVoided:
		return settled + " = 0 and " + l.operationExists("not "+confirmed+" and ("+l.c("o", "kind")+" = ? or "+voided+")"),
			join(confirmedArgs, []any{string(OperationVoid)}, voidedArgs)
	case OperationReversed:
		return settled + " <> 0 and " + l.operationExists(reversed+" or ("+reversedAmount+" > 0 and "+reversedAmount+" >= "+amount+")"),
			reversedArgs
	case OperationPartiallyReversed:
		return settled + " <> 0 and " + l.operationExists("not "+reversed+" and "+reversedAmount+" > 0 and "+reversedAmount+" < "+amount),
			reversedArgs
	default: // OperationSettled
		return settled + " <> 0 and " + l.operationExists("not "+reversed+" and "+reversedAmount+" <= 0"),
			reversedArgs
	}
}

// metaMatches is "o's metadata holds every one of these names with exactly
// this value".
//
// On PostgreSQL it is one containment test on the column read as jsonb, which
// the GIN index of 20261023_0025_add_wallet_listing_indexes answers. SQLite and
// MySQL read each name out of the text with their JSON functions, inside the
// rows the other filters and the tenant already narrowed. An empty column is no
// metadata and matches nothing.
func (l listingSQL) metaMatches(meta Meta) (string, []any) {
	column := "nullif(" + l.c("o", "meta") + ", '')"
	if l.dialect == data.DialectPostgres {
		encoded, _ := json.Marshal(map[string]string(meta))
		return column + "::jsonb @> cast(? as jsonb)", []any{string(encoded)}
	}
	var parts []string
	var args []any
	for _, name := range meta.Names() {
		path := `$."` + name + `"`
		if l.dialect == data.DialectMySQL {
			parts = append(parts, "json_unquote(json_extract("+column+", ?)) = ?")
		} else {
			parts = append(parts, "json_extract("+column+", ?) = ?")
		}
		args = append(args, path, meta[name])
	}
	return strings.Join(parts, " and "), args
}

// contains is "the column, lowered, contains the bound text": strpos on
// PostgreSQL, instr elsewhere.
func (l listingSQL) contains(column string) string {
	if l.dialect == data.DialectPostgres {
		return "strpos(lower(" + column + "), ?) > 0"
	}
	return "instr(lower(" + column + "), ?) > 0"
}

// placeholders is n bindings, separated by commas.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
}

// distinct is the non-empty values, trimmed, once each and in order.
func distinct(values []string) []any {
	out := make([]any, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func distinctStatuses(statuses []OperationStatus) []OperationStatus {
	out := make([]OperationStatus, 0, len(statuses))
	seen := make(map[OperationStatus]bool, len(statuses))
	for _, status := range statuses {
		if !seen[status] {
			seen[status] = true
			out = append(out, status)
		}
	}
	return out
}

// encodeCursor is the opaque cursor of a row: which way it reads and the
// entry it is anchored on.
func encodeCursor(before bool, entryID string) string {
	way := "n"
	if before {
		way = "p"
	}
	return base64.RawURLEncoding.EncodeToString([]byte(way + ":" + entryID))
}

// decodeCursor reads a cursor encodeCursor wrote.
func decodeCursor(cursor string) (before bool, entryID string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return false, "", err
	}
	way, id, ok := strings.Cut(string(raw), ":")
	if !ok || id == "" || len(id) > maxIdentifierLen || (way != "n" && way != "p") {
		return false, "", fmt.Errorf("wallet: %q is not a cursor of a listing", cursor)
	}
	return way == "p", id, nil
}
