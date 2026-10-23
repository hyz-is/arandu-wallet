package feature_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The listing a server-side data table pages through: the operations of a
// tenant, narrowed and ordered in the database, and the platform's listing of
// every tenant.

// list reads one page as the operator and fails the test if it cannot.
func list(t *testing.T, service *wallet.WalletService, in wallet.OperationFilter) wallet.OperationPage {
	t.Helper()
	page, err := service.ListOperations(context.Background(), staff(), in)
	if err != nil {
		t.Fatalf("listing %+v: %v", in, err)
	}
	return page
}

// keysOf is the idempotency keys of a page's rows, in order.
func keysOf(page wallet.OperationPage) []string {
	out := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		out = append(out, row.IdempotencyKey)
	}
	return out
}

// sameSet reports that got holds exactly want, in any order.
func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	a := append([]string(nil), got...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// depositWith credits a wallet with metadata on the operation.
func depositWith(t *testing.T, service *wallet.WalletService, actor security.Subject, walletID, key, amount string, pending bool, meta wallet.Meta) wallet.Receipt {
	t.Helper()
	receipt, err := service.Deposit(context.Background(), actor, wallet.DepositRequest{
		IdempotencyKey: key, WalletID: walletID, Amount: amount, Pending: pending, Meta: meta,
	})
	if err != nil {
		t.Fatalf("depositing %s under %s: %v", amount, key, err)
	}
	return receipt
}

// ledgerFixture is a tenant's ledger with one row in every status, two
// holders, and payments told apart from the rest by their metadata.
type ledgerFixture struct {
	main, credits, other *wallet.Wallet
}

func seedLedger(t *testing.T, service *wallet.WalletService) ledgerFixture {
	t.Helper()
	ctx := context.Background()
	f := ledgerFixture{
		main:    openWallet(t, service, "user-1", "main", 2),
		credits: openWallet(t, service, "user-1", "credits", 2),
		other:   openWallet(t, service, "user-2", "main", 2),
	}
	payment := func(reference string) wallet.Meta {
		return wallet.Meta{"reason": "payment", "reference": reference, "description": "Pix <recarga> & bônus"}
	}

	depositWith(t, service, staff(), f.main.ID, "paid", "100.00", false, payment("ALPR-AAA111"))
	confirmable := depositWith(t, service, staff(), f.credits.ID, "confirmable", "50.00", true, payment("ALPR-BBB222"))
	depositWith(t, service, staff(), f.credits.ID, "waiting", "30.00", true, payment("ALPR-CCC333"))
	voidable := depositWith(t, service, staff(), f.main.ID, "voidable", "20.00", true, payment("ALPR-DDD444"))
	reversible := depositWith(t, service, staff(), f.main.ID, "reversible", "10.00", false, wallet.Meta{"reason": "bonus"})
	partial := depositWith(t, service, staff(), f.other.ID, "partial", "40.00", false, wallet.Meta{"reason": "bonus"})

	if _, err := confirm(t, service, staff(), confirmable.Operation.ID, "confirm-1"); err != nil {
		t.Fatalf("confirming: %v", err)
	}
	if _, err := void(service, staff(), voidable.Operation.ID, "void-1"); err != nil {
		t.Fatalf("voiding: %v", err)
	}
	if _, err := service.Reverse(ctx, staff(), wallet.ReverseRequest{
		IdempotencyKey: "reverse-1", OperationID: reversible.Operation.ID, Reason: "refunded",
	}); err != nil {
		t.Fatalf("reversing: %v", err)
	}
	if _, err := reversePart(service, staff(), partial.Operation.ID, "part-1", "15.00"); err != nil {
		t.Fatalf("reversing in part: %v", err)
	}
	if _, err := service.Transfer(ctx, staff(), wallet.TransferRequest{
		IdempotencyKey: "move", FromWalletID: f.main.ID, ToWalletID: f.credits.ID, Amount: "5.00",
	}); err != nil {
		t.Fatalf("transferring: %v", err)
	}
	return f
}

func TestTheListingFiltersInTheDatabase(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		service := wallet.NewWalletService(db, nil, nil, nil)
		f := seedLedger(t, service)

		everything := list(t, service, wallet.OperationFilter{Count: true, Limit: 200})
		// Six deposits, a confirmation, a void, a reversal, a partial reversal
		// and a transfer written on both sides.
		if everything.Total != 12 || len(everything.Rows) != 12 || everything.HasMore() {
			t.Fatalf("the whole ledger is %d rows of a total %d, more %v; want 12", len(everything.Rows), everything.Total, everything.HasMore())
		}

		byStatus := map[wallet.OperationStatus][]string{
			// A reversal and a partial reversal are movements that counted, and
			// nothing undid them.
			wallet.OperationSettled:           {"paid", "confirm-1", "move", "move", "reverse-1", "part-1"},
			wallet.OperationConfirmed:         {"confirmable"},
			wallet.OperationPending:           {"waiting"},
			wallet.OperationVoided:            {"voidable", "void-1"},
			wallet.OperationReversed:          {"reversible"},
			wallet.OperationPartiallyReversed: {"partial"},
		}
		for status, want := range byStatus {
			page := list(t, service, wallet.OperationFilter{Statuses: []wallet.OperationStatus{status}, Count: true})
			if !sameSet(keysOf(page), want...) || page.Total != int64(len(want)) {
				t.Errorf("%s keeps %v (total %d), want %v", status, keysOf(page), page.Total, want)
			}
			for _, row := range page.Rows {
				if row.Status != status {
					t.Errorf("a row the %s filter kept answers %s", status, row.Status)
				}
			}
		}
		// The partial reversal moved 15.00 back of 40.00, so it is a settled
		// row of its own, and two statuses together are their union.
		page := list(t, service, wallet.OperationFilter{Statuses: []wallet.OperationStatus{wallet.OperationPending, wallet.OperationConfirmed}})
		if !sameSet(keysOf(page), "waiting", "confirmable") {
			t.Errorf("pending or confirmed keeps %v", keysOf(page))
		}

		page = list(t, service, wallet.OperationFilter{
			Kinds: []wallet.OperationKind{wallet.OperationDeposit}, Meta: wallet.Meta{"reason": "payment"},
		})
		if !sameSet(keysOf(page), "paid", "confirmable", "waiting", "voidable") {
			t.Errorf("the payments are %v", keysOf(page))
		}
		page = list(t, service, wallet.OperationFilter{Meta: wallet.Meta{"reason": "payment", "reference": "ALPR-CCC333"}})
		if !sameSet(keysOf(page), "waiting") {
			t.Errorf("one payment by its reference is %v", keysOf(page))
		}
		if page := list(t, service, wallet.OperationFilter{Meta: wallet.Meta{"reason": "pay"}}); len(page.Rows) != 0 {
			t.Errorf("metadata is matched by equality, and %q found %v", "pay", keysOf(page))
		}

		page = list(t, service, wallet.OperationFilter{WalletIDs: []string{f.credits.ID}})
		if !sameSet(keysOf(page), "confirmable", "waiting", "confirm-1", "move") {
			t.Errorf("the credits wallet reads %v", keysOf(page))
		}
		page = list(t, service, wallet.OperationFilter{WalletIDs: []string{f.main.ID, f.credits.ID}, Direction: wallet.EntryWithdraw})
		if !sameSet(keysOf(page), "void-1", "reverse-1", "move") {
			t.Errorf("money out of user-1's wallets is %v", keysOf(page))
		}
		page = list(t, service, wallet.OperationFilter{HolderID: "user-2"})
		if !sameSet(keysOf(page), "partial", "part-1") {
			t.Errorf("user-2's movements are %v", keysOf(page))
		}

		for term, want := range map[string][]string{
			"confirmable": {"confirmable"},
			"alpr-ddd":    {"voidable"},
			"<RECARGA>":   {"paid", "confirmable", "waiting", "voidable"},
			"bônus":       {"paid", "confirmable", "waiting", "voidable"},
			"refunded":    {"reverse-1"},
		} {
			if page := list(t, service, wallet.OperationFilter{Search: term}); !sameSet(keysOf(page), want...) {
				t.Errorf("searching %q finds %v, want %v", term, keysOf(page), want)
			}
		}

		page = list(t, service, wallet.OperationFilter{Sort: wallet.OperationsLargest, Limit: 3})
		if got := keysOf(page); len(got) != 3 || got[0] != "paid" || !sameSet(got[1:], "confirmable", "confirm-1") {
			t.Errorf("the largest first are %v", got)
		}
		page = list(t, service, wallet.OperationFilter{Sort: wallet.OperationsSmallest, Limit: 2})
		if got := keysOf(page); !sameSet(got, "move", "move") {
			t.Errorf("the smallest first are %v", got)
		}

		for _, row := range everything.Rows {
			if row.TenantID != tenant || row.Currency != "BRL" || row.DecimalPlaces != 2 || row.WalletSlug == "" {
				t.Fatalf("a row does not say whose wallet it moved: %+v", row)
			}
			if row.IdempotencyKey == "confirmable" && (row.ResolvedBy == "" || row.Money().String() != "50.00 BRL") {
				t.Errorf("the confirmed deposit reads %+v", row)
			}
			if row.IdempotencyKey == "reverse-1" && row.RefersTo == "" {
				t.Error("a reversal does not name what it reversed")
			}
		}
	})
}

