package wallet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
	"github.com/arandu-io/hesape/database/query"
	"github.com/arandu-io/hesape/pagination"
)

// The keys a data table of agreements speaks. They are the public names the
// address carries, never columns: CreditListingSpec says which column each one
// is, and ListCredits reads only these.
const (
	// CreditStatusFacet is the facet that narrows the table to some statuses,
	// with the number of agreements beside each.
	CreditStatusFacet = "status"
	// CreditSortOpened orders by the day the agreement was opened, and is the
	// default, newest first.
	CreditSortOpened = "opened"
	// CreditSortTotal orders by what the installments add up to.
	CreditSortTotal = "total"
	// CreditSortPrincipal orders by what was financed.
	CreditSortPrincipal = "principal"
	// CreditSortInstallments orders by how many installments there are.
	CreditSortInstallments = "installments"
	// CreditSortStatus orders by the status.
	CreditSortStatus = "status"
	// CreditSortReference orders by the creditor's reference.
	CreditSortReference = "reference"
)

// creditListingSorts is every order an agreement table is read in, by the key
// the address names it with, and the column each key is. The list is closed on
// purpose: a column a request could name is a column the statement sorts by
// without an index behind it.
var creditListingSorts = map[string]string{
	CreditSortOpened:       "created_at",
	CreditSortTotal:        "total",
	CreditSortPrincipal:    "principal",
	CreditSortInstallments: "installments",
	CreditSortStatus:       "status",
	CreditSortReference:    "reference",
}

// creditListingSearch are the columns the search text is looked for in: the
// creditor's reference and the terms the holder accepted, which are stored as
// text and carry the facts the creditor recorded about the sale -- what was
// sold, and to whom -- under the names the creditor gave them.
var creditListingSearch = []string{"reference", "terms"}

// creditStatuses is every status an agreement can have, in the order a table
// offers them.
func creditStatuses() []CreditStatus {
	return []CreditStatus{CreditActive, CreditDelinquent, CreditSettled, CreditCancelled}
}

// CreditListingSpec declares the table of agreements for a data table: the
// search, the orders, the status facet and the default order, in the vocabulary
// of the constants above. An application builds its pagination.Listing from it,
// adds what is its own -- the page sizes, the hideable columns, the facets it
// narrows itself -- and reads the address with that listing:
//
//	spec := wallet.CreditListingSpec()
//	spec.PageSizes, spec.DefaultPageSize = []int{10, 25, 50}, 25
//	listing, err := pagination.NewListing(spec)
//	...
//	page, err := service.ListCredits(ctx, actor, listing.Parse(r.URL.Query()), "/sales/credit", wallet.CreditListFilter{})
//
// The columns in it are documentation of what ListCredits reads: ListCredits
// never takes a column from the state it is given, only the keys.
func CreditListingSpec() pagination.ListingSpec {
	sorts := make(map[string]string, len(creditListingSorts))
	for key, column := range creditListingSorts {
		sorts[key] = column
	}
	statuses := creditStatuses()
	values := make([]string, 0, len(statuses))
	for _, status := range statuses {
		values = append(values, string(status))
	}
	return pagination.ListingSpec{
		Search:      append([]string(nil), creditListingSearch...),
		Sorts:       sorts,
		DefaultSort: CreditSortOpened,
		DefaultDir:  pagination.SortDesc,
		TieBreak:    "id",
		Facets:      []pagination.FacetSpec{{Key: CreditStatusFacet, Column: "status", Values: values}},
	}
}

// CreditListFilter is what the application narrows an agreement table to before
// the reader of the table does: the same narrowing Credits offers, and the days
// the agreements were opened in. Everything left empty keeps everything.
type CreditListFilter struct {
	// HolderID keeps one holder's agreements. A subject who is not an operator
	// reads only their own, whatever this says.
	HolderID string
	// Reference keeps the agreements of one creditor reference, exactly or, with
	// ReferencePrefix, as the start of it.
	Reference       string
	ReferencePrefix bool
	// From, inclusive, and Until, exclusive, keep the agreements opened in a
	// period. A zero bound is no bound.
	From  time.Time
	Until time.Time
}

// Validate reports the errors per field.
func (f CreditListFilter) Validate() validation.Errors {
	e := validation.Errors{}
	validation.MaxLen(e, "holder_id", f.HolderID, maxIdentifierLen)
	validation.MaxLen(e, "reference", f.Reference, maxNameLen)
	if f.ReferencePrefix && strings.TrimSpace(f.Reference) == "" {
		e.Add("reference", "a prefix to match is not empty")
	}
	if !f.From.IsZero() && !f.Until.IsZero() && !f.Until.After(f.From) {
		e.Add("until", "the period ends after it starts")
	}
	return e
}

