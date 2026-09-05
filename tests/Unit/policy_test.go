package unit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/database/model"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The properties this package exists to keep are checked here, and they are
// checked against the code rather than described in a document:
//
//  1. an action nobody wrote a rule for is refused, to everybody;
//  2. the service authorizes before constructing or executing a Model query;
//  3. the tenant comes from the Grant;
//  4. nothing reaches the database without passing through the first two.
//
// The fourth is checked by the handle these tests pass in. It wraps a nil
// *sql.DB, so any statement that were issued would panic and fail the test
// loudly -- which makes "the refusal happened before the Model" a fact the
// suite proves rather than a comment. The structural twin in audit_test.go
// keeps that order visible on every service method, including an allowed path.
//
// What the rules do allow is proved in tests/Feature, against a real database:
// a policy is only as good as what it refuses when the money is really there.

// everyAction is the whole set the policy answers about. A test that listed
// ten of eleven would pass while the eleventh was open.
var everyAction = []security.Action{
	wallet.WalletView,
	wallet.WalletList,
	wallet.WalletCreate,
	wallet.WalletHistory,
	wallet.WalletDeposit,
	wallet.WalletWithdraw,
	wallet.WalletTransfer,
	wallet.WalletReverse,
	wallet.WalletConfirm,
	wallet.WalletCredit,
	wallet.WalletForce,
}

// operator is the most privileged subject this package knows: somebody the
// application trusts to move money that is not their own.
func operator() security.Subject {
	return security.Subject{ID: "staff-1", Tenant: "acme", Roles: []string{wallet.OperatorRole}, Verified: true}
}

// holder is the person whose money it is.
func holder() security.Subject {
	return security.Subject{ID: "user-1", Tenant: "acme", Verified: true}
}

// stranger is signed in, in the same tenant, and holds nothing here.
func stranger() security.Subject {
	return security.Subject{ID: "user-2", Tenant: "acme", Verified: true}
}

// theirWallet is a stored wallet belonging to holder().
func theirWallet() wallet.Wallet {
	return wallet.Wallet{ID: "wallet-1", TenantID: "acme", HolderID: "user-1", Slug: "main", Currency: "BRL", DecimalPlaces: 2}
}

func TestAnActionWithNoRuleIsDeniedToEverybody(t *testing.T) {
	t.Parallel()

	// Not one of the eight. The policy has to refuse it whoever is asking,
	// including the operator: an action that is allowed the moment somebody
	// names it is an action nobody decided about.
	unwritten := security.Action("wallet.confiscate")

	for _, subject := range []security.Subject{operator(), holder(), stranger()} {
		for _, record := range []wallet.Wallet{{}, theirWallet()} {
			_, err := security.Authorize(context.Background(), wallet.WalletPolicy{}, subject, unwritten, record)
			if !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("%s was allowed %s: got %v, want ErrForbidden", subject.ID, unwritten, err)
			}
		}
	}
}

func TestThePolicyDeniesAStrangerEveryActionOnSomebodyElsesWallet(t *testing.T) {
	t.Parallel()

	for _, action := range everyAction {
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()

			_, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
				stranger(), action, theirWallet())
			if !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("a stranger was allowed %s on another holder's wallet: got %v, want ErrForbidden", action, err)
			}
		})
	}
}

func TestOnlyAnOperatorMayReverse(t *testing.T) {
	t.Parallel()

	// The holder may move their own money and may not undo a movement that has
	// already settled. Letting the person who received a payment take it back
	// is the hole this separation exists to close.
	if _, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
		holder(), wallet.WalletReverse, theirWallet()); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the holder was allowed to reverse: got %v, want ErrForbidden", err)
	}
	if _, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
		holder(), wallet.WalletReverse, wallet.Wallet{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the holder passed the probe for a reversal: got %v, want ErrForbidden", err)
	}
	if _, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
		operator(), wallet.WalletReverse, theirWallet()); err != nil {
		t.Fatalf("an operator was refused a reversal: %v", err)
	}
}

