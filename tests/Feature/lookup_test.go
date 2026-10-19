package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"

	wallet "github.com/hyz-is/arandu-wallet"
)

// An operation found by the idempotency key its caller named it with -- the
// question a caller asks after a timeout, before deciding whether to send the
// request again.

func TestAnOperationIsFoundByItsIdempotencyKey(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	funded := deposit(t, service, account.ID, "fund", "10.00")

	found, err := service.OperationByKey(ctx, staff(), "fund")
	if err != nil {
		t.Fatalf("finding the deposit by its key: %v", err)
	}
	if found.Operation.ID != funded.Operation.ID || len(found.Entries) != 1 || found.Entries[0].Amount != 1000 {
		t.Fatalf("the key found %+v, want the deposit of 1000", found)
	}
	if found.Replayed {
		t.Fatal("a lookup answered as a replay; it is a read, and nothing was asked again")
	}

	if _, err := service.OperationByKey(ctx, staff(), "never-sent"); !errors.Is(err, wallet.ErrNotFound) {
		t.Fatalf("a key nobody sent answered %v, want ErrNotFound", err)
	}
	var errs validation.Errors
	if _, err := service.OperationByKey(ctx, staff(), ""); !errors.As(err, &errs) {
		t.Fatalf("an empty key answered %v, want a validation error", err)
	}

	// The key is the tenant's: another customer's subject finds nothing under it.
	other := security.Subject{ID: "staff-9", Tenant: "globex", Roles: []string{wallet.OperatorRole}, Verified: true}
	if _, err := service.OperationByKey(ctx, other, "fund"); !errors.Is(err, wallet.ErrNotFound) {
		t.Fatalf("another tenant found the key and got %v, want ErrNotFound", err)
	}
}

func TestAnOperationFoundByKeyShowsOnlyTheWalletsTheSubjectMayRead(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		ctx := context.Background()
		service := wallet.NewWalletService(db, nil, nil, nil)
		payer := openWallet(t, service, "user-1", "main", 2)
		payee := openWallet(t, service, "user-2", "main", 2)
		deposit(t, service, payer.ID, "fund", "10.00")
		if _, err := payment(t, service, payer.ID, payee.ID, "pay", "4.00"); err != nil {
			t.Fatal(err)
		}

		whole, err := service.OperationByKey(ctx, staff(), "pay")
		if err != nil || len(whole.Entries) != 2 {
			t.Fatalf("the operator read %d entries (%v), want both sides", len(whole.Entries), err)
		}

		for holder, walletID := range map[string]string{"user-1": payer.ID, "user-2": payee.ID} {
			seen, err := service.OperationByKey(ctx, person(holder), "pay")
			if err != nil {
				t.Fatalf("%s was refused the payment they were in: %v", holder, err)
			}
			if len(seen.Entries) != 1 || seen.Entries[0].WalletID != walletID {
				t.Fatalf("%s read %+v, want only their own side", holder, seen.Entries)
			}
		}

		if _, err := service.OperationByKey(ctx, person("user-3"), "pay"); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("somebody who was not in the payment got %v, want ErrForbidden", err)
		}
	})
}
