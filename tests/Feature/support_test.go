package feature_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	hedatabase "github.com/arandu-io/hesape/database"
	sqliteconnector "github.com/arandu-io/hesape/database/connectors/sqlite"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The money tests run against a real database, and they have to.
//
// Everything this package claims about concurrency is a claim about what a
// database does when two statements arrive at once: that an update with a
// predicate matches zero rows when the predicate stopped being true, that a
// unique index refuses the second of two identical inserts, that a transaction
// leaves nothing behind when it rolls back. None of that can be shown by a fake
// -- a fake would demonstrate the assumption rather than test it.
//
// The engine is SQLite, through the pure-Go driver the connector registers, and
// its limits are stated here rather than left for a reader to discover. SQLite
// serializes writers: with BEGIN IMMEDIATE and a busy timeout, a second writing
// transaction waits for the first instead of failing, so the tests below run
// with real contention in Go and a real queue at the database. What they
// therefore cannot demonstrate is a deadlock between two row locks, because
// SQLite takes one lock for the whole database and never two. The ordering
// discipline that prevents such a deadlock on an engine that does take row
// locks is in the service and is asserted here by outcome, not by observing a
// deadlock that this engine cannot produce.

// dsn is how the tests open a database file.
//
// Three settings, and each one is here for a reason the tests depend on:
//
//   - _txlock=immediate takes the write lock when the transaction begins rather
//     than when it first writes. Without it two transactions that both begin by
//     reading deadlock on the upgrade, and SQLite reports that as an error
//     instead of waiting.
//   - journal_mode(WAL) lets a reader run while a writer holds the lock.
//   - busy_timeout waits for the lock instead of failing, which is what turns
//     "database is locked" into the queue the writers are actually in.
const dsn = "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(20000)"

// writers is how many connections the pool opens.
//
// More than one on purpose. The framework's own Open pins SQLite to a single
// connection, which is the right default for an application and the wrong one
// for these tests: with one connection the pool serializes the goroutines
// before the database ever sees them, and a concurrency test that never reaches
// the engine measures the pool.
const writers = 8

// tenant is the customer every test runs as, except where a second one is the
// point.
const tenant = "acme"

// database opens a fresh database with this package's schema applied.
func database(t *testing.T) *data.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "wallet.sqlite")
	handle, err := sql.Open(sqliteconnector.SQLiteConnector{}.DriverName(), "file:"+path+dsn)
	if err != nil {
		t.Fatalf("opening SQLite: %v", err)
	}
	handle.SetMaxOpenConns(writers)
	t.Cleanup(func() { _ = handle.Close() })

	connection := hedatabase.NewConnection(handle, path, "", map[string]any{
		"driver": string(hedatabase.DialectSQLite),
		"name":   "wallet-test",
	})
	migrationConnection := hedatabase.ForMigrations(connection)

	module, err := wallet.New(wallet.Config{Tenant: tenant}, data.Wrap(handle, data.DialectSQLite), sessionStore())
	if err != nil {
		t.Fatalf("building the module: %v", err)
	}
	for _, migration := range module.Migrations() {
		if err := migration.Up(context.Background(), migrationConnection); err != nil {
			t.Fatalf("applying %s: %v", migration.GetName(), err)
		}
	}

	return data.Wrap(handle, data.DialectSQLite)
}

// sessionStore is what New requires. Nothing below goes through HTTP, so it is
// never read; New refuses a nil one, and rightly.
func sessionStore() *security.SessionStore {
	return security.NewSessionStore([]byte(appKey), 0, false, security.NewMemoryBackend())
}

// staff is the operator every test acts as unless the subject is the point.
func staff() security.Subject {
	return security.Subject{ID: "staff-1", Tenant: tenant, Roles: []string{wallet.OperatorRole}, Verified: true}
}

// person is a holder: somebody whose own money it is.
func person(id string) security.Subject {
	return security.Subject{ID: id, Tenant: tenant, Verified: true}
}

// openWallet opens one wallet and fails the test if it cannot.
func openWallet(t *testing.T, service *wallet.WalletService, holder, slug string, places int) *wallet.Wallet {
	t.Helper()

	record, err := service.Open(context.Background(), staff(), wallet.OpenRequest{
		HolderID:      holder,
		Slug:          slug,
		Name:          holder + " " + slug,
		Currency:      "BRL",
		DecimalPlaces: places,
	})
	if err != nil {
		t.Fatalf("opening the wallet %s/%s: %v", holder, slug, err)
	}
	return record
}

// deposit credits a wallet and fails the test if it cannot.
func deposit(t *testing.T, service *wallet.WalletService, walletID, key, amount string) wallet.Receipt {
	t.Helper()

	receipt, err := service.Deposit(context.Background(), staff(), wallet.DepositRequest{
		IdempotencyKey: key,
		WalletID:       walletID,
		Amount:         amount,
	})
	if err != nil {
		t.Fatalf("depositing %s: %v", amount, err)
	}
	return receipt
}

// balanceOf reads a wallet's balance column.
func balanceOf(t *testing.T, service *wallet.WalletService, walletID string) wallet.Amount {
	t.Helper()

	record, err := service.Find(context.Background(), staff(), walletID)
	if err != nil {
		t.Fatalf("reading the balance of %s: %v", walletID, err)
	}
	return record.Balance
}