func TestOnlyAnOperatorSetsACreditLimitOrIgnoresIt(t *testing.T) {
	t.Parallel()

	// A holder who could set their own limit could lend themselves money, and
	// one who could force a movement past it would not need to set it first.
	// Both are refused at the probe as well as on the row, so neither is
	// reachable by loading a wallet first.
	for _, action := range []security.Action{wallet.WalletCredit, wallet.WalletForce} {
		for _, record := range []wallet.Wallet{{}, theirWallet()} {
			if _, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
				holder(), action, record); !errors.Is(err, security.ErrForbidden) {
				t.Errorf("the holder was allowed %s: got %v, want ErrForbidden", action, err)
			}
		}
		if _, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
			operator(), action, theirWallet()); err != nil {
			t.Errorf("an operator was refused %s: %v", action, err)
		}
	}
}

func TestThePolicyDeniesARecordOfAnotherTenant(t *testing.T) {
	t.Parallel()

	theirs := wallet.Wallet{ID: "wallet-9", TenantID: "globex", HolderID: "staff-1"}

	// The subject is the operator, and is even the holder of the record by
	// identifier: the tenant check runs before every rule below it, so neither
	// helps.
	err := wallet.WalletPolicy{}.Can(context.Background(), operator(), wallet.WalletView, theirs)
	if err == nil {
		t.Fatal("the policy allowed a record belonging to another tenant")
	}
	if !strings.Contains(err.Error(), "another tenant") {
		t.Fatalf("the refusal did not name the tenant: %v", err)
	}
}

func TestThePolicyDeniesAGuest(t *testing.T) {
	t.Parallel()

	for _, action := range everyAction {
		for _, record := range []wallet.Wallet{{}, theirWallet()} {
			_, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
				security.Guest("acme"), action, record)
			if !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("a guest was allowed %s: got %v, want ErrForbidden", action, err)
			}
		}
	}
}

func TestAuthorizeRefusesASubjectThatIsNobody(t *testing.T) {
	t.Parallel()

	// The zero Subject is a session that failed to load, not an anonymous
	// reader, and it is refused before the policy is consulted. A package that
	// answered it as a guest would answer a broken session as a visitor.
	_, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
		security.Subject{}, wallet.WalletView, wallet.Wallet{})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("an empty subject was authorized: got %v, want ErrForbidden", err)
	}
}

func TestTheProbeIsNotTheDecision(t *testing.T) {
	t.Parallel()

	// The probe -- a wallet with neither an identifier nor a holder -- asks
	// whether this kind of thing is allowed at all, and the holder passes it.
	// It has to, or no read could ever load the row the real decision is made
	// about. What must not pass is the same subject against somebody else's
	// row.
	if _, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
		stranger(), wallet.WalletWithdraw, wallet.Wallet{}); err != nil {
		t.Fatalf("the probe refused a signed-in subject, so no wallet could ever be loaded to decide about: %v", err)
	}
	if _, err := security.Authorize(context.Background(), wallet.WalletPolicy{},
		stranger(), wallet.WalletWithdraw, theirWallet()); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the decision on the loaded row allowed a stranger to withdraw: got %v, want ErrForbidden", err)
	}
}

// nilHandle is a handle over no database.
//
// Any statement issued through it panics, which is what makes these tests
// prove that the refusal came first: a service that reached the Model before
// authorizing would crash here rather than pass.
func nilHandle() *data.DB { return data.Wrap(nil, data.DialectSQLite) }

