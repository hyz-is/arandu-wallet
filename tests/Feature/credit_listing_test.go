package feature_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
	"github.com/arandu-io/hesape/database/query"
	"github.com/arandu-io/hesape/pagination"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The agreements a data table pages through: a numbered page with its total, the
// search, the status facet with its counts, the order, and the same reach
// Credits has.

// creditTable is the listing an application builds from CreditListingSpec, with
// the page sizes of its own.
func creditTable(t *testing.T, sizes ...int) *pagination.Listing {
	t.Helper()
	spec := wallet.CreditListingSpec()
	spec.PageSizes, spec.DefaultPageSize = sizes, sizes[0]
	listing, err := pagination.NewListing(spec)
	if err != nil {
		t.Fatal(err)
	}
	return listing
}

// openSale opens an agreement whose terms say who bought what, as a creditor's
// terms do, first due on the day given.
func openSale(t *testing.T, service *wallet.WalletService, walletID, key, principal string, count int, firstDue, customer, description string) wallet.CreditStatement {
	t.Helper()
	statement, err := service.OpenCredit(context.Background(), staff(), wallet.CreditOpenRequest{
		IdempotencyKey: key, WalletID: walletID, Reference: "order-" + key,
		Principal: principal, Installments: count, FirstDue: firstDue, TimeZone: "America/Sao_Paulo",
		LateFeeBP: 200, MoraRateBP: 100, DelinquentAfterDays: 30,
		Terms:      wallet.Meta{"customer_name": customer, "description": description},
		AcceptedAt: day(t, "2026-10-10"), AcceptedFrom: "203.0.113.7",
	})
	if err != nil {
		t.Fatalf("opening %s: %v", key, err)
	}
	return statement
}

// salesFixture is a tenant's agreements, one in each status, of two holders:
//
//	sale-a  active      Ana Souza   Sofa        100.00 in 2   buyer-1
//	sale-b  settled     Bruno Lima  Geladeira   300.00 in 3   buyer-1
//	sale-c  cancelled   Carla Dias  Sofa cama   200.00 in 4   buyer-2
//	sale-d  delinquent  Ana Paula   Mesa         50.00 in 1   buyer-2
type salesFixture struct {
	service *wallet.WalletService
	ids     map[string]string
}

func seedSales(t *testing.T, db *data.DB) salesFixture {
	t.Helper()
	ctx := context.Background()
	service := wallet.NewWalletService(db, nil, nil, nil)
	one := openWallet(t, service, "buyer-1", "credit", 2)
	two := openWallet(t, service, "buyer-2", "credit", 2)
	creditLimit(t, service, one.ID, "1000.00")
	creditLimit(t, service, two.ID, "1000.00")

	f := salesFixture{service: service, ids: map[string]string{}}
	a := openSale(t, service, one.ID, "sale-a", "100.00", 2, "2027-01-10", "Ana Souza", "Sofa")
	b := openSale(t, service, one.ID, "sale-b", "300.00", 3, "2026-11-10", "Bruno Lima", "Geladeira")
	c := openSale(t, service, two.ID, "sale-c", "200.00", 4, "2026-11-10", "Carla Dias", "Sofa cama")
	d := openSale(t, service, two.ID, "sale-d", "50.00", 1, "2026-11-10", "Ana Paula", "Mesa")
	for sequence, paid := range []string{"100.00", "100.00", "100.00"} {
		if _, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
			IdempotencyKey: "pay-b-" + paid + string(rune('0'+sequence)), AgreementID: b.Agreement.ID, Sequence: sequence + 1, Paid: paid, At: noon(t, "2026-11-10"),
		}); err != nil {
			t.Fatalf("paying sale-b: %v", err)
		}
	}
	if _, err := service.CancelCredit(ctx, staff(), wallet.CreditCancelRequest{IdempotencyKey: "cancel-c", AgreementID: c.Agreement.ID, Reason: "arrependimento"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepCredit(ctx, staff(), noon(t, "2026-12-31")); err != nil {
		t.Fatal(err)
	}
	f.ids = map[string]string{"sale-a": a.Agreement.ID, "sale-b": b.Agreement.ID, "sale-c": c.Agreement.ID, "sale-d": d.Agreement.ID}
	return f
}

// read lists with the address given, as the operator, and fails the test if it
// cannot.
func (f salesFixture) read(t *testing.T, table *pagination.Listing, address string, in wallet.CreditListFilter) wallet.CreditListing {
	t.Helper()
	values, err := url.ParseQuery(address)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.service.ListCredits(context.Background(), staff(), table.Parse(values), "/sales/credit", in)
	if err != nil {
		t.Fatalf("listing %q: %v", address, err)
	}
	return page
}

func references(page wallet.CreditListing) []string {
	out := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		out = append(out, strings.TrimPrefix(row.Reference, "order-"))
	}
	return out
}