func TestTheListingReadsAPeriod(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "before", "1.00")
	from := mark()
	deposit(t, service, account.ID, "inside", "2.00")
	until := mark()
	deposit(t, service, account.ID, "after", "3.00")

	if page := list(t, service, wallet.OperationFilter{From: from, Until: until}); !sameSet(keysOf(page), "inside") {
		t.Fatalf("the period keeps %v", keysOf(page))
	}
	if page := list(t, service, wallet.OperationFilter{From: from}); !sameSet(keysOf(page), "inside", "after") {
		t.Fatalf("from the mark keeps %v", keysOf(page))
	}
	if page := list(t, service, wallet.OperationFilter{From: from, Until: until, Basis: wallet.DateAccrued}); !sameSet(keysOf(page), "inside") {
		t.Fatalf("by accrual, an operation that named no date is read when it was recorded: %v", keysOf(page))
	}
}

func TestKeysetPagesNeitherRepeatNorSkipWhileMoneyMoves(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		service := wallet.NewWalletService(db, nil, nil, nil)
		account := openWallet(t, service, "user-1", "main", 2)
		for i := range 25 {
			deposit(t, service, account.ID, fmt.Sprintf("seed-%02d", i), fmt.Sprintf("%d.00", i%7+1))
		}

		for _, order := range []wallet.OperationSort{wallet.OperationsNewest, wallet.OperationsOldest, wallet.OperationsLargest, wallet.OperationsSmallest} {
			// Money moves between two reads only where it lands before the
			// first page: newest first, and largest first, since it is the
			// largest. Read the other way, it would be read at the end.
			moving := order == wallet.OperationsNewest || order == wallet.OperationsLargest
			before := list(t, service, wallet.OperationFilter{WalletIDs: []string{account.ID}, Limit: 1, Count: true}).Total

			seen := map[string]bool{}
			var pages []wallet.OperationPage
			cursor := ""
			for n := 0; ; n++ {
				page := list(t, service, wallet.OperationFilter{WalletIDs: []string{account.ID}, Sort: order, Limit: 10, Cursor: cursor})
				pages = append(pages, page)
				for _, row := range page.Rows {
					if seen[row.EntryID] {
						t.Fatalf("%q: page %d repeats %s", order, n, row.IdempotencyKey)
					}
					seen[row.EntryID] = true
				}
				if moving {
					deposit(t, service, account.ID, fmt.Sprintf("late-%s-%d", order, n), "100.00")
				}
				if !page.HasMore() {
					break
				}
				cursor = page.NextCursor
			}
			if int64(len(seen)) != before {
				t.Fatalf("%q read %d rows across %d pages, want the %d there were when the first was read", order, len(seen), len(pages), before)
			}
			if pages[0].PrevCursor != "" {
				t.Errorf("%q: the first page has a page before it", order)
			}

			// Back from the second page is the first page again: what was
			// written since landed before it, never between the two.
			back := list(t, service, wallet.OperationFilter{WalletIDs: []string{account.ID}, Sort: order, Limit: 10, Cursor: pages[1].PrevCursor})
			if !sameOrder(keysOf(back), keysOf(pages[0])) || back.NextCursor == "" {
				t.Errorf("%q: back from page 2 reads %v, want %v", order, keysOf(back), keysOf(pages[0]))
			}
			if (back.PrevCursor != "") != moving {
				t.Errorf("%q: back from page 2 says a page before it is %v", order, back.PrevCursor != "")
			}
		}

		// Numbered pages read the same order by offset, and count.
		first := list(t, service, wallet.OperationFilter{WalletIDs: []string{account.ID}, Sort: wallet.OperationsOldest, Limit: 10, Page: 1, Count: true})
		second := list(t, service, wallet.OperationFilter{WalletIDs: []string{account.ID}, Sort: wallet.OperationsOldest, Limit: 10, Page: 2, Count: true})
		byCursor := list(t, service, wallet.OperationFilter{WalletIDs: []string{account.ID}, Sort: wallet.OperationsOldest, Limit: 10, Cursor: first.NextCursor})
		if !sameOrder(keysOf(second), keysOf(byCursor)) || second.Page != 2 || second.Pages() != int((second.Total+9)/10) || second.PrevCursor == "" {
			t.Fatalf("page 2 by number is %v, by cursor %v (page %d of %d)", keysOf(second), keysOf(byCursor), second.Page, second.Pages())
		}
	})
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTheListingStaysInsideTheTenant(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		ctx := context.Background()
		service := wallet.NewWalletService(db, nil, nil, nil)
		ours := openWallet(t, service, "user-1", "main", 2)
		depositWith(t, service, staff(), ours.ID, "ours", "1.00", false, wallet.Meta{"reason": "payment"})

		theirStaff := security.Subject{ID: "staff-9", Tenant: "globex", Roles: []string{wallet.OperatorRole}, Verified: true}
		theirs, err := service.Open(ctx, theirStaff, wallet.OpenRequest{HolderID: "user-1", Slug: "main", Name: "Theirs", Currency: "BRL", DecimalPlaces: 2})
		if err != nil {
			t.Fatalf("opening a wallet in another tenant: %v", err)
		}
		depositWith(t, service, theirStaff, theirs.ID, "theirs", "9.00", false, wallet.Meta{"reason": "payment"})
		depositWith(t, service, theirStaff, theirs.ID, "theirs-too", "8.00", false, nil)

		page := list(t, service, wallet.OperationFilter{Meta: wallet.Meta{"reason": "payment"}, Count: true})
		if !sameSet(keysOf(page), "ours") || page.Total != 1 {
			t.Fatalf("the operator of acme reads %v (total %d)", keysOf(page), page.Total)
		}
		if page := list(t, service, wallet.OperationFilter{Search: "theirs"}); len(page.Rows) != 0 {
			t.Fatalf("searching finds another tenant's %v", keysOf(page))
		}
		if _, err := service.ListOperations(ctx, staff(), wallet.OperationFilter{WalletIDs: []string{theirs.ID}}); !errors.Is(err, wallet.ErrNotFound) {
			t.Fatalf("naming another tenant's wallet answered %v, want ErrNotFound", err)
		}
		theirPage, err := service.ListOperations(ctx, theirStaff, wallet.OperationFilter{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if !sameSet(keysOf(theirPage), "theirs-too") || !theirPage.HasMore() {
			t.Fatalf("the operator of globex reads %v", keysOf(theirPage))
		}
		// A cursor anchored on another tenant's row anchors on nothing here.
		if page := list(t, service, wallet.OperationFilter{Cursor: theirPage.NextCursor}); len(page.Rows) != 0 {
			t.Fatalf("another tenant's cursor read %v", keysOf(page))
		}
	})
}