func TestTheServiceRefusesBeforeReachingTheModel(t *testing.T) {
	t.Parallel()

	// A nil handle makes even construction of Wallets panic at
	// GetQueryGrammar. This catches moving the configured Model entry point --
	// not only its terminal -- ahead of authorization.
	//
	// The requests are valid ones, so that what is measured is the policy and
	// not the validator: an invalid request would be refused before anything
	// was authorized and would prove nothing about the order.
	service := wallet.NewWalletService(nil, nil)
	ctx := context.Background()
	actor := security.Guest("acme")

	refusals := map[string]func() error{
		"Find": func() error {
			_, err := service.Find(ctx, actor, "wallet-1")
			return err
		},
		"List": func() error {
			_, err := service.List(ctx, actor, wallet.ListRequest{})
			return err
		},
		"Open": func() error {
			_, err := service.Open(ctx, actor, wallet.OpenRequest{
				HolderID: "user-1", Slug: "main", Name: "Main", Currency: "BRL", DecimalPlaces: 2})
			return err
		},
		"History": func() error {
			_, err := service.History(ctx, actor, wallet.HistoryRequest{WalletID: "wallet-1"})
			return err
		},
		"Deposit": func() error {
			_, err := service.Deposit(ctx, actor, wallet.DepositRequest{
				IdempotencyKey: "key-1", WalletID: "wallet-1", Amount: "1.00"})
			return err
		},
		"Withdraw": func() error {
			_, err := service.Withdraw(ctx, actor, wallet.WithdrawRequest{
				IdempotencyKey: "key-1", WalletID: "wallet-1", Amount: "1.00"})
			return err
		},
		"Transfer": func() error {
			_, err := service.Transfer(ctx, actor, wallet.TransferRequest{
				IdempotencyKey: "key-1", FromWalletID: "wallet-1", ToWalletID: "wallet-2", Amount: "1.00"})
			return err
		},
		"Reverse": func() error {
			_, err := service.Reverse(ctx, actor, wallet.ReverseRequest{
				IdempotencyKey: "key-1", OperationID: "operation-1", Reason: "asked"})
			return err
		},
		"Confirm": func() error {
			_, err := service.Confirm(ctx, actor, wallet.ConfirmRequest{
				IdempotencyKey: "key-1", OperationID: "operation-1"})
			return err
		},
		"SetCredit": func() error {
			_, err := service.SetCredit(ctx, actor, wallet.CreditRequest{
				WalletID: "wallet-1", Limit: "10.00"})
			return err
		},
	}

	for name, call := range refusals {
		if err := call(); !errors.Is(err, security.ErrForbidden) {
			t.Errorf("%s reached the Model before the policy refusal: %v", name, err)
		}
	}
}

func TestEveryModelIsWiredAndTenantScoped(t *testing.T) {
	t.Parallel()

	handle := nilHandle()
	for table, rows := range map[string]struct {
		key      string
		keyType  string
		incr     bool
		tenant   string
		entityOK bool
	}{
		"wallets": {
			key: wallet.Wallets(handle).GetTable(), keyType: wallet.Wallets(handle).KeyType,
			incr: wallet.Wallets(handle).Incrementing, tenant: wallet.Wallets(handle).TenantColumn,
			entityOK: model.ModelOf(wallet.Wallets(handle).Entity) != nil,
		},
		"wallet_operations": {
			key: wallet.Operations(handle).GetTable(), keyType: wallet.Operations(handle).KeyType,
			incr: wallet.Operations(handle).Incrementing, tenant: wallet.Operations(handle).TenantColumn,
			entityOK: model.ModelOf(wallet.Operations(handle).Entity) != nil,
		},
		"wallet_entries": {
			key: wallet.Entries(handle).GetTable(), keyType: wallet.Entries(handle).KeyType,
			incr: wallet.Entries(handle).Incrementing, tenant: wallet.Entries(handle).TenantColumn,
			entityOK: model.ModelOf(wallet.Entries(handle).Entity) != nil,
		},
	} {
		if rows.key != table {
			t.Errorf("a model answers for table %q, want %q", rows.key, table)
		}
		if rows.keyType != "string" || rows.incr {
			t.Errorf("%s has key type %q, incrementing %t; want application-generated text", table, rows.keyType, rows.incr)
		}
		if rows.tenant != "tenant_id" {
			t.Errorf("%s has tenant column %q, want tenant_id", table, rows.tenant)
		}
		if !rows.entityOK {
			t.Errorf("%s returned an entity whose embedded Model is not wired to it", table)
		}
	}
}

