package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/test"
	"github.com/arandu-io/hesape/auth"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The two Organizations the kit asks the application for accounts in. They are
// names for Account to read, not tenants: what tenant each one is, is what the
// subject the application answers says.
const (
	// OrganizationA is the Organization whose wallets every check opens.
	OrganizationA = "a"
	// OrganizationB is the other one, whose operator reaches for them.
	OrganizationB = "b"
)

// Fixture is the application's wallet, built by its own composition over one
// fresh database with its migrations run. Wallet asks for a new one in every
// subtest.
type Fixture struct {
	// Service is the WalletService of the Module the application's
	// composition registers: module.Service() of what wallet.New returned in
	// its bootstrap.
	Service *wallet.WalletService

	// DB is the database Service writes, as the application opened it.
	DB *data.DB

	// Account creates an account of the application in the Organization
	// named organization -- OrganizationA or OrganizationB, creating the
	// Organization first where the application keeps them -- and answers the
	// subject its SubjectResolver answers for that account.
	//
	// operator asks for an account that runs the Organization's money: one
	// granted wallet.OperatorRole, or the action wallet.OperatorAction through
	// arandu-permission, the way the application grants either. false asks for
	// a plain account, granted nothing, which is the holder of a wallet.
	//
	// The account must exist where the resolver reads it, because the kit
	// signs it in through the server with framework/test As when Server is
	// set, and the subject the server reads is the resolver's answer.
	Account func(t *testing.T, organization string, operator bool) security.Subject

	// Server is the application framework/test Serve booted over DB, with the
	// wallet's routes registered and the module built over the session store
	// its guards read. Nil when the application does not register the
	// wallet's routes; the checks over HTTP are then not run.
	Server *test.Server
}

// Wallet runs every check against what open builds, calling open once per
// subtest. Each failure says what the application broke, in a sentence.
func Wallet(t *testing.T, open func(t *testing.T) Fixture) {
	t.Helper()
	if open == nil {
		t.Fatal("conformance.Wallet was handed a nil fixture function")
	}
	t.Run("TheLedgerIsAppendOnly", func(t *testing.T) { appendOnly(t, prepare(t, open(t))) })
	t.Run("EveryBalanceIsTheSumOfItsEntries", func(t *testing.T) { balanced(t, prepare(t, open(t))) })
	t.Run("OneIdempotencyKeyIsOneOperation", func(t *testing.T) { idempotent(t, prepare(t, open(t))) })
	t.Run("AHoldSettlesExactlyOnce", func(t *testing.T) { holds(t, prepare(t, open(t))) })
	t.Run("AnotherOrganizationReachesNoWallet", func(t *testing.T) { isolated(t, prepare(t, open(t))) })
	t.Run("AGrantToReadCannotWrite", func(t *testing.T) { readOnly(t, prepare(t, open(t))) })
}

// kit is one fixture and the three accounts every check acts as.
type kit struct {
	Fixture
	ctx       context.Context
	operatorA security.Subject
	holderA   security.Subject
	operatorB security.Subject
}

