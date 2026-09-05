package feature_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

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
