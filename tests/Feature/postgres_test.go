package feature_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/arandu-io/framework/data"
	hedatabase "github.com/arandu-io/hesape/database"
	pgxconnector "github.com/arandu-io/hesape/database/connectors/pgx"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What SQLite cannot be asked, PostgreSQL answers.
//
// SQLite serializes writers: one transaction holds the whole database, so a
// read inside a transaction can never see a value another transaction is about
// to change. Every claim about a guarded update therefore comes out true there
// whether the guard exists or not, and a suite that stopped at SQLite would be
// reporting the engine's behaviour as the package's.
//
// PostgreSQL at READ COMMITTED does interleave. Two transactions read the same
// balance, both decide it is enough, and both write -- unless the deciding is
// done by the statement that writes. That is the defect these tests exist to
// catch, and it is catchable here: with the predicate removed from the update,
// TestTheGuardIsWhatKeepsTheBalanceWhole fails and the balance goes negative.
//
// They run when ARANDU_TEST_POSTGRES_DSN names a server, and skip when it does
// not, so `go test ./...` still passes with nothing installed. The DSN is the
// driver's own rather than a configuration struct: the point is to exercise the
// engine.

// postgresDSN is the environment variable the suite reads its server from.
const postgresDSN = "ARANDU_TEST_POSTGRES_DSN"

// postgres opens a schema of its own on the configured server, with this
// package's tables in it.
//
// A schema per test, dropped afterwards, rather than a database per test: the
// tests run in parallel against one server, and a shared set of tables would
// make one test's wallets visible to another's listing.
func postgres(t *testing.T) *data.DB {
	t.Helper()

	dsn := os.Getenv(postgresDSN)
	if dsn == "" {
		t.Skipf("no server: set %s to run the tests that need transactions which really interleave", postgresDSN)
	}

	schema := schemaFor(t.Name())
	ctx := context.Background()

	// The schema is created over a connection of its own and closed again,
	// because the pool below resolves names in a schema that has to exist
	// before its first connection opens.
	bootstrap, err := sql.Open(pgxconnector.PostgresConnector{}.DriverName(), dsn)
	if err != nil {
		t.Fatalf("opening PostgreSQL: %v", err)
	}
	if err := bootstrap.PingContext(ctx); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("no server answered at %s: %v", postgresDSN, err)
	}
	if _, err := bootstrap.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("dropping the schema: %v", err)
	}
	if _, err := bootstrap.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("creating the schema: %v", err)
	}
	_ = bootstrap.Close()

	t.Cleanup(func() {
		cleanup, err := sql.Open(pgxconnector.PostgresConnector{}.DriverName(), dsn)
		if err != nil {
			return
		}
		defer func() { _ = cleanup.Close() }()
		_, _ = cleanup.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	// The search path goes in the connection string and not into a statement
	// somebody runs afterwards: a pool opens connections when it needs them, so
	// a setting applied to the first one is a setting the second does not have
	// -- which is a test that passes until it is made concurrent.
	handle, err := sql.Open(pgxconnector.PostgresConnector{}.DriverName(), withSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatalf("opening PostgreSQL: %v", err)
	}
	handle.SetMaxOpenConns(writers)
	t.Cleanup(func() { _ = handle.Close() })

	connection := hedatabase.NewConnection(handle, schema, "", map[string]any{
		"driver": string(hedatabase.DialectPostgres),
		"name":   "wallet-test",
	})
	migrationConnection := hedatabase.ForMigrations(connection)

	module, err := wallet.New(wallet.Config{Tenant: tenant}, data.Wrap(handle, data.DialectPostgres), sessionStore())
	if err != nil {
		t.Fatalf("building the module: %v", err)
	}
	for _, migration := range module.Migrations() {
		if err := migration.Up(ctx, migrationConnection); err != nil {
			t.Fatalf("applying %s: %v", migration.GetName(), err)
		}
	}

	return data.Wrap(handle, data.DialectPostgres)
}

// schemaFor turns a test's name into a PostgreSQL identifier.
//
// A schema per test, dropped afterwards, rather than one set of tables for the
// whole package: these run in parallel against one server, and shared tables
// would put one test's wallets in another test's listing.
func schemaFor(name string) string {
	return "wallet_" + strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '_'
	}, name)
}