// prepare checks the fixture and asks it for the accounts, refusing a wiring
// under which no check below could mean anything.
func prepare(t *testing.T, f Fixture) *kit {
	t.Helper()
	switch {
	case f.Service == nil:
		t.Fatal("the fixture has no Service: hand the kit module.Service() of the wallet Module the " +
			"application's composition built")
	case f.DB == nil:
		t.Fatal("the fixture has no DB: hand the kit the database the wallet Service writes")
	case f.Account == nil:
		t.Fatal("the fixture has no Account: the kit needs the application to create its accounts " +
			"and answer the subjects its SubjectResolver reads for them")
	}

	k := &kit{Fixture: f, ctx: t.Context()}
	k.operatorA = f.Account(t, OrganizationA, true)
	k.holderA = f.Account(t, OrganizationA, false)
	k.operatorB = f.Account(t, OrganizationB, true)

	for _, s := range []security.Subject{k.operatorA, k.holderA, k.operatorB} {
		if s.ID == "" || s.Tenant == "" || s.IsGuest() {
			t.Fatalf("Account answered a subject with ID %q and tenant %q: an account the resolver "+
				"answers names itself and the Organization it belongs to", s.ID, s.Tenant)
		}
	}
	if !wallet.IsOperator(k.operatorA) || !wallet.IsOperator(k.operatorB) {
		t.Fatalf("an account made to run its Organization's money is not the wallet's operator "+
			"(roles %v, actions %v): the application must grant it %s, or the action %s, where its "+
			"SubjectResolver reads them", k.operatorA.Roles, k.operatorA.Actions,
			wallet.OperatorRole, wallet.OperatorAction)
	}
	if wallet.IsOperator(k.holderA) {
		t.Fatalf("a plain account of the application is the wallet's operator (roles %v, actions %v): "+
			"its SubjectResolver hands an account a reach it was never granted, so every holder runs "+
			"every wallet of the Organization", k.holderA.Roles, k.holderA.Actions)
	}
	if k.holderA.ID == k.operatorA.ID {
		t.Fatalf("Account answered the same account %q for the operator and the holder: each call "+
			"creates an account of its own", k.holderA.ID)
	}
	if k.holderA.Tenant != k.operatorA.Tenant {
		t.Fatalf("two accounts of one Organization answered the tenants %q and %q: the subject's "+
			"tenant has to be the Organization, the same for everybody in it",
			k.operatorA.Tenant, k.holderA.Tenant)
	}
	if k.operatorB.Tenant == k.operatorA.Tenant {
		t.Fatalf("the accounts of two Organizations answered the same tenant %q: the application's "+
			"subject does not carry its Organization as the tenant, so nothing can tell two "+
			"customers' money apart", k.operatorA.Tenant)
	}
	return k
}

// open opens a wallet in BRL at two decimal places, as actor, for holder.
func (k *kit) open(t *testing.T, actor security.Subject, holder, slug string) *wallet.Wallet {
	t.Helper()
	w, err := k.Service.Open(k.ctx, actor, wallet.OpenRequest{
		HolderID: holder, Slug: slug, Name: "Conformance " + slug, Currency: "BRL", DecimalPlaces: 2,
	})
	if err != nil {
		t.Fatalf("the operator of %s could not open a wallet for %s: %v", actor.Tenant, holder, err)
	}
	return w
}

// find reads a wallet as the operator of its own Organization.
func (k *kit) find(t *testing.T, id string) *wallet.Wallet {
	t.Helper()
	w, err := k.Service.Find(k.ctx, k.operatorA, id)
	if err != nil {
		t.Fatalf("the operator could not read wallet %s of its own Organization: %v", id, err)
	}
	return w
}

// must fails the test when a movement the kit sets up is refused: the check
// that follows would be about a wallet that is not in the state it says.
func must[T any](t *testing.T, what string, do func() (T, error)) T {
	t.Helper()
	value, err := do()
	if err != nil {
		t.Fatalf("%s was refused: %v", what, err)
	}
	return value
}

// row is what an entry says, compared field by field between two readings.
type row struct {
	ID, OperationID string
	Kind            wallet.EntryKind
	Sequence        int64
	Position        int
	Amount          wallet.Amount
	BalanceAfter    wallet.Amount
	Settled         bool
	CreatedAt       string
}

func (r row) String() string {
	return fmt.Sprintf("%s #%d of operation %s: %s %d, balance after %d, settled %v, written %s",
		r.ID, r.Sequence, r.OperationID, r.Kind, r.Amount, r.BalanceAfter, r.Settled, r.CreatedAt)
}

// ledger reads every entry of a wallet, oldest first, through the Service.
func (k *kit) ledger(t *testing.T, id string) []row {
	t.Helper()
	const page = 200
	var rows []row
	cursor := ""
	for {
		statement, err := k.Service.History(k.ctx, k.operatorA, wallet.HistoryRequest{
			WalletID: id, Query: data.Query{Cursor: cursor, Limit: page},
		})
		if err != nil {
			t.Fatalf("the operator could not read the ledger of wallet %s: %v", id, err)
		}
		for _, e := range statement.Entries {
			rows = append(rows, row{
				ID: e.ID, OperationID: e.OperationID, Kind: e.Kind, Sequence: e.Sequence,
				Position: e.Position, Amount: e.Amount, BalanceAfter: e.BalanceAfter,
				Settled: bool(e.Settled), CreatedAt: e.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000"),
			})
		}
		if len(statement.Entries) < page {
			return rows
		}
		cursor = statement.Entries[len(statement.Entries)-1].ID
	}
}

