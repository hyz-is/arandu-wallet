package feature_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/validation"

	wallet "github.com/hyz-is/arandu-wallet"
)

// Agreements found by the creditor's reference, and read a page at a time.

func referencesOf(agreements []*wallet.CreditAgreement) map[string]bool {
	out := make(map[string]bool, len(agreements))
	for _, agreement := range agreements {
		out[agreement.Reference] = true
	}
	return out
}

func TestAgreementsAreFoundByTheirReference(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		ctx := context.Background()
		service := wallet.NewWalletService(db, nil, nil, nil)
		buyer := openWallet(t, service, "user-1", "main", 2)
		creditLimit(t, service, buyer.ID, "1000.00")
		for _, key := range []string{"2026-09-a", "2026-10-a", "2026-10-b", "2026-10_c"} {
			openAgreement(t, service, buyer.ID, key, "10.00", 2, 0)
		}

		for name, query := range map[string]struct {
			in   wallet.CreditQuery
			want []string
		}{
			"a month of orders by prefix": {wallet.CreditQuery{Reference: "order-2026-10", ReferencePrefix: true},
				[]string{"order-2026-10-a", "order-2026-10-b", "order-2026-10_c"}},
			"one order exactly":                      {wallet.CreditQuery{Reference: "order-2026-10-b"}, []string{"order-2026-10-b"}},
			"a prefix asked exactly matches nothing": {wallet.CreditQuery{Reference: "order-2026-10"}, nil},
			// A prefix is the leading characters, not a pattern: nothing in it is
			// a wildcard, and the case is compared as written.
			"a wildcard is a character": {wallet.CreditQuery{Reference: "order-2026-10_", ReferencePrefix: true},
				[]string{"order-2026-10_c"}},
			"a percent sign is a character": {wallet.CreditQuery{Reference: "order-%", ReferencePrefix: true}, nil},
			"the case is the reference's":   {wallet.CreditQuery{Reference: "ORDER-2026", ReferencePrefix: true}, nil},
		} {
			got, err := service.Credits(ctx, staff(), query.in)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			found := referencesOf(got)
			if len(got) != len(query.want) {
				t.Errorf("%s found %v, want %v", name, found, query.want)
				continue
			}
			for _, reference := range query.want {
				if !found[reference] {
					t.Errorf("%s found %v, want %v", name, found, query.want)
				}
			}
		}

		// A holder searching by reference finds only their own.
		other := openWallet(t, service, "user-2", "main", 2)
		creditLimit(t, service, other.ID, "1000.00")
		openAgreement(t, service, other.ID, "2026-10-z", "10.00", 2, 0)
		theirs, err := service.Credits(ctx, person("user-2"), wallet.CreditQuery{Reference: "order-2026-10", ReferencePrefix: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(theirs) != 1 || theirs[0].Reference != "order-2026-10-z" {
			t.Fatalf("the holder found %v, want only their own agreement", referencesOf(theirs))
		}
	})
}

func TestAgreementsAreReadAPageAtATime(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		ctx := context.Background()
		service := wallet.NewWalletService(db, nil, nil, nil)
		buyer := openWallet(t, service, "user-1", "main", 2)
		creditLimit(t, service, buyer.ID, "1000.00")
		keys := []string{"p1", "p2", "p3", "p4", "p5"}
		for _, key := range keys {
			openAgreement(t, service, buyer.ID, key, "10.00", 2, 0)
		}

		seen := map[string]bool{}
		cursor := ""
		pages := 0
		for {
			page, err := service.Credits(ctx, staff(), wallet.CreditQuery{HolderID: "user-1", Cursor: cursor, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			pages++
			for i, agreement := range page {
				if seen[agreement.ID] {
					t.Fatalf("page %d repeated agreement %s", pages, agreement.Reference)
				}
				seen[agreement.ID] = true
				if i > 0 {
					before := page[i-1]
					if agreement.CreatedAt.After(before.CreatedAt) ||
						(agreement.CreatedAt.Equal(before.CreatedAt) && agreement.ID > before.ID) {
						t.Fatalf("page %d is not newest first", pages)
					}
				}
			}
			if len(page) < 2 {
				break
			}
			cursor = page[len(page)-1].ID
			if pages > len(keys) {
				t.Fatal("the pages never ended")
			}
		}
		if len(seen) != len(keys) || pages != 3 {
			t.Fatalf("%d agreements in %d pages, want %d in 3", len(seen), pages, len(keys))
		}

		for name, query := range map[string]wallet.CreditQuery{
			"a page past the maximum":   {Limit: wallet.MaxPageSize + 1},
			"a negative page":           {Limit: -1},
			"an empty prefix":           {ReferencePrefix: true},
			"a reference past its size": {Reference: strings.Repeat("x", 121)},
		} {
			var errs validation.Errors
			if _, err := service.Credits(ctx, staff(), query); !errors.As(err, &errs) {
				t.Errorf("%s answered %v, want a validation error", name, err)
			}
		}
	})
}
