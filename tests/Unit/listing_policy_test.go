package unit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// Who reads the operations of one tenant, and who reads every tenant's.

func TestOnlyAPlatformOperatorReadsEveryTenant(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	platform := security.Subject{ID: "root", Tenant: "platform", Roles: []string{wallet.PlatformRole}, Verified: true}
	granted := security.Subject{ID: "root", Tenant: "platform", Actions: []security.Action{wallet.WalletOperationsAll}, Verified: true}
	for name, subject := range map[string]security.Subject{"by role": platform, "by action": granted} {
		if _, err := security.Authorize(ctx, wallet.WalletPolicy{}, subject, wallet.WalletOperationsAll, wallet.Wallet{}); err != nil {
			t.Errorf("a platform operator %s was refused: %v", name, err)
		}
		// The probe only: it is never a question about one tenant's wallet.
		if _, err := security.Authorize(ctx, wallet.WalletPolicy{}, subject, wallet.WalletOperationsAll, theirWallet()); !errors.Is(err, security.ErrForbidden) {
			t.Errorf("a platform operator %s was allowed it on a wallet: %v", name, err)
		}
		// And it makes nobody an operator, even in its own tenant: somebody
		// else's wallet there is refused it everything.
		somebodys := wallet.Wallet{ID: "wallet-9", TenantID: "platform", HolderID: "user-9", Currency: "BRL", DecimalPlaces: 2}
		for _, action := range everyAction {
			if _, err := security.Authorize(ctx, wallet.WalletPolicy{}, subject, action, somebodys); !errors.Is(err, security.ErrForbidden) {
				t.Errorf("a platform operator %s was allowed %s", name, action)
			}
		}
	}

	impersonating := platform
	impersonating.Impersonator = &security.Subject{ID: "root", Tenant: "platform", Roles: []string{wallet.PlatformRole}}
	for name, subject := range map[string]security.Subject{
		"the operator":                   operator(),
		"an operator by action":          {ID: "finance-1", Tenant: "acme", Actions: []security.Action{wallet.OperatorAction}},
		"the holder":                     holder(),
		"a guest":                        security.Guest("acme"),
		"inside somebody else's account": impersonating,
	} {
		if _, err := security.Authorize(ctx, wallet.WalletPolicy{}, subject, wallet.WalletOperationsAll, wallet.Wallet{}); !errors.Is(err, security.ErrForbidden) {
			t.Errorf("%s read every tenant: %v", name, err)
		}
	}
}

func TestTheOperatorIsAnActionAsWellAsARole(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	granted := security.Subject{ID: "finance-1", Tenant: "acme", Actions: []security.Action{wallet.OperatorAction}, Verified: true}
	if !wallet.IsOperator(granted) || !wallet.IsOperator(operator()) || wallet.IsOperator(holder()) {
		t.Fatal("IsOperator does not answer by the role and by the action alike")
	}
	if _, err := security.Authorize(ctx, wallet.WalletPolicy{}, granted, wallet.WalletReverse, theirWallet()); err != nil {
		t.Fatalf("an operator by action was refused a reversal: %v", err)
	}
	other := theirWallet()
	other.TenantID = "globex"
	if _, err := security.Authorize(ctx, wallet.WalletPolicy{}, granted, wallet.WalletView, other); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("an operator by action reached another tenant: %v", err)
	}
	if _, err := security.Authorize(ctx, wallet.WalletPolicy{}, operator(), wallet.OperatorAction, wallet.Wallet{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("OperatorAction was answered as an action of its own: %v", err)
	}
}

func TestADataTableAsksForAnOrderThePackageKnows(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		column, direction string
		want              wallet.OperationSort
		ok                bool
	}{
		{"created_at", "desc", wallet.OperationsNewest, true},
		{"created_at", "asc", wallet.OperationsOldest, true},
		{"amount", "DESC", wallet.OperationsLargest, true},
		{"amount", "asc", wallet.OperationsSmallest, true},
		{"", "", wallet.OperationsNewest, true},
		{"balance_after", "asc", wallet.OperationsNewest, false},
		{"amount", "sideways", wallet.OperationsNewest, false},
	} {
		got, ok := wallet.OperationSortFor(c.column, c.direction)
		if got != c.want || ok != c.ok {
			t.Errorf("OperationSortFor(%q, %q) = %q, %v; want %q, %v", c.column, c.direction, got, ok, c.want, c.ok)
		}
	}
}
