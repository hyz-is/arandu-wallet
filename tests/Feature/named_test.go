package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// Reaching a wallet by the name its holder knows it by.
//
// The pair of holder and slug is what names a wallet, and it has been under a
// unique index since the table was created. What was missing was the read.

func TestAWalletIsReachableByItsHolderAndSlug(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	main := openWallet(t, service, "user-1", "main", 2)
	bonus := openWallet(t, service, "user-1", "bonus", 2)
	theirs := openWallet(t, service, "user-2", "main", 2)

	for _, c := range []struct {
		holder, slug, want string
	}{
		{"user-1", "main", main.ID},
		{"user-1", "bonus", bonus.ID},
		{"user-2", "main", theirs.ID},
	} {
		got, err := service.FindBySlug(ctx, staff(), c.holder, c.slug)
		if err != nil {
			t.Fatalf("reading %s/%s: %v", c.holder, c.slug, err)
		}
		if got.ID != c.want {
			t.Errorf("%s/%s answered %s, want %s", c.holder, c.slug, got.ID, c.want)
		}
	}

	if _, err := service.FindBySlug(ctx, staff(), "user-1", "nothing"); !errors.Is(err, wallet.ErrNotFound) {
		t.Errorf("a slug nobody opened answered %v, want ErrNotFound", err)
	}
	if _, err := service.FindBySlug(ctx, staff(), "", "main"); err == nil {
		t.Error("an empty holder was accepted, and a read with half a name reads somebody's first wallet")
	}
}

// TestFindBySlugOpensNothing holds the decision. A read that created what it
// did not find would open a wallet under a currency and a scale this package
// would have had to guess.
func TestFindBySlugOpensNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)

	if _, err := service.FindBySlug(ctx, staff(), "user-1", wallet.DefaultSlug); !errors.Is(err, wallet.ErrNotFound) {
		t.Fatalf("asking about a holder with no wallet answered %v, want ErrNotFound", err)
	}
	records, err := service.List(ctx, staff(), wallet.ListRequest{HolderID: "user-1"})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("the read opened %d wallets", len(records))
	}
}

func TestFindBySlugAsksAboutTheWalletItFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	openWallet(t, service, "user-2", "main", 2)

	if _, err := service.FindBySlug(ctx, person("user-1"), "user-2", "main"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a holder read somebody else's wallet by name: %v", err)
	}
}

func TestOpeningAWalletDerivesTheSlugFromItsName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)

	opened, err := service.Open(ctx, staff(), wallet.OpenRequest{
		HolderID: "user-1", Name: "Store Credit", Currency: "BRL", DecimalPlaces: 2,
	})
	if err != nil {
		t.Fatalf("opening with no slug: %v", err)
	}
	if opened.Slug != "store-credit" {
		t.Fatalf("the slug was derived as %q, want %q", opened.Slug, "store-credit")
	}
	if found, err := service.FindBySlug(ctx, staff(), "user-1", "store-credit"); err != nil {
		t.Fatalf("reading the derived slug back: %v", err)
	} else if found.ID != opened.ID {
		t.Errorf("the derived slug names %s, want %s", found.ID, opened.ID)
	}

	// A name that derives to nothing is refused rather than opened under a slug
	// nobody chose.
	if _, err := service.Open(ctx, staff(), wallet.OpenRequest{
		HolderID: "user-1", Name: "!!!", Currency: "BRL", DecimalPlaces: 2,
	}); err == nil {
		t.Error("a name that derives to no slug was accepted")
	}
}