func TestAHolderListsOnlyTheirOwnWallets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	mine := openWallet(t, service, "user-1", "main", 2)
	theirs := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, mine.ID, "mine", "1.00")
	deposit(t, service, theirs.ID, "theirs", "1.00")

	page, err := service.ListOperations(ctx, person("user-1"), wallet.OperationFilter{HolderID: "user-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(keysOf(page), "mine") {
		t.Fatalf("a holder asking for somebody else's reads %v, want their own", keysOf(page))
	}
	if _, err := service.ListOperations(ctx, person("user-1"), wallet.OperationFilter{WalletIDs: []string{theirs.ID}}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a holder naming somebody else's wallet answered %v, want ErrForbidden", err)
	}

	// The operator held as an arandu-permission action is the operator.
	granted := security.Subject{ID: "finance-1", Tenant: tenant, Actions: []security.Action{wallet.OperatorAction}, Verified: true}
	page, err = service.ListOperations(ctx, granted, wallet.OperationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(keysOf(page), "mine", "theirs") {
		t.Fatalf("a subject granted %s reads %v", wallet.OperatorAction, keysOf(page))
	}
}

func TestThePlatformListsEveryTenantAndNobodyElseDoes(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		ctx := context.Background()
		service := wallet.NewWalletService(db, nil, nil, nil)
		ours := openWallet(t, service, "user-1", "main", 2)
		depositWith(t, service, staff(), ours.ID, "acme-paid", "1.00", true, wallet.Meta{"reason": "payment"})
		depositWith(t, service, staff(), ours.ID, "acme-bonus", "2.00", false, wallet.Meta{"reason": "bonus"})

		theirStaff := security.Subject{ID: "staff-9", Tenant: "globex", Roles: []string{wallet.OperatorRole}, Verified: true}
		theirs, err := service.Open(ctx, theirStaff, wallet.OpenRequest{HolderID: "user-7", Slug: "main", Name: "Theirs", Currency: "BRL", DecimalPlaces: 2})
		if err != nil {
			t.Fatal(err)
		}
		paid := depositWith(t, service, theirStaff, theirs.ID, "globex-paid", "9.00", true, wallet.Meta{"reason": "payment"})
		if _, err := confirm(t, service, theirStaff, paid.Operation.ID, "globex-confirm"); err != nil {
			t.Fatal(err)
		}

		platform := security.Subject{ID: "root", Tenant: "platform", Roles: []string{wallet.PlatformRole}, Verified: true}
		page, err := service.ListAllOperations(ctx, platform, wallet.PlatformOperationFilter{
			OperationFilter: wallet.OperationFilter{Kinds: []wallet.OperationKind{wallet.OperationDeposit}, Meta: wallet.Meta{"reason": "payment"}, Count: true},
		})
		if err != nil {
			t.Fatalf("the platform's listing: %v", err)
		}
		if !sameSet(keysOf(page), "acme-paid", "globex-paid") || page.Total != 2 {
			t.Fatalf("the platform reads %v (total %d)", keysOf(page), page.Total)
		}
		for _, row := range page.Rows {
			want := map[string]wallet.OperationStatus{"acme-paid": wallet.OperationPending, "globex-paid": wallet.OperationConfirmed}[row.IdempotencyKey]
			wantTenant := map[string]string{"acme-paid": tenant, "globex-paid": "globex"}[row.IdempotencyKey]
			if row.Status != want || row.TenantID != wantTenant || row.HolderID == "" {
				t.Errorf("%s reads %s in %s, want %s in %s", row.IdempotencyKey, row.Status, row.TenantID, want, wantTenant)
			}
		}

		page, err = service.ListAllOperations(ctx, platform, wallet.PlatformOperationFilter{
			OperationFilter: wallet.OperationFilter{Statuses: []wallet.OperationStatus{wallet.OperationConfirmed}},
			Tenants:         []string{"globex"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !sameSet(keysOf(page), "globex-paid") {
			t.Fatalf("one tenant's confirmed payments are %v", keysOf(page))
		}

		// Paging across tenants is one keyset.
		seen := map[string]bool{}
		cursor := ""
		for {
			page, err := service.ListAllOperations(ctx, platform, wallet.PlatformOperationFilter{OperationFilter: wallet.OperationFilter{Limit: 1, Cursor: cursor}})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range page.Rows {
				if seen[row.EntryID] {
					t.Fatalf("the platform's pages repeat %s", row.IdempotencyKey)
				}
				seen[row.EntryID] = true
			}
			if !page.HasMore() {
				break
			}
			cursor = page.NextCursor
		}
		if len(seen) != 4 {
			t.Fatalf("the platform paged through %d rows, want 4", len(seen))
		}

		granted := security.Subject{ID: "root-2", Tenant: "platform", Actions: []security.Action{wallet.WalletOperationsAll}, Verified: true}
		if _, err := service.ListAllOperations(ctx, granted, wallet.PlatformOperationFilter{}); err != nil {
			t.Fatalf("a subject granted %s was refused: %v", wallet.WalletOperationsAll, err)
		}

		impersonating := platform
		impersonating.Impersonator = &security.Subject{ID: "root", Tenant: "platform", Roles: []string{wallet.PlatformRole}}
		grantedOperator := security.Subject{ID: "finance-1", Tenant: tenant, Actions: []security.Action{wallet.OperatorAction}, Verified: true}
		for name, subject := range map[string]security.Subject{
			"a tenant's operator":                   staff(),
			"an operator by action":                 grantedOperator,
			"a holder":                              person("user-1"),
			"a guest":                               security.Guest(tenant),
			"inside somebody else's account":        impersonating,
			"the operator of the platform's tenant": {ID: "x", Tenant: "platform", Roles: []string{wallet.OperatorRole}},
		} {
			if _, err := service.ListAllOperations(ctx, subject, wallet.PlatformOperationFilter{}); !errors.Is(err, security.ErrForbidden) {
				t.Errorf("%s read every tenant: %v", name, err)
			}
		}
	})
}

func TestPlatformOperatorForComesFromTheApplicationsDecision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "one", "1.00")

	g, err := security.Authorize(ctx, allowAll{}, security.Subject{ID: "admin-1", Tenant: "platform", Verified: true}, "admin.view", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	subject, err := wallet.PlatformOperatorFor(g)
	if err != nil {
		t.Fatal(err)
	}
	if !wallet.IsPlatformOperator(subject) || wallet.IsOperator(subject) {
		t.Fatalf("PlatformOperatorFor answered %+v", subject)
	}
	page, err := service.ListAllOperations(ctx, subject, wallet.PlatformOperationFilter{})
	if err != nil || !sameSet(keysOf(page), "one") {
		t.Fatalf("the derived platform operator read %v: %v", keysOf(page), err)
	}
	if _, err := wallet.PlatformOperatorFor(security.Grant{}); err == nil {
		t.Fatal("the zero Grant became a platform operator")
	}
}

// allowAll is an application's own policy that has already decided.
type allowAll struct{}

func (allowAll) Can(context.Context, security.Subject, security.Action, struct{}) error { return nil }

func TestTheListingRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	for name, filter := range map[string]wallet.OperationFilter{
		"a kind":           {Kinds: []wallet.OperationKind{"payout"}},
		"a status":         {Statuses: []wallet.OperationStatus{"failed"}},
		"a sort":           {Sort: "balance"},
		"a cursor":         {Cursor: "not-a-cursor"},
		"page and cursor":  {Page: 2, Cursor: "bjp4"},
		"a limit":          {Limit: wallet.MaxPageSize + 1},
		"a meta name":      {Meta: wallet.Meta{`a"b`: "x"}},
		"a period":         {From: time.Now(), Until: time.Now().Add(-time.Hour)},
		"a direction":      {Direction: "sideways"},
		"too many wallets": {WalletIDs: make([]string, wallet.MaxListingValues+1)},
	} {
		_, err := service.ListOperations(context.Background(), staff(), filter)
		var errs validation.Errors
		if !errors.As(err, &errs) {
			t.Errorf("%s was not refused as invalid: %v", name, err)
		}
	}
	if _, err := service.ListAllOperations(context.Background(), staff(), wallet.PlatformOperationFilter{Tenants: []string{"../x"}}); err == nil {
		t.Error("a tenant that cannot be one was accepted")
	}
}

