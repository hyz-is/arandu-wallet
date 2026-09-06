package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What a wallet says about itself, beside what it holds.
//
// The columns are the application's to write and this package's only to carry,
// so what is asserted here is that they travel: they go in when the wallet is
// opened, they come back on the record and in the response, and they can be
// corrected afterwards without touching the money.

func TestAWalletCarriesWhatItIsForAndTheApplicationsOwnFacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)

	opened, err := service.Open(ctx, staff(), wallet.OpenRequest{
		HolderID:      "user-1",
		Slug:          "main",
		Name:          "Main",
		Description:   "what the shop settles into",
		Meta:          wallet.Meta{"ledger_account": "2100", "contract": "c-77"},
		Currency:      "BRL",
		DecimalPlaces: 2,
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if opened.Description != "what the shop settles into" {
		t.Errorf("the wallet was opened with description %q", opened.Description)
	}
	if opened.Meta["ledger_account"] != "2100" {
		t.Errorf("the wallet was opened with meta %v", opened.Meta)
	}

	// And it comes back off the row rather than out of the value that wrote it.
	read, err := service.Find(ctx, staff(), opened.ID)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if read.Description != opened.Description {
		t.Errorf("the row says %q and the write said %q", read.Description, opened.Description)
	}
	if read.Meta["contract"] != "c-77" {
		t.Errorf("the row carries %v", read.Meta)
	}

	answered := wallet.NewResource(*read).ToArray()
	if answered["description"] != "what the shop settles into" {
		t.Errorf("the response says %v", answered["description"])
	}
	if facts, ok := answered["meta"].(map[string]string); !ok || facts["contract"] != "c-77" {
		t.Errorf("the response carries %v", answered["meta"])
	}
}

func TestDescribeReplacesTheLabelsAndLeavesTheMoneyAlone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "10.00")

	relabelled, err := service.Describe(ctx, staff(), wallet.DescribeRequest{
		WalletID:    account.ID,
		Name:        "Settlement",
		Description: "moved to the new contract",
		Meta:        wallet.Meta{"contract": "c-78"},
	})
	if err != nil {
		t.Fatalf("describing: %v", err)
	}
	if relabelled.Name != "Settlement" || relabelled.Description != "moved to the new contract" {
		t.Errorf("the wallet is %q / %q", relabelled.Name, relabelled.Description)
	}
	if relabelled.Meta["contract"] != "c-78" {
		t.Errorf("the wallet carries %v", relabelled.Meta)
	}
	if relabelled.Balance != 1000 {
		t.Errorf("the balance is %d after a relabelling, want 1000", relabelled.Balance)
	}
	if relabelled.Slug != account.Slug || relabelled.Currency != account.Currency ||
		relabelled.DecimalPlaces != account.DecimalPlaces {
		t.Errorf("a relabelling moved the slug, the currency or the scale: %q %q %d",
			relabelled.Slug, relabelled.Currency, relabelled.DecimalPlaces)
	}

	// Replaced and not merged, so a name can be taken away.
	cleared, err := service.Describe(ctx, staff(), wallet.DescribeRequest{
		WalletID: account.ID, Name: "Settlement",
	})
	if err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if len(cleared.Meta) != 0 || cleared.Description != "" {
		t.Errorf("the wallet still carries %q / %v", cleared.Description, cleared.Meta)
	}
}

// TestDescribeIsAllowedOnAFrozenWallet holds the boundary between the gate on
// money and everything else. A freeze says this package cannot explain the
// balance; it says nothing about what the wallet is called.
func TestDescribeIsAllowedOnAFrozenWallet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := database(t)
	service := wallet.NewWalletService(handle, nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "10.00")

	diverge(t, handle, account.ID, 500)
	if report, err := service.Reconcile(ctx, staff(), account.ID); err != nil {
		t.Fatalf("reconciling: %v", err)
	} else if !report.Frozen {
		t.Fatal("the wallet was not frozen")
	}

	if _, err := service.Describe(ctx, staff(), wallet.DescribeRequest{
		WalletID: account.ID, Name: "Under investigation",
		Description: "frozen on 2026-09-06, ticket 41",
	}); err != nil {
		t.Fatalf("describing a frozen wallet: %v", err)
	}
	if _, err := service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
		IdempotencyKey: "still-frozen", WalletID: account.ID, Amount: "1.00",
	}); !errors.Is(err, wallet.ErrWalletFrozen) {
		t.Fatalf("the money moved on a frozen wallet: %v", err)
	}
}

func TestAHolderMayRelabelTheirOwnWalletAndNobodyElses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	mine := openWallet(t, service, "user-1", "main", 2)
	theirs := openWallet(t, service, "user-2", "main", 2)

	if _, err := service.Describe(ctx, person("user-1"), wallet.DescribeRequest{
		WalletID: mine.ID, Name: "Mine",
	}); err != nil {
		t.Fatalf("a holder relabelling their own wallet: %v", err)
	}
	if _, err := service.Describe(ctx, person("user-1"), wallet.DescribeRequest{
		WalletID: theirs.ID, Name: "Not mine",
	}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a holder relabelled somebody else's wallet: %v", err)
	}
}
