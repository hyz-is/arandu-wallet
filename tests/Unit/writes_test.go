package unit_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/auth"

	wallet "github.com/hyz-is/arandu-wallet"
)

// writes is the decision, per action, of whether a Grant issued for it may
// change anything. It is written out rather than derived, so that a change to
// it is a line somebody reads in review: moving an action from false to true
// is letting a Grant that was issued to look at money move it.
var writes = map[security.Action]bool{
	wallet.WalletView:               false,
	wallet.WalletList:               false,
	wallet.WalletHistory:            false,
	wallet.WalletPurchases:          false,
	wallet.WalletCreditView:         false,
	wallet.WalletOperationsAll:      false,
	wallet.OperatorAction:           false,
	wallet.WalletCreate:             true,
	wallet.WalletDeposit:            true,
	wallet.WalletWithdraw:           true,
	wallet.WalletTransfer:           true,
	wallet.WalletReverse:            true,
	wallet.WalletConfirm:            true,
	wallet.WalletVoid:               true,
	wallet.WalletCredit:             true,
	wallet.WalletForce:              true,
	wallet.WalletPay:                true,
	wallet.WalletRefund:             true,
	wallet.WalletDescribe:           true,
	wallet.WalletClose:              true,
	wallet.WalletReconcile:          true,
	wallet.WalletHold:               true,
	wallet.WalletCapture:            true,
	wallet.WalletRelease:            true,
	wallet.WalletExpire:             true,
	wallet.WalletWithdrawalsRequest: true,
	wallet.WalletWithdrawalsManage:  true,
	wallet.WalletCreditOpen:         true,
	wallet.WalletCreditManage:       true,
}

// TestEveryActionIsDecidedReadOrWrite reads the actions policy.go declares and
// requires each one in the table above, so an action added to the policy
// without a decision about whether its Grant writes fails here instead of
// being decided by whatever Writes happens to answer for a name it never saw.
func TestEveryActionIsDecidedReadOrWrite(t *testing.T) {
	t.Parallel()

	declared := declaredActions(t)
	for value := range declared {
		if _, decided := writes[security.Action(value)]; !decided {
			t.Errorf("policy.go declares %q and this test does not say whether its Grant writes", value)
		}
	}
	for action := range writes {
		if !declared[string(action)] {
			t.Errorf("the table decides %q, which policy.go no longer declares", action)
		}
	}
}

// TestWritesAnswersTheDecision holds WalletPolicy.Writes to the table.
func TestWritesAnswersTheDecision(t *testing.T) {
	t.Parallel()

	for action, want := range writes {
		if got := (wallet.WalletPolicy{}).Writes(action); got != want {
			t.Errorf("Writes(%s) = %v, want %v", action, got, want)
		}
	}
}

// TestAGrantToReadCannotWrite is the decision as Authorize applies it: the
// operator, who may do everything in the tenant, is issued a Grant to view
// and a Grant to read the ledger that the database refuses on every write,
// and a Grant to deposit it accepts.
func TestAGrantToReadCannotWrite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, action := range []security.Action{wallet.WalletView, wallet.WalletList, wallet.WalletHistory, wallet.WalletPurchases, wallet.WalletCreditView} {
		g, err := security.Authorize(ctx, wallet.WalletPolicy{}, operator(), action, wallet.Wallet{})
		if err != nil {
			t.Fatalf("authorizing %s for the operator: %v", action, err)
		}
		if err := auth.Writable(g); !errors.Is(err, auth.ErrReadGrant) {
			t.Errorf("a Grant issued for %s answers Writable with %v, want auth.ErrReadGrant", action, err)
		}
	}
	g, err := security.Authorize(ctx, wallet.WalletPolicy{}, operator(), wallet.WalletDeposit, wallet.Wallet{})
	if err != nil {
		t.Fatalf("authorizing a deposit for the operator: %v", err)
	}
	if err := auth.Writable(g); err != nil {
		t.Errorf("a Grant issued for a deposit is refused on writes: %v", err)
	}
}

// declaredActions answers the value of every constant of type security.Action
// in policy.go.
func declaredActions(t *testing.T) map[string]bool {
	t.Helper()

	path := filepath.Join(packageRoot(t), "policy.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	found := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || !isActionType(vs.Type) {
				continue
			}
			for _, value := range vs.Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("reading %s: %v", lit.Value, err)
				}
				found[text] = true
			}
		}
	}
	if len(found) == 0 {
		t.Fatal("policy.go declares no security.Action constant, so this test read the wrong file")
	}
	return found
}

func isActionType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "security" && sel.Sel.Name == "Action"
}