// appendOnly runs every kind of movement that undoes or settles another and
// requires each to leave every entry already written exactly as it was.
func appendOnly(t *testing.T, k *kit) {
	payer := k.open(t, k.operatorA, k.holderA.ID, "ledger")
	payee := k.open(t, k.operatorA, "conformance-payee", "ledger")
	svc, op := k.Service, k.operatorA

	var deposited, withdrawn, proposed, doomed wallet.Receipt
	steps := []struct {
		name string
		do   func() error
	}{
		{"a deposit", func() (err error) {
			deposited, err = svc.Deposit(k.ctx, op, wallet.DepositRequest{IdempotencyKey: "ledger-1", WalletID: payer.ID, Amount: "100.00"})
			return err
		}},
		{"a withdrawal", func() (err error) {
			withdrawn, err = svc.Withdraw(k.ctx, op, wallet.WithdrawRequest{IdempotencyKey: "ledger-2", WalletID: payer.ID, Amount: "30.00"})
			return err
		}},
		{"a transfer", func() error {
			_, err := svc.Transfer(k.ctx, op, wallet.TransferRequest{IdempotencyKey: "ledger-3", FromWalletID: payer.ID, ToWalletID: payee.ID, Amount: "20.00"})
			return err
		}},
		{"a pending deposit", func() (err error) {
			proposed, err = svc.Deposit(k.ctx, op, wallet.DepositRequest{IdempotencyKey: "ledger-4", WalletID: payer.ID, Amount: "7.00", Pending: true})
			return err
		}},
		{"the confirmation of the pending deposit", func() error {
			_, err := svc.Confirm(k.ctx, op, wallet.ConfirmRequest{IdempotencyKey: "ledger-5", OperationID: proposed.Operation.ID})
			return err
		}},
		{"a second pending deposit", func() (err error) {
			doomed, err = svc.Deposit(k.ctx, op, wallet.DepositRequest{IdempotencyKey: "ledger-6", WalletID: payer.ID, Amount: "9.00", Pending: true})
			return err
		}},
		{"the void of the second pending deposit", func() error {
			_, err := svc.Void(k.ctx, op, wallet.VoidRequest{IdempotencyKey: "ledger-7", OperationID: doomed.Operation.ID, Reason: "conformance"})
			return err
		}},
		{"a partial reversal of the deposit", func() error {
			_, err := svc.ReversePartially(k.ctx, op, wallet.PartialReversalRequest{IdempotencyKey: "ledger-8", OperationID: deposited.Operation.ID, Amount: "5.00", Reason: "conformance"})
			return err
		}},
		{"the reversal of the withdrawal", func() error {
			_, err := svc.Reverse(k.ctx, op, wallet.ReverseRequest{IdempotencyKey: "ledger-9", OperationID: withdrawn.Operation.ID, Reason: "conformance"})
			return err
		}},
	}

	before := map[string][]row{payer.ID: nil, payee.ID: nil}
	for _, step := range steps {
		if err := step.do(); err != nil {
			t.Fatalf("%s was refused: %v", step.name, err)
		}
		grew := false
		for id, old := range before {
			now := k.ledger(t, id)
			if len(now) < len(old) {
				t.Fatalf("after %s wallet %s has %d entries where it had %d: the ledger is append-only, "+
					"and something the application wired deleted rows it had written", step.name, id, len(now), len(old))
			}
			for i := range old {
				if now[i] != old[i] {
					t.Fatalf("after %s the entry at position %d of wallet %s was\n\t%s\nand is\n\t%s\n"+
						"the ledger is append-only: an undo is a new entry, and something the application "+
						"wired rewrote one already written", step.name, i, id, old[i], now[i])
				}
			}
			if len(now) > len(old) {
				grew = true
			}
			before[id] = now
		}
		if !grew {
			t.Fatalf("%s was accepted and appended no entry: what it did was written over the ledger "+
				"instead of beside it", step.name)
		}
	}
}

