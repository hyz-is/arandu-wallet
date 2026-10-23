package wallet

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/auth"
)

// TestTheBalanceStatementRefusesAGrantThatCannotWrite holds the one write in
// this package that the query builder does not make: the guarded balance
// statement, which move composes by hand.
//
// The service is built over a handle with no database behind it, so a
// statement that ran would panic; each refusal below is therefore proof that
// the check came before the statement, and not a rollback after it.
func TestTheBalanceStatementRefusesAGrantThatCannotWrite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewWalletService(data.Wrap(nil, data.DialectSQLite), nil, nil, nil)
	staff := security.Subject{ID: "staff-1", Tenant: "acme", Roles: []string{OperatorRole}, Verified: true}

	viewing, err := security.Authorize(ctx, WalletPolicy{}, staff, WalletView, Wallet{})
	if err != nil {
		t.Fatalf("authorizing a view: %v", err)
	}
	if _, err := s.move(ctx, viewing, "operation-1", 0, movement{wallet: aWallet(), kind: EntryDeposit, amount: 100}); !errors.Is(err, auth.ErrReadGrant) {
		t.Errorf("a deposit moved under a Grant issued to view answered %v, want auth.ErrReadGrant", err)
	}

	owner := security.Subject{ID: "user-1", Tenant: "acme", Verified: true}
	watched, err := auth.Impersonate(staff, owner)
	if err != nil {
		t.Fatalf("viewing as the holder: %v", err)
	}
	asOwner, err := security.Authorize(ctx, WalletPolicy{}, watched, WalletDeposit, Wallet{})
	if err != nil {
		t.Fatalf("authorizing a deposit for the account being viewed: %v", err)
	}
	if _, err := s.move(ctx, asOwner, "operation-2", 0, movement{wallet: aWallet(), kind: EntryDeposit, amount: 100}); !errors.Is(err, auth.ErrReadOnly) {
		t.Errorf("a deposit moved by somebody only viewing the account answered %v, want auth.ErrReadOnly", err)
	}
}