// ledgerOf sums a wallet's entries, which is what the balance column is a
// projection of.
//
// It reads through the service, so it is subject to the same authorization as
// everything else: a helper that went around the policy to check a balance
// would be a helper proving a property of a path nobody uses.
func ledgerOf(t *testing.T, service *wallet.WalletService, walletID string) wallet.Amount {
	t.Helper()

	total := wallet.Amount(0)
	cursor := ""
	for {
		statement, err := service.History(context.Background(), staff(), wallet.HistoryRequest{
			WalletID: walletID,
			Query:    data.Query{Cursor: cursor, Limit: 200},
		})
		if err != nil {
			t.Fatalf("reading the ledger of %s: %v", walletID, err)
		}
		if len(statement.Entries) == 0 {
			return total
		}
		for _, entry := range statement.Entries {
			sum, err := total.Add(entry.Signed())
			if err != nil {
				t.Fatalf("summing the ledger of %s: %v", walletID, err)
			}
			total = sum
		}
		if len(statement.Entries) < 200 {
			return total
		}
		cursor = statement.Entries[len(statement.Entries)-1].ID
	}
}

// The rate providers the money tests run against.
//
// This package ships none, on purpose: a rate comes from outside the process
// and the manifest says nothing leaves it. What a test needs is not a real
// quote but a provider that behaves in a stated way, so that a property of this
// package can be separated from a property of whatever service somebody wires
// in. Each one below is written for a question the suite asks.

// quotedAt is the moment every test rate says it was obtained.
//
// Fixed rather than time.Now, because a recorded rate is compared against the
// value it was quoted with, and a clock read twice answers twice.
var quotedAt = time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)

// fixedRate answers the same rate every time, and counts how often it is asked.
//
// The count is the point as much as the rate is: a conversion that consults the
// provider twice is a conversion that could have applied two different numbers,
// and the only way to see it from outside is to count.
type fixedRate struct {
	numerator   int64
	denominator int64

	mu    sync.Mutex
	calls int
}

// Rate answers the configured fraction for whatever pair it is asked about.
func (p *fixedRate) Rate(_ context.Context, _ security.Grant, from, to wallet.Currency) (wallet.Rate, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return wallet.Rate{
		From:        from,
		To:          to,
		Numerator:   p.numerator,
		Denominator: p.denominator,
		QuotedAt:    quotedAt,
	}, nil
}

// Calls is how many times the provider has been asked.
func (p *fixedRate) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// driftingRate answers a different rate on every call, which is what a rate
// provider really does: a quote is true of a moment, and two moments differ.
//
// A conversion that reads it once uses the first answer and records the first
// answer. One that reads it twice cannot: the number it multiplied by and the
// number it wrote down are different, and no amount of care in between makes
// them agree again.
type driftingRate struct {
	mu    sync.Mutex
	calls int
}

// Rate answers numerator = 1 + the number of calls so far, over one, so the
// first answer is 2, the second 3, and no two calls agree.
func (p *driftingRate) Rate(_ context.Context, _ security.Grant, from, to wallet.Currency) (wallet.Rate, error) {
	p.mu.Lock()
	p.calls++
	numerator := int64(p.calls) + 1
	p.mu.Unlock()
	return wallet.Rate{
		From:        from,
		To:          to,
		Numerator:   numerator,
		Denominator: 1,
		QuotedAt:    quotedAt,
	}, nil
}

// Calls is how many times the provider has been asked.
func (p *driftingRate) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// First is the rate this provider answers with the first time it is asked.
func (p *driftingRate) First() int64 { return 2 }

// misquotedRate answers a rate for a pair other than the one it was asked
// about, which is the mistake a badly wired provider makes and the one whose
// number would otherwise be written into a wallet meaning something else.
type misquotedRate struct {
	from wallet.Currency
	to   wallet.Currency
}

// Rate ignores the pair it was asked about and answers about its own.
func (p misquotedRate) Rate(_ context.Context, _ security.Grant, _, _ wallet.Currency) (wallet.Rate, error) {
	return wallet.Rate{From: p.from, To: p.to, Numerator: 2, Denominator: 1, QuotedAt: quotedAt}, nil
}

// undatedRate answers a rate that does not say when it was quoted, which is a
// rate nothing can be reproduced against.
type undatedRate struct{}

// Rate answers a well formed fraction with no time on it.
func (undatedRate) Rate(_ context.Context, _ security.Grant, from, to wallet.Currency) (wallet.Rate, error) {
	return wallet.Rate{From: from, To: to, Numerator: 2, Denominator: 1}, nil
}

// errRateUnavailable is what a provider with no quote answers with.
var errRateUnavailable = errors.New("no rate for this pair")

// unavailableRate has no quote for anything.
type unavailableRate struct{}

// Rate refuses rather than guessing.
func (unavailableRate) Rate(_ context.Context, _ security.Grant, _, _ wallet.Currency) (wallet.Rate, error) {
	return wallet.Rate{}, errRateUnavailable
}

// Compile-time proof that every double answers the seam this package declares.
var (
	_ wallet.RateProvider = (*fixedRate)(nil)
	_ wallet.RateProvider = (*driftingRate)(nil)
	_ wallet.RateProvider = misquotedRate{}
	_ wallet.RateProvider = undatedRate{}
	_ wallet.RateProvider = unavailableRate{}
)

// openIn opens one wallet in a named currency at a named scale.
func openIn(t *testing.T, service *wallet.WalletService, holder, slug string, currency wallet.Currency, places int) *wallet.Wallet {
	t.Helper()

	record, err := service.Open(context.Background(), staff(), wallet.OpenRequest{
		HolderID:      holder,
		Slug:          slug,
		Name:          holder + " " + slug,
		Currency:      currency,
		DecimalPlaces: places,
	})
	if err != nil {
		t.Fatalf("opening the wallet %s/%s in %s: %v", holder, slug, currency, err)
	}
	return record
}