// balanced moves money every way a balance moves and requires each wallet's
// balance to be what its entries add up to.
func balanced(t *testing.T, k *kit) {
	a := k.open(t, k.operatorA, k.holderA.ID, "sum")
	b := k.open(t, k.operatorA, "conformance-payee", "sum")
	svc, op := k.Service, k.operatorA

	dep := must(t, "a deposit", func() (wallet.Receipt, error) {
		return svc.Deposit(k.ctx, op, wallet.DepositRequest{IdempotencyKey: "sum-1", WalletID: a.ID, Amount: "250.00"})
	})
	must(t, "a withdrawal", func() (wallet.Receipt, error) {
		return svc.Withdraw(k.ctx, op, wallet.WithdrawRequest{IdempotencyKey: "sum-2", WalletID: a.ID, Amount: "40.10"})
	})
	must(t, "a transfer", func() (wallet.Receipt, error) {
		return svc.Transfer(k.ctx, op, wallet.TransferRequest{IdempotencyKey: "sum-3", FromWalletID: a.ID, ToWalletID: b.ID, Amount: "60.25"})
	})
	must(t, "a pending deposit", func() (wallet.Receipt, error) {
		return svc.Deposit(k.ctx, op, wallet.DepositRequest{IdempotencyKey: "sum-4", WalletID: a.ID, Amount: "13.00", Pending: true})
	})
	h := must(t, "a hold", func() (*wallet.Hold, error) {
		return svc.Hold(k.ctx, op, wallet.HoldRequest{IdempotencyKey: "sum-5", WalletID: a.ID, Amount: "20.00"})
	})
	must(t, "a capture of part of the hold", func() (wallet.Receipt, error) {
		return svc.Capture(k.ctx, op, wallet.CaptureRequest{IdempotencyKey: "sum-6", HoldID: h.ID, Amount: "12.50"})
	})
	must(t, "a partial reversal", func() (wallet.Receipt, error) {
		return svc.ReversePartially(k.ctx, op, wallet.PartialReversalRequest{IdempotencyKey: "sum-7", OperationID: dep.Operation.ID, Amount: "0.15", Reason: "conformance"})
	})

	for _, id := range []string{a.ID, b.ID} {
		w := k.find(t, id)
		entries := k.ledger(t, id)
		var sum wallet.Amount
		for _, e := range entries {
			if e.Settled {
				signed := e.Amount
				if e.Kind == wallet.EntryWithdraw {
					signed = -signed
				}
				sum += signed
			}
		}
		if sum != w.Balance {
			t.Errorf("wallet %s holds %d and its settled entries add up to %d: the balance moved without "+
				"the ledger, which is a balance nothing can explain", id, w.Balance, sum)
		}
		if n := len(entries); n == 0 || entries[n-1].BalanceAfter != w.Balance {
			t.Errorf("wallet %s holds %d and its newest entry records %v: the entry and the balance were "+
				"not written by the same statement", id, w.Balance, entries)
		}
		report, err := svc.Reconcile(k.ctx, op, id)
		if err != nil {
			t.Fatalf("the operator could not reconcile wallet %s: %v", id, err)
		}
		if !report.Balanced() || report.Frozen {
			t.Errorf("Reconcile found wallet %s off its ledger by %d and froze it: the balance and the "+
				"entries disagree on the application's database", id, report.Difference())
		}
	}
}