func TestASystemGrantWithoutATenantReachesNothing(t *testing.T) {
	t.Parallel()

	// A system grant with no tenant names no customer. The Model refuses it
	// while preparing the query, before the nil handle can issue a statement.
	_, err := wallet.Wallets(nilHandle()).NewQuery().WhereKey("wallet-1").First(
		context.Background(), security.SystemGrant(wallet.WalletView, ""))
	if !errors.Is(err, model.ErrNoTenant) {
		t.Fatalf("a system grant with no tenant returned %v, want ErrNoTenant", err)
	}
}

func TestTheTenantComesFromTheGrant(t *testing.T) {
	t.Parallel()

	g := security.SystemGrant(wallet.WalletView, "acme")
	if got := data.Tenant(g); got != "acme" {
		t.Fatalf("data.Tenant(g) = %q, want %q", got, "acme")
	}

	// And a Grant nobody issued carries no tenant at all, so a statement that
	// took its tenant from anywhere else would be reading rows this Grant does
	// not name.
	if got := data.Tenant(security.Grant{}); got != "" {
		t.Fatalf("the zero Grant carries the tenant %q, want none", got)
	}
}

func TestEveryRequestValidatesItsInput(t *testing.T) {
	t.Parallel()

	if errs := (wallet.OpenRequest{}).Validate(); !errs.Any() {
		t.Error("an empty OpenRequest validated")
	}
	if errs := (wallet.OpenRequest{HolderID: "user-1", Slug: "main", Name: "Main", Currency: "BRL", DecimalPlaces: 10}).Validate(); !errs.Any() {
		t.Error("a scale past the maximum validated")
	}
	if errs := (wallet.OpenRequest{HolderID: "user-1", Slug: "main", Name: "Main", Currency: "BRL", DecimalPlaces: 2}).Validate(); errs.Any() {
		t.Errorf("a valid OpenRequest was rejected: %v", errs)
	}

	// Every movement of money needs a key from the caller. Without one there is
	// no way to tell a retry from a second payment, so the request is refused
	// rather than treated as a first attempt.
	if errs := (wallet.DepositRequest{WalletID: "wallet-1", Amount: "1.00"}).Validate(); !errs.Any() {
		t.Error("a deposit with no idempotency key validated")
	}
	if errs := (wallet.WithdrawRequest{WalletID: "wallet-1", Amount: "1.00"}).Validate(); !errs.Any() {
		t.Error("a withdrawal with no idempotency key validated")
	}
	if errs := (wallet.TransferRequest{FromWalletID: "wallet-1", ToWalletID: "wallet-2", Amount: "1.00"}).Validate(); !errs.Any() {
		t.Error("a transfer with no idempotency key validated")
	}
	if errs := (wallet.TransferRequest{IdempotencyKey: "key-1", FromWalletID: "wallet-1", Amount: "1.00"}).Validate(); !errs.Any() {
		t.Error("a transfer with no destination validated")
	}
	if errs := (wallet.ReverseRequest{IdempotencyKey: "key-1", OperationID: "operation-1"}).Validate(); !errs.Any() {
		t.Error("a reversal with no reason validated")
	}
	if errs := (wallet.ReverseRequest{IdempotencyKey: "key-1", OperationID: "operation-1", Reason: "chargeback"}).Validate(); errs.Any() {
		t.Errorf("a valid ReverseRequest was rejected: %v", errs)
	}
}

func TestTheConfigurationRefusesWhatCannotWork(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]wallet.Config{
		"no tenant":        {},
		"tenant with a /":  {Tenant: "acme/reports"},
		"tenant uppercase": {Tenant: "Acme"},
		"relative prefix":  {Tenant: "acme", Prefix: "wallet"},
		"page size too big": {Tenant: "acme",
			PageSize: wallet.MaxPageSize + 1},
		"negative page size": {Tenant: "acme", PageSize: -1},
	} {
		if err := cfg.Validate(); err == nil {
			t.Errorf("the configuration with %s was accepted", name)
		}
	}

	if err := (wallet.Config{Tenant: "acme"}).Validate(); err != nil {
		t.Fatalf("a valid configuration was refused: %v", err)
	}
}