func TestTheMetadataIndexAnswersAContainmentTestOnPostgres(t *testing.T) {
	t.Parallel()

	db := postgres(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	// An empty table is read by scanning it whatever its indexes, so the scan
	// is priced out of the plan: what is asked is whether the index can answer
	// the expression the listing writes, not whether it is worth it today.
	if _, err := tx.ExecContext(ctx, "set local enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(ctx,
		`explain select 1 from wallet_operations as o where nullif(o.meta, '')::jsonb @> cast($1 as jsonb)`,
		`{"reason":"payment"}`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	plan := ""
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	if !strings.Contains(plan, "wallet_operations_meta_gin") {
		t.Fatalf("the metadata filter is not answered by its index:\n%s", plan)
	}
}

func TestTheListingFiltersOnMySQL(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(mysqlServer(t), nil, nil, nil)
	f := seedLedger(t, service)
	page := list(t, service, wallet.OperationFilter{
		Kinds: []wallet.OperationKind{wallet.OperationDeposit}, Meta: wallet.Meta{"reason": "payment"},
		Statuses: []wallet.OperationStatus{wallet.OperationPending, wallet.OperationConfirmed}, Count: true,
	})
	if !sameSet(keysOf(page), "confirmable", "waiting") || page.Total != 2 {
		t.Fatalf("pending and confirmed payments are %v", keysOf(page))
	}
	if page := list(t, service, wallet.OperationFilter{WalletIDs: []string{f.main.ID}, Search: "alpr-aaa"}); !sameSet(keysOf(page), "paid") {
		t.Fatalf("searching finds %v", keysOf(page))
	}
}