// idempotent sends one request again, concurrently, and under another shape,
// and requires one operation and one movement of money.
func idempotent(t *testing.T, k *kit) {
	a := k.open(t, k.operatorA, k.holderA.ID, "keys")
	other := k.open(t, k.operatorA, "conformance-other", "keys")
	svc, op := k.Service, k.operatorA

	request := wallet.DepositRequest{IdempotencyKey: "same-key", WalletID: a.ID, Amount: "10.00"}
	first := must(t, "a deposit", func() (wallet.Receipt, error) { return svc.Deposit(k.ctx, op, request) })
	again, err := svc.Deposit(k.ctx, op, request)
	if err != nil {
		t.Fatalf("the same deposit sent again under its key was refused (%v): a client that retries after a "+
			"timeout is told its money did not move when it did", err)
	}
	if !again.Replayed || again.Operation.ID != first.Operation.ID {
		t.Fatalf("the same deposit sent again under its key answered operation %s (replayed %v), want the "+
			"first receipt, operation %s", again.Operation.ID, again.Replayed, first.Operation.ID)
	}
	if got := k.find(t, a.ID).Balance; got != 1000 {
		t.Fatalf("the same deposit of 10.00 sent twice under one key left a balance of %d minor units, "+
			"want 1000: the key moved the money twice", got)
	}
	found, err := svc.OperationByKey(k.ctx, op, request.IdempotencyKey)
	if err != nil || found.Operation.ID != first.Operation.ID {
		t.Fatalf("OperationByKey(%q) answered operation %s (%v), want %s", request.IdempotencyKey,
			found.Operation.ID, err, first.Operation.ID)
	}

	conflicts := []struct {
		name string
		do   func() error
	}{
		{"another amount", func() error {
			_, err := svc.Deposit(k.ctx, op, wallet.DepositRequest{IdempotencyKey: "same-key", WalletID: a.ID, Amount: "11.00"})
			return err
		}},
		{"another wallet", func() error {
			_, err := svc.Deposit(k.ctx, op, wallet.DepositRequest{IdempotencyKey: "same-key", WalletID: other.ID, Amount: "10.00"})
			return err
		}},
		{"another kind of movement", func() error {
			_, err := svc.Withdraw(k.ctx, op, wallet.WithdrawRequest{IdempotencyKey: "same-key", WalletID: a.ID, Amount: "10.00"})
			return err
		}},
	}
	for _, c := range conflicts {
		if err := c.do(); !errors.Is(err, wallet.ErrOperationConflict) {
			t.Errorf("the key of a deposit sent again naming %s answered %v, want wallet.ErrOperationConflict: "+
				"a key reused for another request was taken for the first one", c.name, err)
		}
	}
	if got, want := k.find(t, a.ID).Balance, wallet.Amount(1000); got != want {
		t.Errorf("after the key was reused for other requests wallet %s holds %d, want %d: a refused "+
			"request moved money", a.ID, got, want)
	}
	if got := k.find(t, other.ID).Balance; got != 0 {
		t.Errorf("a deposit refused for reusing another wallet's key left %d in wallet %s", got, other.ID)
	}

	const callers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		moved   int
		failure []error
	)
	race := wallet.DepositRequest{IdempotencyKey: "raced-key", WalletID: a.ID, Amount: "1.00"}
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			r, err := svc.Deposit(k.ctx, op, race)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failure = append(failure, err)
			case !r.Replayed:
				moved++
			}
		}()
	}
	wg.Wait()
	for _, err := range failure {
		t.Errorf("one of %d copies of a deposit sent at once under one key failed (%v): the database the "+
			"application wired does not let the loser answer with the winner's receipt", callers, err)
	}
	if moved != 1 {
		t.Errorf("%d of %d copies of a deposit sent at once under one key said they moved the money, want 1", moved, callers)
	}
	if got := k.find(t, a.ID).Balance; got != 1100 {
		t.Errorf("%d copies of a deposit of 1.00 sent at once under one key left %d minor units, want 1100", callers, got)
	}
}

