package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A credit limit is the one thing in this package that lets a balance be
// negative, so what it is worth is entirely in where it stops.
//
// The tests below are about the boundary and about who may move it. The one
// that matters under load is in postgres_test.go, because the limit is enforced
// by a predicate on the statement that moves the money and only an engine whose
// transactions really interleave can tell that apart from a value somebody read
// a moment earlier.

// creditLimit sets a wallet's limit and fails the test if it cannot.
func creditLimit(t *testing.T, service *wallet.WalletService, walletID, limit string) *wallet.Wallet {
	t.Helper()

	record, err := service.SetCredit(context.Background(), staff(), wallet.CreditRequest{
		WalletID: walletID,
		Limit:    limit,
	})
	if err != nil {
		t.Fatalf("setting the credit limit of %s to %s: %v", walletID, limit, err)
	}
	return record
}

// withdraw takes money out and answers with whatever the service said.
func withdraw(t *testing.T, service *wallet.WalletService, actor security.Subject, walletID, key, amount string, force bool) error {
	t.Helper()

	_, err := service.Withdraw(context.Background(), actor, wallet.WithdrawRequest{
		IdempotencyKey: key,
		WalletID:       walletID,
		Amount:         amount,
		Force:          force,
	})
	return err
}

func TestACreditLimitLetsTheBalanceGoBelowZeroAndNoFurther(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	if got := creditLimit(t, service, account.ID, "10.00").CreditLimit; got != 1000 {
		t.Fatalf("the wallet reports a credit limit of %d, want 1000", got)
	}

	// Nothing was ever deposited, so every unit below comes out of the limit.
	if err := withdraw(t, service, staff(), account.ID, "spend-1", "4.00", false); err != nil {
		t.Fatalf("a withdrawal inside the credit limit was refused: %v", err)
	}
	if err := withdraw(t, service, staff(), account.ID, "spend-2", "6.00", false); err != nil {
		t.Fatalf("a withdrawal reaching exactly the credit limit was refused: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != -1000 {
		t.Fatalf("the balance is %d, want -1000", got)
	}

	// And one minor unit past it is refused, which is the whole of what the
	// limit is: a floor, not a suggestion.
	if err := withdraw(t, service, staff(), account.ID, "spend-3", "0.01", false); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("a withdrawal one minor unit past the credit limit returned %v, want ErrInsufficientFunds", err)
	}
	if got := balanceOf(t, service, account.ID); got != -1000 {
		t.Fatalf("the refused withdrawal moved the balance to %d, want -1000", got)
	}

	// The ledger still explains the balance, negative or not. This is the
	// invariant every other test in the package leans on, and a wallet below
	// zero is exactly where an accounting that only added up on the way down
	// would stop adding up.
	if ledger := ledgerOf(t, service, account.ID); ledger != -1000 {
		t.Fatalf("the ledger sums to %d and the balance column says -1000", ledger)
	}
}

func TestACreditLimitIsNotLoweredBelowWhatIsAlreadySpent(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	creditLimit(t, service, account.ID, "10.00")
	if err := withdraw(t, service, staff(), account.ID, "spend-1", "8.00", false); err != nil {
		t.Fatalf("a withdrawal inside the credit limit was refused: %v", err)
	}

	// Lowering the limit under the balance would leave the wallet somewhere no
	// withdrawal could have taken it, so the write is refused and nothing
	// changes.
	_, err := service.SetCredit(context.Background(), staff(), wallet.CreditRequest{
		WalletID: account.ID, Limit: "5.00",
	})
	if !errors.Is(err, wallet.ErrCreditBelowBalance) {
		t.Fatalf("lowering the limit under the balance returned %v, want ErrCreditBelowBalance", err)
	}
	if got := creditLimit(t, service, account.ID, "8.00").CreditLimit; got != 800 {
		t.Fatalf("lowering the limit to exactly the balance left %d, want 800", got)
	}

	// With the money back, the limit goes wherever it is put.
	deposit(t, service, account.ID, "repay", "8.00")
	if got := creditLimit(t, service, account.ID, "0").CreditLimit; got != 0 {
		t.Fatalf("removing the limit left %d, want 0", got)
	}
	if err := withdraw(t, service, staff(), account.ID, "spend-2", "0.01", false); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("a wallet with no credit limit allowed a withdrawal past zero: %v", err)
	}
}