// withSearchPath returns the connection string with the schema named on it.
func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("reading %s: %v", postgresDSN, err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestTheGuardIsWhatKeepsTheBalanceWhole(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(postgres(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	// Twenty units in, one per withdrawal, and far more askers than the balance
	// can serve. At READ COMMITTED every one of them reads a balance that is
	// still changing, so a package that decided in Go on the value it read
	// would let the losers through and drive the column negative.
	const (
		opening    = wallet.Amount(2000)
		each       = wallet.Amount(100)
		affordable = int(opening / each)
	)
	deposit(t, service, account.ID, "opening", "20.00")

	var (
		start     sync.WaitGroup
		done      sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		other     []error
	)
	start.Add(1)
	done.Add(withdrawers)

	for i := range withdrawers {
		go func() {
			defer done.Done()
			start.Wait()

			_, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
				IdempotencyKey: fmt.Sprintf("withdraw-%d", i),
				WalletID:       account.ID,
				Amount:         "1.00",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, wallet.ErrInsufficientFunds):
			default:
				other = append(other, err)
			}
		}()
	}
	start.Done()
	done.Wait()

	for _, err := range other {
		t.Errorf("a withdrawal failed for a reason that is not the balance: %v", err)
	}
	if succeeded != affordable {
		t.Errorf("%d withdrawals succeeded against a balance of %d, want exactly %d", succeeded, opening, affordable)
	}

	balance := balanceOf(t, service, account.ID)
	if balance < 0 {
		t.Fatalf("the balance went negative: %d", balance)
	}
	if want := opening - wallet.Amount(succeeded)*each; balance != want {
		t.Fatalf("the balance is %d, want %d", balance, want)
	}
	if ledger := ledgerOf(t, service, account.ID); ledger != balance {
		t.Fatalf("the ledger sums to %d and the balance column says %d", ledger, balance)
	}
}

// TestTheGuardCountsTheCreditLimitAndStopsAtIt is the same property as the
// test above with a floor that is not zero, and it is a separate test because
// the mutation that breaks it is a different one.
//
// A guard that compared the balance against the amount alone would be safe and
// wrong: it would refuse ten of the thirty withdrawals this wallet can afford,
// and the count below says so. A guard that read the limit in Go and decided
// there would pass on SQLite and let this wallet past its limit here, which is
// why the assertion is on the exact count and the exact final balance rather
// than only on the sign.
func TestTheGuardCountsTheCreditLimitAndStopsAtIt(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(postgres(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	// Twenty units in and ten of credit: thirty withdrawals of one unit are
	// affordable and the two hundredth is not.
	const (
		opening    = wallet.Amount(2000)
		credit     = wallet.Amount(1000)
		each       = wallet.Amount(100)
		affordable = int((opening + credit) / each)
	)
	deposit(t, service, account.ID, "opening", "20.00")
	creditLimit(t, service, account.ID, "10.00")

	var (
		start     sync.WaitGroup
		done      sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		other     []error
	)
	start.Add(1)
	done.Add(withdrawers)

	for i := range withdrawers {
		go func() {
			defer done.Done()
			start.Wait()

			_, err := service.Withdraw(context.Background(), staff(), wallet.WithdrawRequest{
				IdempotencyKey: fmt.Sprintf("withdraw-%d", i),
				WalletID:       account.ID,
				Amount:         "1.00",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, wallet.ErrInsufficientFunds):
			default:
				other = append(other, err)
			}
		}()
	}
	start.Done()
	done.Wait()

	for _, err := range other {
		t.Errorf("a withdrawal failed for a reason that is not the balance: %v", err)
	}
	if succeeded != affordable {
		t.Errorf("%d withdrawals succeeded against a balance of %d and a credit limit of %d, want exactly %d",
			succeeded, opening, credit, affordable)
	}

	balance := balanceOf(t, service, account.ID)
	if balance < -credit {
		t.Fatalf("the balance is %d, which is past a credit limit of %d", balance, credit)
	}
	if want := opening - wallet.Amount(succeeded)*each; balance != want {
		t.Fatalf("the balance is %d, want %d", balance, want)
	}
	if ledger := ledgerOf(t, service, account.ID); ledger != balance {
		t.Fatalf("the ledger sums to %d and the balance column says %d", ledger, balance)
	}
}

func TestTheIdempotencyKeyHoldsWhenTransactionsInterleave(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(postgres(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)

	// The lookup that opens every movement finds nothing in all of these,
	// because none of them has committed yet. What stops the second is the
	// unique index the operation row goes in under, and the loser answering
	// with the winner's receipt instead of its own error.
	const callers = 40

	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		written int
		failed  []error
	)
	start.Add(1)
	done.Add(callers)

	for range callers {
		go func() {
			defer done.Done()
			start.Wait()

			receipt, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
				IdempotencyKey: "one-key", WalletID: account.ID, Amount: "10.00",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failed = append(failed, err)
			case !receipt.Replayed:
				written++
			}
		}()
	}
	start.Done()
	done.Wait()

	for _, err := range failed {
		t.Errorf("a call under a shared key failed: %v", err)
	}
	if written != 1 {
		t.Errorf("%d of %d callers reported that they moved the money, want exactly 1", written, callers)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d after %d identical requests, want 1000", got, callers)
	}
	if got := ledgerOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the ledger sums to %d, want 1000", got)
	}
}

func TestTransfersInOppositeDirectionsDoNotDeadlockOnRowLocks(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(postgres(t), nil)
	left := openWallet(t, service, "user-1", "main", 2)
	right := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, left.ID, "opening-left", "50.00")
	deposit(t, service, right.ID, "opening-right", "50.00")

	// This is the engine the ordering discipline exists for. PostgreSQL takes a
	// row lock on each updated wallet and holds it to the end of the
	// transaction; two transfers that locked in the order the request named
	// them would each hold what the other wants, and PostgreSQL would break the
	// cycle by killing one with a deadlock error. The service sorts the
	// movements by wallet identifier instead, so both directions take the same
	// two locks in the same sequence and neither waits on the other's.
	//
	// A deadlock here arrives as an error, so the assertion that none happened
	// is the empty list of failures below.
	const pairs = 100

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
		fails []error
	)
	start.Add(1)
	done.Add(pairs * 2)

	send := func(from, to, key string) {
		defer done.Done()
		start.Wait()
		_, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
			IdempotencyKey: key, FromWalletID: from, ToWalletID: to, Amount: "1.00",
		})
		if err != nil {
			mu.Lock()
			fails = append(fails, err)
			mu.Unlock()
		}
	}

	for i := range pairs {
		go send(left.ID, right.ID, fmt.Sprintf("left-to-right-%d", i))
		go send(right.ID, left.ID, fmt.Sprintf("right-to-left-%d", i))
	}
	start.Done()
	done.Wait()

	for _, err := range fails {
		t.Errorf("a transfer failed, which on this engine is what a deadlock looks like: %v", err)
	}

	leftBalance := balanceOf(t, service, left.ID)
	rightBalance := balanceOf(t, service, right.ID)
	if total := leftBalance + rightBalance; total != 10000 {
		t.Fatalf("the two wallets hold %d together, want 10000", total)
	}
	if leftBalance != 5000 || rightBalance != 5000 {
		t.Fatalf("the wallets hold %d and %d, want 5000 each", leftBalance, rightBalance)
	}
}