func countsOf(page wallet.CreditListing) map[string]int {
	out := map[string]int{}
	for _, count := range page.Result.FacetCounts(wallet.CreditStatusFacet) {
		out[count.Value] = count.Count
	}
	return out
}

func TestTheStatusesAreCountedBesideEachValue(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		f := seedSales(t, db)
		table := creditTable(t, 10)

		page := f.read(t, table, "", wallet.CreditListFilter{})
		if page.Result.Total() != 4 || len(page.Rows) != 4 {
			t.Fatalf("the table holds %d, read %d rows, want 4", page.Result.Total(), len(page.Rows))
		}
		if got := countsOf(page); got["active"] != 1 || got["delinquent"] != 1 || got["settled"] != 1 || got["cancelled"] != 1 {
			t.Fatalf("the counts are %v, want one of each", got)
		}
		statuses := map[string]wallet.CreditStatus{"sale-a": wallet.CreditActive, "sale-b": wallet.CreditSettled, "sale-c": wallet.CreditCancelled, "sale-d": wallet.CreditDelinquent}
		for _, row := range page.Rows {
			if want := statuses[strings.TrimPrefix(row.Reference, "order-")]; row.Status != want {
				t.Errorf("%s reads %s, want %s", row.Reference, row.Status, want)
			}
		}

		// Choosing a status keeps its agreements, and the counts still say what
		// the others would add.
		page = f.read(t, table, "status=delinquent", wallet.CreditListFilter{})
		if got := references(page); len(got) != 1 || got[0] != "sale-d" {
			t.Fatalf("the delinquent are %v", got)
		}
		if got := countsOf(page); got["active"] != 1 || got["delinquent"] != 1 || got["settled"] != 1 {
			t.Fatalf("the counts under a chosen status are %v, want the facet's own choice left out", got)
		}
		if selected := page.Result.FacetCounts(wallet.CreditStatusFacet); !selected[1].Selected || selected[0].Selected {
			t.Fatalf("the choice is not marked: %+v", selected)
		}
		page = f.read(t, table, "status=active&status=settled", wallet.CreditListFilter{})
		if !sameSet(references(page), "sale-a", "sale-b") {
			t.Fatalf("two statuses keep %v", references(page))
		}
	})
}

func TestTheSearchReadsTheReferenceAndTheTerms(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		f := seedSales(t, db)
		table := creditTable(t, 10)

		for name, want := range map[string][]string{
			"q=ana":           {"sale-a", "sale-d"},
			"q=ANA+SOUZA":     {"sale-a"},
			"q=sofa":          {"sale-a", "sale-c"},
			"q=geladeira":     {"sale-b"},
			"q=order-sale-c":  {"sale-c"},
			"q=nobody":        nil,
			"q=%25":           nil,
			"q=s_le":          nil,
			"q=ana&q=ignored": {"sale-a", "sale-d"},
		} {
			page := f.read(t, table, name, wallet.CreditListFilter{})
			if !sameSet(references(page), want...) {
				t.Errorf("%s found %v, want %v", name, references(page), want)
			}
			if page.Result.Total() != len(want) {
				t.Errorf("%s counts %d, want %d", name, page.Result.Total(), len(want))
			}
		}

		// The search narrows the counts, and a chosen status does not narrow its own.
		page := f.read(t, table, "q=sofa&status=active", wallet.CreditListFilter{})
		if !sameSet(references(page), "sale-a") {
			t.Fatalf("a search and a status found %v", references(page))
		}
		if got := countsOf(page); got["active"] != 1 || got["cancelled"] != 1 || got["settled"] != 0 || got["delinquent"] != 0 {
			t.Fatalf("the counts under a search are %v", got)
		}
	})
}

