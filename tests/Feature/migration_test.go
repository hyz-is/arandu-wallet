package feature_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/arandu-io/framework/data"
	hedatabase "github.com/arandu-io/hesape/database"
	sqliteconnector "github.com/arandu-io/hesape/database/connectors/sqlite"
	"github.com/arandu-io/hesape/database/migrations"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The schema a release adds goes on and comes off: a migration whose Down does
// not run is a rollback that fails on the day it is needed.

func TestTheLatestMigrationsRollBackAndApplyAgain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wallet.sqlite")
	handle, err := sql.Open(sqliteconnector.SQLiteConnector{}.DriverName(), "file:"+path+dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	conn := hedatabase.ForMigrations(hedatabase.NewConnection(handle, path, "", map[string]any{
		"driver": string(hedatabase.DialectSQLite), "name": "wallet-test",
	}))
	module, err := wallet.New(wallet.Config{Tenant: tenant}, data.Wrap(handle, data.DialectSQLite), sessionStore())
	if err != nil {
		t.Fatal(err)
	}

	all := module.Migrations()
	byName := map[string]int{}
	for i, migration := range all {
		byName[migration.GetName()] = i
	}
	captured := byName["20261019_0022_add_wallet_hold_captured_amount"]
	if captured == 0 {
		t.Fatal("the migration that records what a capture took is not declared")
	}

	// Everything before it, and a hold captured before a capture could take
	// part of one.
	for _, migration := range all[:captured] {
		if err := migration.Up(ctx, conn); err != nil {
			t.Fatalf("applying %s: %v", migration.GetName(), err)
		}
	}
	if _, err := handle.ExecContext(ctx, `insert into wallet_holds
		(id, tenant_id, wallet_id, idempotency_key, amount, status, created_at, updated_at)
		values ('hold-1', 'acme', 'wallet-1', 'key-1', 700, 'captured', '2026-10-01 00:00:00', '2026-10-01 00:00:00'),
		       ('hold-2', 'acme', 'wallet-1', 'key-2', 300, 'released', '2026-10-01 00:00:00', '2026-10-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range all[captured:] {
		if err := migration.Up(ctx, conn); err != nil {
			t.Fatalf("applying %s: %v", migration.GetName(), err)
		}
	}
	for id, want := range map[string]int64{"hold-1": 700, "hold-2": 0} {
		var got int64
		if err := handle.QueryRowContext(ctx, `select captured_amount from wallet_holds where id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s records %d captured, want %d", id, got, want)
		}
	}

	latest := []string{
		"20261019_0023_add_wallet_period_indexes",
		"20261019_0022_add_wallet_hold_captured_amount",
		"20261019_0021_add_wallet_operation_settlement",
	}
	for _, name := range latest {
		reversible, ok := all[byName[name]].(migrations.ReversibleMigration)
		if !ok {
			t.Fatalf("%s cannot be rolled back", name)
		}
		if err := reversible.Down(ctx, conn); err != nil {
			t.Fatalf("rolling back %s: %v", name, err)
		}
	}
	for i := len(latest) - 1; i >= 0; i-- {
		if err := all[byName[latest[i]]].Up(ctx, conn); err != nil {
			t.Fatalf("applying %s again: %v", latest[i], err)
		}
	}
}
