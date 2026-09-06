package feature_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/arandu-io/framework/data"
	hedatabase "github.com/arandu-io/hesape/database"
	mysqlconnector "github.com/arandu-io/hesape/database/connectors/mysql"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What PostgreSQL answers, MySQL has to answer too.
//
// The guard on a balance is a predicate on an update, and what that predicate
// sees while another transaction changes the same row is the engine's answer
// rather than this package's. So a claim proved against one server is a claim
// about that server. This file is the second one.
//
// # The two things that are not the same here
//
// MySQL quotes identifiers with backticks rather than double quotes, and it has
// no returning clause, so the statement that moves a balance is composed
// differently and read back in two steps instead of one. Both differences are
// inside moveStatement and moving; nothing about the guard changes, and the
// tests below are the same tests.
//
// InnoDB also repeats reads by default where PostgreSQL reads committed. The
// transaction is opened at the level rather than told afterwards, because MySQL
// refuses a SET once a transaction is in progress -- so the same code means the
// same thing on the three servers instead of meaning whatever each one was
// configured for.
//
// They run when ARANDU_TEST_MYSQL_DSN names a server and skip when it does not.

// mysqlDSN is the environment variable the suite reads its server from.
const mysqlDSN = "ARANDU_TEST_MYSQL_DSN"

// mysqlServer opens a database of its own on the configured server, with this
// package's tables in it.
//
// A database per test rather than one set of tables for the package: these run
// in parallel against one server, and shared tables would put one test's
// wallets in another test's listing. MySQL has no schemas separate from
// databases, so the isolation PostgreSQL gets from a search path is a database
// here.
func mysqlServer(t *testing.T) *data.DB {
	t.Helper()

	dsn := os.Getenv(mysqlDSN)
	if dsn == "" {
		t.Skipf("no server: set %s to run the tests that need transactions which really interleave", mysqlDSN)
	}
	name := schemaFor(t.Name())
	ctx := context.Background()

	driver := mysqlconnector.MySqlConnector{}.DriverName()
	bootstrap, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("opening MySQL: %v", err)
	}
	if err := bootstrap.PingContext(ctx); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("no server answered at %s: %v", mysqlDSN, err)
	}
	if _, err := bootstrap.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("dropping the database: %v", err)
	}
	if _, err := bootstrap.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("creating the database: %v", err)
	}
	_ = bootstrap.Close()

	t.Cleanup(func() {
		cleanup, err := sql.Open(driver, dsn)
		if err != nil {
			return
		}
		defer func() { _ = cleanup.Close() }()
		_, _ = cleanup.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+name+"`")
	})

	handle, err := sql.Open(driver, withDatabase(t, dsn, name))
	if err != nil {
		t.Fatalf("opening MySQL: %v", err)
	}
	handle.SetMaxOpenConns(writers)
	t.Cleanup(func() { _ = handle.Close() })

	connection := hedatabase.NewConnection(handle, "", "", map[string]any{
		"driver": string(hedatabase.DialectMySQL),
		"name":   "wallet-test",
	})
	migrationConnection := hedatabase.ForMigrations(connection)

	module, err := wallet.New(wallet.Config{Tenant: tenant, CSRF: csrf()},
		data.Wrap(handle, data.DialectMySQL), sessionStore())
	if err != nil {
		t.Fatalf("building the module: %v", err)
	}
	for _, migration := range module.Migrations() {
		if err := migration.Up(ctx, migrationConnection); err != nil {
			t.Fatalf("applying %s: %v", migration.GetName(), err)
		}
	}

	return data.Wrap(handle, data.DialectMySQL)
}

// withDatabase returns the DSN with the database name replaced.
//
// A MySQL DSN is not a URL, so it is not parsed as one: the name sits between
// the last slash and the first question mark, and that is what is rewritten.
func withDatabase(t *testing.T, dsn, name string) string {
	t.Helper()

	head, query, hasQuery := strings.Cut(dsn, "?")
	slash := strings.LastIndex(head, "/")
	if slash < 0 {
		t.Fatalf("%s names no database: %q", mysqlDSN, dsn)
	}
	out := head[:slash+1] + name
	if hasQuery {
		out += "?" + query
	}
	return out
}