// holds sets money aside, spends past it, and settles it every way, twice.
func holds(t *testing.T, k *kit) {
	a := k.open(t, k.operatorA, k.holderA.ID, "holds")
	svc, op := k.Service, k.operatorA
	must(t, "a deposit", func() (wallet.Receipt, error) {
		return svc.Deposit(k.ctx, op, wallet.DepositRequest{IdempotencyKey: "holds-1", WalletID: a.ID, Amount: "100.00"})
	})

	held := must(t, "a hold of 40.00", func() (*wallet.Hold, error) {
		return svc.Hold(k.ctx, op, wallet.HoldRequest{IdempotencyKey: "holds-2", WalletID: a.ID, Amount: "40.00"})
	})
	w := k.find(t, a.ID)
	if w.Balance != 10000 || w.Held != 4000 || w.Available() != 6000 {
		t.Fatalf("a hold of 40.00 on 100.00 left balance %d, held %d, available %d; want 10000, 4000 and 6000: "+
			"a hold sets money aside without moving the balance", w.Balance, w.Held, w.Available())
	}
	if _, err := svc.Withdraw(k.ctx, op, wallet.WithdrawRequest{IdempotencyKey: "holds-3", WalletID: a.ID, Amount: "70.00"}); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("a withdrawal of 70.00 with 60.00 not held answered %v, want wallet.ErrInsufficientFunds: "+
			"the money a hold set aside was spent", err)
	}

	// Captured from several callers at once: one takes the money.
	const callers = 4
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		captured int
	)
	wg.Add(callers)
	for i := range callers {
		go func() {
			defer wg.Done()
			_, err := svc.Capture(k.ctx, op, wallet.CaptureRequest{IdempotencyKey: fmt.Sprintf("holds-capture-%d", i), HoldID: held.ID})
			if err == nil {
				mu.Lock()
				captured++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if captured != 1 {
		t.Fatalf("%d of %d captures of one hold under different keys succeeded, want exactly 1", captured, callers)
	}
	settledOnce := func(after string) {
		t.Helper()
		w := k.find(t, a.ID)
		if w.Balance != 6000 || w.Held != 0 {
			t.Fatalf("after %s the wallet holds %d with %d held, want 6000 and nothing held: the hold of "+
				"40.00 settled more than once", after, w.Balance, w.Held)
		}
	}
	settledOnce("the capture")
	if _, err := svc.Capture(k.ctx, op, wallet.CaptureRequest{IdempotencyKey: "holds-again", HoldID: held.ID}); err == nil {
		t.Fatal("a hold already captured was captured again under a new key")
	}
	settledOnce("capturing the captured hold again")
	if _, err := svc.Release(k.ctx, op, wallet.ReleaseRequest{HoldID: held.ID, Reason: "conformance"}); !errors.Is(err, wallet.ErrHoldNotHeld) {
		t.Fatalf("releasing a hold already captured answered %v, want wallet.ErrHoldNotHeld: its money is gone", err)
	}
	settledOnce("releasing the captured hold")

	second := must(t, "a second hold of 25.00", func() (*wallet.Hold, error) {
		return svc.Hold(k.ctx, op, wallet.HoldRequest{IdempotencyKey: "holds-4", WalletID: a.ID, Amount: "25.00"})
	})
	must(t, "releasing the second hold", func() (*wallet.Hold, error) {
		return svc.Release(k.ctx, op, wallet.ReleaseRequest{HoldID: second.ID, Reason: "conformance"})
	})
	settledOnce("releasing the second hold")
	_, _ = svc.Release(k.ctx, op, wallet.ReleaseRequest{HoldID: second.ID, Reason: "conformance"})
	settledOnce("releasing the second hold again")
	if _, err := svc.Capture(k.ctx, op, wallet.CaptureRequest{IdempotencyKey: "holds-5", HoldID: second.ID}); !errors.Is(err, wallet.ErrHoldNotHeld) {
		t.Fatalf("capturing a hold already released answered %v, want wallet.ErrHoldNotHeld", err)
	}
	settledOnce("capturing the released hold")
}

// isolated reaches for a wallet of Organization A as the operator of B, every
// way the Service and the routes offer.
func isolated(t *testing.T, k *kit) {
	svc, theirs := k.Service, k.operatorB
	a := k.open(t, k.operatorA, k.holderA.ID, "main")
	deposit := must(t, "a deposit", func() (wallet.Receipt, error) {
		return svc.Deposit(k.ctx, k.operatorA, wallet.DepositRequest{IdempotencyKey: "tenant-key", WalletID: a.ID, Amount: "50.00"})
	})
	held := must(t, "a hold", func() (*wallet.Hold, error) {
		return svc.Hold(k.ctx, k.operatorA, wallet.HoldRequest{IdempotencyKey: "tenant-hold", WalletID: a.ID, Amount: "5.00"})
	})

	reached := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("the operator of Organization %s %s of Organization %s: the tenant is not the Grant's, "+
				"so one customer reaches another's money", OrganizationB, what, OrganizationA)
		}
	}
	w, err := svc.Find(k.ctx, theirs, a.ID)
	if err == nil && w != nil {
		t.Errorf("the operator of Organization %s read wallet %s of Organization %s, balance %d: the tenant is "+
			"not the Grant's, so one customer reads another's money", OrganizationB, a.ID, OrganizationA, w.Balance)
	} else if !errors.Is(err, wallet.ErrNotFound) && !errors.Is(err, security.ErrForbidden) {
		t.Errorf("reading a wallet of another Organization answered %v, want wallet.ErrNotFound", err)
	}
	_, err = svc.FindBySlug(k.ctx, theirs, k.holderA.ID, "main")
	reached("found by holder and slug the wallet", err)
	_, err = svc.History(k.ctx, theirs, wallet.HistoryRequest{WalletID: a.ID})
	reached("read the ledger", err)
	_, err = svc.Deposit(k.ctx, theirs, wallet.DepositRequest{IdempotencyKey: "intruder-1", WalletID: a.ID, Amount: "1.00"})
	reached("deposited into a wallet", err)
	_, err = svc.Withdraw(k.ctx, theirs, wallet.WithdrawRequest{IdempotencyKey: "intruder-2", WalletID: a.ID, Amount: "1.00"})
	reached("withdrew from a wallet", err)
	_, err = svc.Hold(k.ctx, theirs, wallet.HoldRequest{IdempotencyKey: "intruder-3", WalletID: a.ID, Amount: "1.00"})
	reached("held money on a wallet", err)
	_, err = svc.Capture(k.ctx, theirs, wallet.CaptureRequest{IdempotencyKey: "intruder-4", HoldID: held.ID})
	reached("captured a hold", err)
	_, err = svc.Reverse(k.ctx, theirs, wallet.ReverseRequest{IdempotencyKey: "intruder-5", OperationID: deposit.Operation.ID, Reason: "conformance"})
	reached("reversed an operation", err)
	_, err = svc.OperationByKey(k.ctx, theirs, "tenant-key")
	reached("found by its idempotency key the operation", err)

	listed, err := svc.List(k.ctx, theirs, wallet.ListRequest{Query: data.Query{Limit: 200}})
	if err != nil {
		t.Fatalf("the operator of Organization %s could not list its own wallets: %v", OrganizationB, err)
	}
	for _, w := range listed {
		if w.ID == a.ID {
			t.Errorf("the operator of Organization %s lists wallet %s of Organization %s", OrganizationB, a.ID, OrganizationA)
		}
	}
	page, err := svc.ListOperations(k.ctx, theirs, wallet.OperationFilter{Limit: wallet.MaxPageSize})
	if err != nil {
		t.Fatalf("the operator of Organization %s could not list its own operations: %v", OrganizationB, err)
	}
	for _, r := range page.Rows {
		if r.WalletID == a.ID {
			t.Errorf("the operator of Organization %s lists operation %s on wallet %s of Organization %s",
				OrganizationB, r.OperationID, a.ID, OrganizationA)
		}
	}

	// The same holder, slug and key are another Organization's own.
	b := k.open(t, theirs, k.holderA.ID, "main")
	r, err := svc.Deposit(k.ctx, theirs, wallet.DepositRequest{IdempotencyKey: "tenant-key", WalletID: b.ID, Amount: "2.00"})
	if err != nil || r.Replayed {
		t.Errorf("a deposit in Organization %s under a key Organization %s already spent answered replayed=%v, "+
			"%v: an idempotency key is one Organization's, and another's use of it is another request",
			OrganizationB, OrganizationA, r.Replayed, err)
	}

	if got := k.find(t, a.ID); got.Balance != 5000 || got.Held != 500 {
		t.Errorf("after the operator of Organization %s reached for it, wallet %s holds %d with %d held, want "+
			"5000 with 500", OrganizationB, a.ID, got.Balance, got.Held)
	}

	if k.Server == nil {
		t.Log("the fixture has no Server: the application does not register the wallet's routes, so they were not checked")
		return
	}
	show := k.Server.Route("wallet.show", a.ID)
	own := test.As(t, k.Server, auth.Subject{ID: k.holderA.ID, Tenant: k.holderA.Tenant}).
		WithHeader("Accept", "application/json").Get(show)
	if !strings.Contains(own.GetContent(), a.ID) {
		t.Fatalf("GET %s, signed in as the holder of the wallet, did not answer the wallet: the module reads "+
			"another session store than the one the application's guards and As write, or the resolver does not "+
			"answer the account, so the check across Organizations below would prove nothing.\n%s", show, own.GetContent())
	}
	intruder := test.As(t, k.Server, auth.Subject{ID: theirs.ID, Tenant: theirs.Tenant}).
		WithHeader("Accept", "application/json")
	answer := intruder.Get(show)
	if strings.Contains(answer.GetContent(), a.ID) {
		t.Errorf("GET %s, signed in as the operator of Organization %s, answered the wallet of Organization %s: "+
			"the tenant a request is served in is not the session's\n%s", show, OrganizationB, OrganizationA, answer.GetContent())
	}
	answer.AssertNotFound()
	listing := intruder.Get(k.Server.Route("wallet.index"))
	if strings.Contains(listing.GetContent(), a.ID) {
		t.Errorf("the wallet listing, signed in as the operator of Organization %s, lists wallet %s of "+
			"Organization %s\n%s", OrganizationB, a.ID, OrganizationA, listing.GetContent())
	}
	intruder.Get(k.Server.Route("wallet.entries", a.ID)).AssertNotFound()
}