var _ validation.Validatable = CreditListFilter{}

// CreditListing is a page of agreements and the result a data table is drawn
// from.
type CreditListing struct {
	// Rows are the page, in the order the table asked for.
	Rows []*CreditAgreement
	// Result is the state that was read -- the state ListCredits was given, on
	// the last page when it asked for one past it -- the page with the number of
	// agreements the filter keeps, and, under CreditStatusFacet, how many
	// agreements each status holds with the search and the filter kept.
	Result *pagination.ListingResult
}

// ListCredits reads the page of agreements a data table asks for, narrowed,
// searched, ordered and counted in the database: the numbered page with its
// total, the search, the status facet with its counts, and the order, where
// Credits reads a cursor.
//
// state is read by a pagination.Listing built from CreditListingSpec, with the
// application's own page sizes and facets added if it likes. Only what the
// wallet declares is read from it -- the search text, the status facet, the
// sort key and direction, the page and its size -- through the wallet's own
// columns; a key it does not know (another sort, a facet the application
// narrows itself) is not applied, and a sort it does not know is the default.
// The result carries the state as it was given, so the links of the table keep
// every parameter the application put in the address.
//
// It asks the policy what Credits asks, WalletCreditView, first about the kind
// of thing and then about the holder: a holder reads their own agreements and
// an operator any holder's, and the tenant is the Grant's. A page past
// MaxPageSize is refused.
func (s *WalletService) ListCredits(ctx context.Context, actor security.Subject, state pagination.ListingState, path string, in CreditListFilter) (CreditListing, error) {
	if state.Listing() == nil {
		return CreditListing{}, query.ErrNoListing
	}
	errs := in.Validate()
	if state.PerPage() > MaxPageSize {
		errs.Add("per_page", fmt.Sprintf("a page holds between 1 and %d agreements", MaxPageSize))
	}
	if errs.Any() {
		return CreditListing{}, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditView, Wallet{})
	if err != nil {
		return CreditListing{}, err
	}
	holder := strings.TrimSpace(in.HolderID)
	if !IsOperator(actor) {
		// Not a filter the caller chose: whoever is not an operator reads their
		// own agreements, whatever they asked for.
		holder = actor.ID
	}
	if holder != "" {
		if _, err := security.Authorize(ctx, s.policy, actor, WalletCreditView, Wallet{HolderID: holder}); err != nil {
			return CreditListing{}, err
		}
	}

	read, err := walletCreditState(state)
	if err != nil {
		return CreditListing{}, err
	}

	q := CreditAgreements(s.db)
	if holder != "" {
		q = q.Where("holder_id", "=", holder)
	}
	q = s.whereCreditReference(q, in.Reference, in.ReferencePrefix)
	if !in.From.IsZero() {
		q = q.Where("created_at", ">=", in.From.UTC())
	}
	if !in.Until.IsZero() {
		q = q.Where("created_at", "<", in.Until.UTC())
	}

	rows, found, err := q.Base().Listing(ctx, g, read, path)
	if err != nil {
		return CreditListing{}, err
	}

	final := state.Clamp(found.Total())
	page := pagination.NewLengthAwarePage(len(rows), found.Total(), final.PerPage(), final.Page(), final.PageOptions(path))
	counts := map[string][]pagination.FacetCount{CreditStatusFacet: found.FacetCounts(CreditStatusFacet)}
	return CreditListing{
		Rows:   []*CreditAgreement(CreditAgreementCollectionOf(rows)),
		Result: pagination.NewListingResult(final, page, counts),
	}, nil
}

// walletCreditState is the state an application's listing holds, said again in
// the wallet's own listing: the same search, status facet, order, page size and
// page, and the wallet's columns. The wallet reads its columns from its own
// declaration and never from the application's, so a spec that names another
// column for a key cannot move a statement.
func walletCreditState(state pagination.ListingState) (pagination.ListingState, error) {
	asked := state.Listing()
	spec := CreditListingSpec()
	spec.PageSizes, spec.DefaultPageSize = asked.PageSizes(), asked.DefaultPageSize()
	own, err := pagination.NewListing(spec)
	if err != nil {
		return pagination.ListingState{}, fmt.Errorf("wallet: the listing of credit agreements cannot be read with the page sizes %v: %w", spec.PageSizes, err)
	}
	read := own.Parse(nil).
		WithSearch(state.Search()).
		WithFacet(CreditStatusFacet, state.Facet(CreditStatusFacet)...).
		WithSort(state.SortKey(), state.Direction()).
		WithPageSize(state.PerPage()).
		WithPage(state.Page())
	return read, nil
}