// TestTheGuardIsWhatKeepsTheBalanceWholeOnMySQL is the property the whole
// package rests on, asked of the second engine.
//
// It is the one that matters most here, because the statement has no returning
// clause: the row is read back in a second statement, and if that read were the
// decision rather than the report of one, the count below would not be exact.
// A package deciding in Go on a value it read lets the losers through.
//
// Removing the balance from the predicate fails it here as loudly as anywhere:
// measured, two hundred withdrawals settle against a balance of twenty and the
// wallet ends at -18000.
//
// # What the isolation level does and does not buy here
//
// Measured, not assumed: this test passes on MySQL at read committed and at
// InnoDB's own repeatable read. An update re-reads the row it is about to write
// at both, so the guard decides at both, and the level is not what makes this
// exact -- the predicate is.
//
// The level is still named, for the reason the level is always named: the
// package is one package on three engines, and what a read inside its
// transaction sees should not be a property of which server it was pointed at
// or of what an operator set for a cluster. Naming it makes the same code mean
// the same thing everywhere; the guard is what keeps the money whole.
func TestTheGuardIsWhatKeepsTheBalanceWholeOnMySQL(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(mysqlServer(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	const (
		opening    = wallet.Amount(2000)
		each       = wallet.Amount(100)
		affordable = int(opening / each)
	)
	deposit(t, service, account.ID, "opening", "20.00")

	succeeded, other := race(withdrawers, func(i int) error {
		return withdraw(t, service, staff(), account.ID, fmt.Sprintf("withdraw-%d", i), "1.00", false)
	})
	for _, err := range other {
		t.Errorf("a withdrawal failed for a reason that is not the balance: %v", err)
	}
	if succeeded != affordable {
		t.Errorf("%d withdrawals succeeded against a balance of %d, want exactly %d",
			succeeded, opening, affordable)
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

// TestTheGuardCountsTheCreditLimitOnMySQL is the same property with a floor
// that is not zero.
//
// A guard that read the limit in Go and decided there would pass on SQLite and
// let this wallet past its limit here, which is why the assertion is on the
// exact count and the exact final balance rather than only on the sign.
func TestTheGuardCountsTheCreditLimitOnMySQL(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(mysqlServer(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	const (
		opening    = wallet.Amount(2000)
		credit     = wallet.Amount(1000)
		each       = wallet.Amount(100)
		affordable = int((opening + credit) / each)
	)
	deposit(t, service, account.ID, "opening", "20.00")
	creditLimit(t, service, account.ID, "10.00")

	succeeded, other := race(withdrawers, func(i int) error {
		return withdraw(t, service, staff(), account.ID, fmt.Sprintf("withdraw-%d", i), "1.00", false)
	})
	for _, err := range other {
		t.Errorf("a withdrawal failed for a reason that is not the balance: %v", err)
	}
	if succeeded != affordable {
		t.Errorf("%d withdrawals succeeded against %d plus %d of credit, want exactly %d",
			succeeded, opening, credit, affordable)
	}
	if balance, want := balanceOf(t, service, account.ID), -credit; balance != want {
		t.Fatalf("the balance is %d, want %d: the wallet stopped somewhere other than its limit", balance, want)
	}
	if ledger := ledgerOf(t, service, account.ID); ledger != -credit {
		t.Fatalf("the ledger sums to %d, want %d", ledger, -credit)
	}
}

// TestTheIdempotencyKeyHoldsOnMySQL asks the second guarantee of a concurrent
// write: the same request arriving many times moves the money once.
//
// The unique index refuses all but one, and the losers answer with the winner's
// receipt rather than with an error. On an engine where the row is read back in
// a second statement, a replay that reported the balance it found instead of
// the one its own operation wrote would show up in the total.
func TestTheIdempotencyKeyHoldsOnMySQL(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(mysqlServer(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "20.00")

	answered, other := race(writers, func(int) error {
		return withdraw(t, service, staff(), account.ID, "one-key", "1.00", false)
	})
	for _, err := range other {
		t.Errorf("a caller under one key was refused: %v", err)
	}
	if answered != writers {
		t.Errorf("%d of %d callers were answered, and every one of them should be", answered, writers)
	}
	if balance, want := balanceOf(t, service, account.ID), wallet.Amount(1900); balance != want {
		t.Fatalf("the balance is %d, want %d: one key moved the money more than once", balance, want)
	}
	if ledger := ledgerOf(t, service, account.ID); ledger != 1900 {
		t.Fatalf("the ledger sums to %d, want 1900", ledger)
	}
}

// TestAFrozenWalletIsRefusedAtTheWriteOnMySQL holds the gate the statement
// carries, on the engine where the statement is composed differently.
//
// The two reasons a wallet is out of service are columns in the same predicate
// as the balance, so they are asked at the instant of the write. A wallet
// frozen between the read that loaded it and the write that would move it is
// refused by the write, and this is that in the small: freeze it, then ask.
func TestAFrozenWalletIsRefusedAtTheWriteOnMySQL(t *testing.T) {
	t.Parallel()

	db := mysqlServer(t)
	service := wallet.NewWalletService(db, nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "20.00")

	diverge(t, db, account.ID, 500)
	if _, err := service.Reconcile(context.Background(), staff(), account.ID); err != nil {
		t.Fatalf("reconciling a wallet that stopped adding up: %v", err)
	}

	err := withdraw(t, service, staff(), account.ID, "after-the-freeze", "1.00", false)
	if err == nil {
		t.Fatal("a frozen wallet paid out")
	}
	if !errors.Is(err, wallet.ErrWalletFrozen) {
		t.Fatalf("a frozen wallet refused a withdrawal for the wrong reason: %v", err)
	}
}

// race runs fn n times at once and reports how many were answered.
//
// The starting gate is a WaitGroup rather than a sleep: every caller is inside
// fn before any of them reaches the database, which is what makes the writes
// actually interleave instead of queueing.
//
// A refusal about the money is counted as an answer to the question rather than
// as a failure, and everything else comes back for the test to report. That
// split is the whole point: "not enough" is this package deciding, and a driver
// error is not.
func race(n int, fn func(i int) error) (int, []error) {
	var (
		start     sync.WaitGroup
		done      sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		other     []error
	)
	start.Add(1)
	done.Add(n)

	for i := range n {
		go func() {
			defer done.Done()
			start.Wait()

			err := fn(i)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, wallet.ErrInsufficientFunds), errors.Is(err, wallet.ErrBalanceEmpty):
			default:
				other = append(other, err)
			}
		}()
	}
	start.Done()
	done.Wait()
	return succeeded, other
}
