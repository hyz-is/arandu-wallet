package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The policy is checked against a database here, and not only in the unit
// suite, because the unit suite can only prove what the policy answers. What a
// caller can actually reach is a question about the statements the service
// issues afterwards, and it is the one that matters: a policy that refuses
// correctly while the query reads the row anyway refuses nothing.

func TestAWalletOfAnotherTenantIsNotReachable(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "key-1", "10.00")

	// The same identifier, the same role, another customer. Everything about
	// this subject is privileged except the one thing that decides.
	intruder := security.Subject{ID: "staff-1", Tenant: "globex", Roles: []string{wallet.OperatorRole}, Verified: true}
	ctx := context.Background()

	if _, err := service.Find(ctx, intruder, account.ID); !errors.Is(err, wallet.ErrNotFound) {
		t.Errorf("Find across tenants returned %v, want ErrNotFound", err)
	}
	if _, err := service.History(ctx, intruder, wallet.HistoryRequest{WalletID: account.ID}); !errors.Is(err, wallet.ErrNotFound) {
		t.Errorf("History across tenants returned %v, want ErrNotFound", err)
	}
	if _, err := service.Deposit(ctx, intruder, wallet.DepositRequest{
		IdempotencyKey: "key-2", WalletID: account.ID, Amount: "1.00",
	}); !errors.Is(err, wallet.ErrNotFound) {
		t.Errorf("Deposit across tenants returned %v, want ErrNotFound", err)
	}
	if _, err := service.Withdraw(ctx, intruder, wallet.WithdrawRequest{
		IdempotencyKey: "key-3", WalletID: account.ID, Amount: "1.00",
	}); !errors.Is(err, wallet.ErrNotFound) {
		t.Errorf("Withdraw across tenants returned %v, want ErrNotFound", err)
	}

	listed, err := service.List(ctx, intruder, wallet.ListRequest{})
	if err != nil {
		t.Fatalf("List across tenants: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("List across tenants returned %d wallets, want none", len(listed))
	}

	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d after everything another tenant tried, want 1000", got)
	}
}

func TestAHolderReachesTheirOwnMoneyAndNobodyElsesf(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	mine := openWallet(t, service, "user-1", "main", 2)
	theirs := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, mine.ID, "key-1", "10.00")
	deposit(t, service, theirs.ID, "key-2", "10.00")

	me := person("user-1")
	ctx := context.Background()

	// My own wallet, through the whole surface.
	if _, err := service.Find(ctx, me, mine.ID); err != nil {
		t.Errorf("the holder could not read their own wallet: %v", err)
	}
	if _, err := service.History(ctx, me, wallet.HistoryRequest{WalletID: mine.ID}); err != nil {
		t.Errorf("the holder could not read their own statement: %v", err)
	}
	if _, err := service.Withdraw(ctx, me, wallet.WithdrawRequest{
		IdempotencyKey: "key-3", WalletID: mine.ID, Amount: "1.00",
	}); err != nil {
		t.Errorf("the holder could not spend their own money: %v", err)
	}

	// And somebody else's, through the same surface. Every one of these is a
	// decision taken on the row that came back, not on the probe that let the
	// read happen.
	if _, err := service.Find(ctx, me, theirs.ID); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("the holder read another holder's wallet: %v", err)
	}
	if _, err := service.History(ctx, me, wallet.HistoryRequest{WalletID: theirs.ID}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("the holder read another holder's statement: %v", err)
	}
	if _, err := service.Withdraw(ctx, me, wallet.WithdrawRequest{
		IdempotencyKey: "key-4", WalletID: theirs.ID, Amount: "1.00",
	}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("the holder spent another holder's money: %v", err)
	}
	if _, err := service.Transfer(ctx, me, wallet.TransferRequest{
		IdempotencyKey: "key-5", FromWalletID: theirs.ID, ToWalletID: mine.ID, Amount: "1.00",
	}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("the holder transferred out of another holder's wallet: %v", err)
	}

	if got := balanceOf(t, service, theirs.ID); got != 1000 {
		t.Fatalf("the other holder's balance is %d, want 1000", got)
	}
	if got := balanceOf(t, service, mine.ID); got != 900 {
		t.Fatalf("the holder's balance is %d, want 900", got)
	}
}

func TestAListingNarrowsToTheHolderRatherThanRefusingIt(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	mine := openWallet(t, service, "user-1", "main", 2)
	openWallet(t, service, "user-2", "main", 2)

	// The predicate is in the statement, and what the caller asked for cannot
	// widen it: a holder who names somebody else gets their own rows, not a
	// refusal and not the other holder's.
	listed, err := service.List(context.Background(), person("user-1"), wallet.ListRequest{HolderID: "user-2"})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != mine.ID {
		t.Fatalf("the listing returned %d wallets, want only the caller's own", len(listed))
	}

	// The operator sees what they asked for.
	listed, err = service.List(context.Background(), staff(), wallet.ListRequest{HolderID: "user-2"})
	if err != nil {
		t.Fatalf("listing as an operator: %v", err)
	}
	if len(listed) != 1 || listed[0].HolderID != "user-2" {
		t.Fatalf("the operator's listing returned %d wallets, want the one they named", len(listed))
	}
}

func TestOnlyAnOperatorReversesAgainstTheDatabase(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	credited := deposit(t, service, account.ID, "key-1", "10.00")

	_, err := service.Reverse(context.Background(), person("user-1"), wallet.ReverseRequest{
		IdempotencyKey: "key-2", OperationID: credited.Operation.ID, Reason: "I changed my mind",
	})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the holder reversed their own credit: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d, want 1000", got)
	}
}

func TestAGuestReachesNothing(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "key-1", "10.00")

	visitor := security.Guest(tenant)
	ctx := context.Background()

	if _, err := service.Find(ctx, visitor, account.ID); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("a guest read a wallet: %v", err)
	}
	if _, err := service.List(ctx, visitor, wallet.ListRequest{}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("a guest listed wallets: %v", err)
	}
	if _, err := service.Deposit(ctx, visitor, wallet.DepositRequest{
		IdempotencyKey: "key-2", WalletID: account.ID, Amount: "1.00",
	}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("a guest deposited: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d, want 1000", got)
	}
}