// readOnly authorizes reading a wallet and then tries to write with the Grant.
func readOnly(t *testing.T, k *kit) {
	if _, ok := any(wallet.WalletPolicy{}).(auth.Writes); !ok {
		t.Fatal("wallet.WalletPolicy does not implement auth.Writes, so every Grant it issues writes")
	}
	a := k.open(t, k.operatorA, k.holderA.ID, "read")
	must(t, "a deposit", func() (wallet.Receipt, error) {
		return k.Service.Deposit(k.ctx, k.operatorA, wallet.DepositRequest{IdempotencyKey: "read-1", WalletID: a.ID, Amount: "30.00"})
	})
	entries := k.ledger(t, a.ID)

	for _, reader := range []struct {
		who    string
		as     security.Subject
		action security.Action
	}{
		{"the holder", k.holderA, wallet.WalletView},
		{"the holder", k.holderA, wallet.WalletHistory},
		{"the operator", k.operatorA, wallet.WalletView},
		{"the operator", k.operatorA, wallet.WalletHistory},
	} {
		g, err := security.Authorize(k.ctx, wallet.WalletPolicy{}, reader.as, reader.action, *a)
		if err != nil {
			t.Fatalf("%s was refused %s on their own wallet: %v", reader.who, reader.action, err)
		}
		if _, err := wallet.Wallets(k.DB).WhereKey(a.ID).Update(k.ctx, g, map[string]any{"balance": int64(999999)}); !errors.Is(err, auth.ErrReadGrant) {
			t.Errorf("a Grant issued to %s for %s updated the wallet's balance (%v), want auth.ErrReadGrant: "+
				"a Grant to read money moved it", reader.who, reader.action, err)
		}
		if _, err := wallet.Entries(k.DB).Where("wallet_id", a.ID).Delete(k.ctx, g); !errors.Is(err, auth.ErrReadGrant) {
			t.Errorf("a Grant issued to %s for %s deleted ledger entries (%v), want auth.ErrReadGrant", reader.who, reader.action, err)
		}
	}
	if got := k.find(t, a.ID).Balance; got != 3000 {
		t.Errorf("after writes under Grants issued to read, wallet %s holds %d, want 3000", a.ID, got)
	}
	if got := k.ledger(t, a.ID); len(got) != len(entries) {
		t.Errorf("after deletes under Grants issued to read, wallet %s has %d entries, want %d", a.ID, len(got), len(entries))
	}
}