func TestTheTableIsOrderedAndPagedByNumber(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		f := seedSales(t, db)
		table := creditTable(t, 2, 5)

		first := f.read(t, table, "sort=total&dir=asc", wallet.CreditListFilter{})
		if got := references(first); strings.Join(got, ",") != "sale-d,sale-a" {
			t.Fatalf("the smallest totals are %v", got)
		}
		if first.Result.Total() != 4 || first.Result.Page.LastPage() != 2 || first.Result.State.Page() != 1 {
			t.Fatalf("the first page says %d rows over %d pages, on page %d", first.Result.Total(), first.Result.Page.LastPage(), first.Result.State.Page())
		}
		second := f.read(t, table, "sort=total&dir=asc&page=2", wallet.CreditListFilter{})
		if got := references(second); strings.Join(got, ",") != "sale-c,sale-b" {
			t.Fatalf("the second page is %v", got)
		}
		beyond := f.read(t, table, "sort=total&dir=asc&page=9", wallet.CreditListFilter{})
		if strings.Join(references(beyond), ",") != "sale-c,sale-b" || beyond.Result.State.Page() != 2 {
			t.Fatalf("a page past the last reads %v on page %d, want the last", references(beyond), beyond.Result.State.Page())
		}
		if !strings.Contains(beyond.Result.Page.URL(1), "page=1") && !strings.Contains(beyond.Result.Page.URL(1), "/sales/credit") {
			t.Errorf("the links of the page are %q", beyond.Result.Page.URL(1))
		}

		for address, want := range map[string]string{
			"sort=installments&dir=desc":     "sale-c,sale-b",
			"sort=principal&dir=desc":        "sale-b,sale-c",
			"sort=reference&dir=asc":         "sale-a,sale-b",
			"sort=status&dir=asc":            "sale-a,sale-c",
			"sort=total&dir=desc&per_page=5": "sale-b,sale-c,sale-a,sale-d",
		} {
			if got := strings.Join(references(f.read(t, table, address, wallet.CreditListFilter{})), ","); got != want {
				t.Errorf("%s reads %s, want %s", address, got, want)
			}
		}

		// A page size of the table's own is read as it is.
		if got := f.read(t, table, "per_page=5", wallet.CreditListFilter{}); len(got.Rows) != 4 || got.Result.State.PerPage() != 5 {
			t.Fatalf("a page of five holds %d rows of %d", len(got.Rows), got.Result.State.PerPage())
		}
	})
}

func TestTheApplicationNarrowsBeforeTheTableDoes(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		f := seedSales(t, db)
		table := creditTable(t, 10)

		if page := f.read(t, table, "", wallet.CreditListFilter{HolderID: "buyer-2"}); !sameSet(references(page), "sale-c", "sale-d") || countsOf(page)["active"] != 0 {
			t.Fatalf("one holder's agreements are %v with counts %v", references(page), countsOf(page))
		}
		if page := f.read(t, table, "", wallet.CreditListFilter{Reference: "order-sale-", ReferencePrefix: true}); page.Result.Total() != 4 {
			t.Fatalf("a prefix keeps %v", references(page))
		}
		if page := f.read(t, table, "", wallet.CreditListFilter{Reference: "order-sale-b"}); !sameSet(references(page), "sale-b") {
			t.Fatalf("one reference keeps %v", references(page))
		}
		if page := f.read(t, table, "", wallet.CreditListFilter{Reference: "sale-", ReferencePrefix: true}); page.Result.Total() != 0 {
			t.Fatalf("a prefix of nothing keeps %v", references(page))
		}
	})
}

func TestTheTableReadsAPeriodOfOpening(t *testing.T) {
	t.Parallel()

	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)
	account := openWallet(t, service, "buyer-1", "credit", 2)
	creditLimit(t, service, account.ID, "1000.00")
	openSale(t, service, account.ID, "before", "10.00", 1, "2026-11-10", "A", "x")
	from := mark()
	openSale(t, service, account.ID, "inside", "10.00", 1, "2026-11-10", "B", "x")
	until := mark()
	openSale(t, service, account.ID, "after", "10.00", 1, "2026-11-10", "C", "x")

	f := salesFixture{service: service}
	table := creditTable(t, 10)
	for name, c := range map[string]struct {
		in   wallet.CreditListFilter
		want []string
	}{
		"both bounds":    {wallet.CreditListFilter{From: from, Until: until}, []string{"inside"}},
		"from only":      {wallet.CreditListFilter{From: from}, []string{"inside", "after"}},
		"until only":     {wallet.CreditListFilter{Until: until}, []string{"before", "inside"}},
		"the end is out": {wallet.CreditListFilter{From: from, Until: from.Add(time.Nanosecond)}, nil},
	} {
		page := f.read(t, table, "", c.in)
		if !sameSet(references(page), c.want...) {
			t.Errorf("%s keeps %v, want %v", name, references(page), c.want)
		}
		if got := countsOf(page)["active"]; got != len(c.want) {
			t.Errorf("%s counts %d active, want %d", name, got, len(c.want))
		}
	}
}