func TestConcurrentReversalsInterleaveAndUndoOnce(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(postgres(t), nil)
	account := openWallet(t, service, "user-1", "main", 2)
	credited := deposit(t, service, account.ID, "opening", "10.00")

	// Each caller has its own key, so none of these is a replay: they are
	// different requests to undo one movement, all of them reading a table that
	// says it has not been undone yet. The unique index over the kind and the
	// operation being settled is what makes exactly one of them right.
	const callers = 25

	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		undone  int
		refused int
		other   []error
	)
	start.Add(1)
	done.Add(callers)

	for i := range callers {
		go func() {
			defer done.Done()
			start.Wait()

			_, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
				IdempotencyKey: fmt.Sprintf("reverse-%d", i),
				OperationID:    credited.Operation.ID,
				Reason:         "chargeback",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				undone++
			case errors.Is(err, wallet.ErrAlreadyReversed):
				refused++
			default:
				other = append(other, err)
			}
		}()
	}
	start.Done()
	done.Wait()

	for _, err := range other {
		t.Errorf("a reversal failed for a reason other than being second: %v", err)
	}
	if undone != 1 {
		t.Errorf("%d of %d reversals succeeded, want exactly 1", undone, callers)
	}
	if refused != callers-undone {
		t.Errorf("%d reversals were refused as already done, want %d", refused, callers-undone)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d after one deposit and its reversal, want 0", got)
	}
}

func TestARolledBackTransferLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(postgres(t), nil)
	source := openWallet(t, service, "user-1", "main", 2)
	target := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, source.ID, "opening", "10.00")

	// The transfer moves the wallet whose identifier sorts first, then the
	// other. One of the two orders therefore credits the target before the
	// source is found to be short, and the rollback is what has to take that
	// credit back -- along with the operation row, or the key would be spent on
	// a request that moved nothing.
	_, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-1", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "10.01",
	})
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("an uncovered transfer returned %v, want ErrInsufficientFunds", err)
	}

	if got := balanceOf(t, service, source.ID); got != 1000 {
		t.Errorf("the source holds %d, want 1000", got)
	}
	if got := balanceOf(t, service, target.ID); got != 0 {
		t.Errorf("the target holds %d, want 0", got)
	}
	if got := ledgerOf(t, service, target.ID); got != 0 {
		t.Errorf("the target's ledger sums to %d, want 0", got)
	}
	if statement, err := service.History(context.Background(), staff(), wallet.HistoryRequest{WalletID: target.ID}); err != nil {
		t.Errorf("reading the target's statement: %v", err)
	} else if len(statement.Entries) != 0 {
		t.Errorf("the rolled-back transfer left %d entries on the target", len(statement.Entries))
	}

	// And the key is free, because nothing under it was committed.
	if _, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "key-1", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "1.00",
	}); err != nil {
		t.Fatalf("retrying the key of a rolled-back request: %v", err)
	}
	if got := balanceOf(t, service, target.ID); got != 100 {
		t.Fatalf("the target holds %d, want 100", got)
	}
}

// TestConcurrentExchangesUnderOneKeySettleAtOneRate is the idempotency
// property with a rate in it, and it needs an engine that really interleaves.
//
// Forty callers send one key at once. All of them find no operation, because
// none has committed; all of them are free to ask the provider, and the
// provider here answers a different rate to each. One of them wins the unique
// index and the other thirty-nine roll back with nothing written and go and
// read the winner's receipt.
//
// What has to be true afterwards is not only that the money moved once. It is
// that every caller was told the same rate, that the rate they were told is the
// rate the ledger settled at, and that the thirty-nine quotes nobody used left
// nothing behind -- a second conversion row, or a credit computed from a quote
// whose row lost.
func TestConcurrentExchangesUnderOneKeySettleAtOneRate(t *testing.T) {
	t.Parallel()

	rates := &driftingRate{}
	service := wallet.NewWalletService(postgres(t), rates)
	source := openIn(t, service, "user-1", "main", "USD", 2)
	target := openIn(t, service, "user-2", "main", "BRL", 2)
	deposit(t, service, source.ID, "opening", "100.00")

	const callers = 40

	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		written int
		answers []wallet.Conversion
		failed  []error
	)
	start.Add(1)
	done.Add(callers)

	for range callers {
		go func() {
			defer done.Done()
			start.Wait()

			receipt, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
				IdempotencyKey: "one-key", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "10.00",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failed = append(failed, err)
			default:
				if !receipt.Replayed {
					written++
				}
				if receipt.Conversion != nil {
					answers = append(answers, *receipt.Conversion)
				}
			}
		}()
	}
	start.Done()
	done.Wait()

	for _, err := range failed {
		t.Errorf("a call under a shared key failed: %v", err)
	}
	if written != 1 {
		t.Errorf("%d of %d callers reported that they moved the money, want exactly 1", written, callers)
	}
	if len(answers) != callers {
		t.Fatalf("%d of %d callers were told a rate, want all of them", len(answers), callers)
	}

	// Every caller was told the same conversion, down to the moment it was
	// quoted. A caller that was answered with its own losing quote would show
	// up here, and its money would not be in the wallet.
	settled := answers[0]
	for _, answer := range answers[1:] {
		if answer.OperationID != settled.OperationID ||
			answer.RateNumerator != settled.RateNumerator ||
			answer.RateDenominator != settled.RateDenominator ||
			answer.ToAmount != settled.ToAmount {
			t.Fatalf("one caller was told %s crediting %d and another %s crediting %d",
				answer.Rate(), answer.ToAmount, settled.Rate(), settled.ToAmount)
		}
	}

	// And the ledger settled at exactly that rate, on both sides.
	if got := balanceOf(t, service, source.ID); got != 9000 {
		t.Errorf("the source holds %d after %d identical requests, want 9000", got, callers)
	}
	if got := balanceOf(t, service, target.ID); got != settled.ToAmount {
		t.Errorf("the target holds %d and the recorded conversion credited %d", got, settled.ToAmount)
	}
	for _, id := range []string{source.ID, target.ID} {
		if got, want := ledgerOf(t, service, id), balanceOf(t, service, id); got != want {
			t.Errorf("the ledger of %s sums to %d and its balance is %d", id, got, want)
		}
	}

	// One conversion row for one operation, whatever the losers quoted.
	statement := statementOf(t, service, target.ID)
	if got := len(statement.Conversions); got != 1 {
		t.Fatalf("the target's statement carries %d rates after %d identical requests, want 1", got, callers)
	}
	assertConversionReproduces(t, statement.Conversions[settled.OperationID])
}
