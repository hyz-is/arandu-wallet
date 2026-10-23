package feature_test

import (
	"context"
	"strings"
	"testing"

	"github.com/arandu-io/framework/foundation"
	hedatabase "github.com/arandu-io/hesape/database"
	"github.com/arandu-io/hesape/database/migrations"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A database migrated before the framework's default time precision was
// microseconds holds the wallet's instants in whole seconds. The widening
// migration raises every one of them in place, and the money stays where it
// was.

// widening is the migration that raises the wallet's instants to microseconds.
func widening(t *testing.T, all []foundation.Migration) migrations.WidenTimePrecision {
	t.Helper()
	for _, migration := range all {
		if widen, ok := migration.(migrations.WidenTimePrecision); ok && widen.GetName() == "20261019_0024_widen_wallet_time_precision" {
			return widen
		}
	}
	t.Fatal("no migration widens the wallet's instants")
	return migrations.WidenTimePrecision{}
}

func TestAMigrationWidensTheWalletInstants(t *testing.T) {
	t.Parallel()

	module, err := wallet.New(wallet.Config{Tenant: tenant}, database(t), sessionStore())
	if err != nil {
		t.Fatal(err)
	}
	widen := widening(t, module.Migrations())
	if len(widen.Tables) != 11 {
		t.Fatalf("the widening names %d tables, want the module's 11", len(widen.Tables))
	}
}

func TestWideningKeepsTheLedgerOnPostgres(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := postgres(t)
	service := wallet.NewWalletService(db, nil, nil, nil)

	// The shape an older release left: every date-time column in seconds.
	rows, err := db.QueryContext(ctx, `SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type LIKE 'timestamp%'`)
	if err != nil {
		t.Fatal(err)
	}
	var narrow []string
	for rows.Next() {
		var table, column, kind string
		if err := rows.Scan(&table, &column, &kind); err != nil {
			t.Fatal(err)
		}
		zone := "without"
		if strings.Contains(kind, "with time zone") {
			zone = "with"
		}
		narrow = append(narrow, `ALTER TABLE "`+table+`" ALTER COLUMN "`+column+`" TYPE timestamp(0) `+zone+` time zone`)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if len(narrow) == 0 {
		t.Fatal("the wallet tables have no date-time column, so this test measures nothing")
	}
	for _, statement := range narrow {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "fund", "10.00")

	module, err := wallet.New(wallet.Config{Tenant: tenant}, db, sessionStore())
	if err != nil {
		t.Fatal(err)
	}
	widen := widening(t, module.Migrations())
	conn := hedatabase.ForMigrations(hedatabase.NewConnection(db.Unwrap(), "", "", map[string]any{
		"driver": string(hedatabase.DialectPostgres), "name": "wallet-test",
	}))
	if err := widen.Up(ctx, conn); err != nil {
		t.Fatalf("widening: %v", err)
	}
	// A connection that prepared a statement over the narrow columns has a
	// cached plan whose result type the widening changed, and PostgreSQL
	// refuses to run it; a deploy starts new connections after it migrates,
	// and so does this.
	db.Unwrap().SetMaxIdleConns(0)
	db.Unwrap().SetMaxIdleConns(2)

	var left int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type LIKE 'timestamp%' AND datetime_precision < 6`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d date-time columns are still in seconds after the widening", left)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("the balance is %d after the widening, want 1000", got)
	}
	deposit(t, service, account.ID, "more", "1.00")
	if got, ledger := balanceOf(t, service, account.ID), ledgerOf(t, service, account.ID); got != 1100 || ledger != 1100 {
		t.Fatalf("after the widening the balance is %d and the ledger %d, want 1100", got, ledger)
	}
}