func TestACreditLimitIsAMagnitudeAndNeverANegative(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	_, err := service.SetCredit(context.Background(), staff(), wallet.CreditRequest{
		WalletID: account.ID, Limit: "-10.00",
	})
	if !errors.Is(err, wallet.ErrCreditNegative) {
		t.Fatalf("a negative credit limit returned %v, want ErrCreditNegative", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the refused limit moved the balance to %d", got)
	}
}

func TestOnlyAnOperatorSetsACreditLimitAgainstTheDatabase(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	// The holder of the wallet, on their own wallet, which is the widest a
	// holder ever gets. Lending yourself money is not one of the things owning
	// the money allows.
	_, err := service.SetCredit(context.Background(), person("user-1"), wallet.CreditRequest{
		WalletID: account.ID, Limit: "10.00",
	})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the holder set their own credit limit: got %v, want ErrForbidden", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the refused request moved the balance to %d", got)
	}
	if err := withdraw(t, service, person("user-1"), account.ID, "spend-1", "0.01", false); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("the refused limit was applied anyway: %v", err)
	}
}

func TestForcingAMovementIsAuthorizedSeparatelyFromMakingIt(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	// The holder may take their own money out, and may not take out money that
	// is not there. The two are different decisions, and asking for the second
	// is refused rather than quietly answered as the first.
	if err := withdraw(t, service, person("user-1"), account.ID, "forced-1", "5.00", true); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the holder forced a withdrawal: got %v, want ErrForbidden", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the refused force moved the balance to %d", got)
	}

	// The same request without the field is the ordinary withdrawal, refused by
	// the money rather than by the policy.
	if err := withdraw(t, service, person("user-1"), account.ID, "plain-1", "5.00", false); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("an unfunded withdrawal returned %v, want ErrInsufficientFunds", err)
	}

	// An operator is answered, and the balance goes past a limit that is not
	// there at all.
	if err := withdraw(t, service, staff(), account.ID, "forced-2", "5.00", true); err != nil {
		t.Fatalf("an operator was refused a forced withdrawal: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != -500 {
		t.Fatalf("the forced withdrawal left %d, want -500", got)
	}
	if ledger := ledgerOf(t, service, account.ID); ledger != -500 {
		t.Fatalf("the ledger sums to %d and the balance column says -500", ledger)
	}
}

func TestAForcedWithdrawalStillCannotWrapTheColumn(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	// Whole units, so the largest amount a request can name is the largest the
	// column can hold.
	account := openIn(t, service, "user-1", "main", "XAU", 0)

	// Force lowers the floor to the smallest value the column holds and no
	// further. Two movements reach exactly that, and the third is refused --
	// by the same guard, which is what makes force a lower floor rather than
	// no floor at all.
	if err := withdraw(t, service, staff(), account.ID, "forced-1", "9223372036854775807", true); err != nil {
		t.Fatalf("a forced withdrawal of the whole range was refused: %v", err)
	}
	if err := withdraw(t, service, staff(), account.ID, "forced-2", "1", true); err != nil {
		t.Fatalf("a forced withdrawal reaching the end of the range was refused: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != -9223372036854775807-1 {
		t.Fatalf("the balance is %d, want the smallest an int64 holds", got)
	}
	if err := withdraw(t, service, staff(), account.ID, "forced-3", "1", true); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("a forced withdrawal past the range returned %v, want ErrInsufficientFunds", err)
	}
	if got := balanceOf(t, service, account.ID); got != -9223372036854775807-1 {
		t.Fatalf("the balance wrapped to %d", got)
	}
}