func TestTheTableKeepsTheReachOfCredits(t *testing.T) {
	t.Parallel()

	f := seedSales(t, database(t))
	table := creditTable(t, 10)
	ctx := context.Background()
	state := table.Parse(nil)

	// A holder reads their own, whatever the filter asks for.
	own, err := f.service.ListCredits(ctx, person("buyer-1"), state, "/sales", wallet.CreditListFilter{HolderID: "buyer-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(references(own), "sale-a", "sale-b") || own.Result.Total() != 2 {
		t.Fatalf("a holder who asked for another's read %v", references(own))
	}
	if got := countsOf(own); got["cancelled"] != 0 || got["delinquent"] != 0 || got["active"] != 1 || got["settled"] != 1 {
		t.Fatalf("a holder's counts are %v, and they count only their own", got)
	}

	// Somebody with no agreements reads none.
	none, err := f.service.ListCredits(ctx, person("stranger"), state, "/sales", wallet.CreditListFilter{HolderID: "buyer-1"})
	if err != nil || len(none.Rows) != 0 || none.Result.Total() != 0 {
		t.Fatalf("a stranger read %d agreements: %v", len(none.Rows), err)
	}

	// A guest, or an account of another tenant, is refused.
	for name, subject := range map[string]security.Subject{
		"a guest":              security.Guest(tenant),
		"another tenant's one": {ID: "staff-1", Tenant: "elsewhere", Roles: []string{wallet.OperatorRole}, Verified: true},
	} {
		page, err := f.service.ListCredits(ctx, subject, state, "/sales", wallet.CreditListFilter{})
		if err == nil && len(page.Rows) > 0 {
			t.Errorf("%s read %d agreements", name, len(page.Rows))
		}
	}
}

func TestTheTableTakesOnlyWhatTheWalletDeclaresFromTheState(t *testing.T) {
	t.Parallel()

	f := seedSales(t, database(t))
	// An application's listing: a sort and a facet the wallet does not know,
	// and a column of its own for a key the wallet does.
	spec := wallet.CreditListingSpec()
	spec.PageSizes, spec.DefaultPageSize = []int{10}, 10
	spec.Sorts["customer"] = "holder_id"
	spec.Sorts[wallet.CreditSortTotal] = "tenant_id; drop table x"
	spec.Facets = append(spec.Facets, pagination.FacetSpec{Key: "period", Column: "created_at", Values: []string{"2026-10-31"}})
	table, err := pagination.NewListing(spec)
	if err != nil {
		t.Fatal(err)
	}

	page := f.read(t, table, "sort=customer&dir=asc&period=2026-10-31&q=sofa", wallet.CreditListFilter{})
	if !sameSet(references(page), "sale-a", "sale-c") {
		t.Fatalf("the foreign facet and sort changed the rows: %v", references(page))
	}
	if !strings.Contains(page.Result.State.Encode(), "period=2026-10-31") || !strings.Contains(page.Result.Page.URL(1), "period=2026-10-31") {
		t.Fatalf("the state lost what the application put in the address: %q", page.Result.State.Encode())
	}
	if got := page.Result.FacetCounts("period"); got != nil {
		t.Fatalf("the wallet counted a facet it does not know: %v", got)
	}
	// The wallet's own column for the key moves, whatever the spec said.
	if got := strings.Join(references(f.read(t, table, "sort=total&dir=asc", wallet.CreditListFilter{})), ","); got != "sale-d,sale-a,sale-c,sale-b" {
		t.Fatalf("a total is ordered as %s", got)
	}
}

func TestTheTableRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	f := seedSales(t, database(t))
	ctx := context.Background()

	if _, err := f.service.ListCredits(ctx, staff(), pagination.ListingState{}, "/sales", wallet.CreditListFilter{}); !errors.Is(err, query.ErrNoListing) {
		t.Fatalf("a state no listing read answered %v", err)
	}

	big := wallet.CreditListingSpec()
	big.PageSizes, big.DefaultPageSize = []int{wallet.MaxPageSize + 1}, wallet.MaxPageSize+1
	listing, err := pagination.NewListing(big)
	if err != nil {
		t.Fatal(err)
	}
	var invalid validation.Errors
	if _, err := f.service.ListCredits(ctx, staff(), listing.Parse(nil), "/sales", wallet.CreditListFilter{}); !errors.As(err, &invalid) || len(invalid["per_page"]) == 0 {
		t.Fatalf("a page past the ceiling answered %v", err)
	}

	table := creditTable(t, 10)
	for name, in := range map[string]wallet.CreditListFilter{
		"a prefix of nothing": {ReferencePrefix: true},
		"a period backwards":  {From: time.Now(), Until: time.Now().Add(-time.Hour)},
		"a long reference":    {Reference: strings.Repeat("x", 4096)},
	} {
		if _, err := f.service.ListCredits(ctx, staff(), table.Parse(nil), "/sales", in); !errors.As(err, &invalid) {
			t.Errorf("%s answered %v", name, err)
		}
	}
}
