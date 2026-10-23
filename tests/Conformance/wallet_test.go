package conformance_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	fhttp "github.com/arandu-io/framework/http"
	"github.com/arandu-io/framework/http/middleware"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/test"
	hedatabase "github.com/arandu-io/hesape/database"
	_ "github.com/arandu-io/hesape/database/connectors/pgx"
	_ "github.com/arandu-io/hesape/database/connectors/sqlite"
	hhttp "github.com/arandu-io/hesape/http"

	wallet "github.com/hyz-is/arandu-wallet"
	"github.com/hyz-is/arandu-wallet/conformance"
)

// The kit run against the smallest application that installs the wallet the
// way bootstrap/app.go does: a session store whose resolver reads the
// accounts, the module built over that store and the test's database, its
// routes on a router behind the middleware that protects forms. What an
// application keeps in a users table this one keeps in a directory beside
// the server, which is where its resolver reads.

// appKey is any thirty-two bytes; an application reads its own from the
// environment.
const appKey = "0123456789abcdef0123456789abcdef"

func TestWalletOnSQLite(t *testing.T) {
	conformance.Wallet(t, func(t *testing.T) conformance.Fixture { return serve(t, test.SQLite()) })
}

// TestWalletOnPostgres runs on the server ARANDU_TEST_DATABASE_URL names, or
// ARANDU_TEST_POSTGRES_DSN, which is what the rest of this suite reads.
func TestWalletOnPostgres(t *testing.T) {
	url := os.Getenv("ARANDU_TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("ARANDU_TEST_POSTGRES_DSN")
	}
	if url == "" {
		t.Skip("no server: set ARANDU_TEST_DATABASE_URL or ARANDU_TEST_POSTGRES_DSN to run the kit on PostgreSQL")
	}
	conformance.Wallet(t, func(t *testing.T) conformance.Fixture { return serve(t, test.Database(url)) })
}

// directory is the application's accounts: what its resolver answers for each.
type directory struct {
	mu       sync.Mutex
	accounts map[string]security.Subject
	made     int
}

// resolve is the SubjectResolver: the account as it is now, or gone.
func (d *directory) resolve(_ context.Context, who security.Subject) (security.Subject, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	account, ok := d.accounts[who.ID]
	if !ok || account.Tenant != who.Tenant {
		return security.Subject{}, hhttp.ErrSubjectGone
	}
	return account, nil
}

// create makes an account in an Organization, which is the tenant here.
func (d *directory) create(organization string, operator bool) security.Subject {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.made++
	account := security.Subject{
		ID:       fmt.Sprintf("account-%d", d.made),
		Tenant:   "organization-" + organization,
		Verified: true,
	}
	if operator {
		account.Roles = []string{wallet.OperatorRole}
	}
	d.accounts[account.ID] = account
	return account
}

// migrate runs the wallet's migrations, the way `aru migrate` does.
func migrate(ctx context.Context, env test.Env) error {
	module, err := wallet.New(wallet.Config{Tenant: "guests"}, env.DB,
		security.NewSessionStore([]byte(appKey), time.Hour, false, security.NewMemoryBackend()))
	if err != nil {
		return err
	}
	connection := hedatabase.ForMigrations(hedatabase.NewConnection(env.DB.Unwrap(), "", "", map[string]any{
		"driver": string(env.DB.Dialect()),
		"name":   "wallet-conformance",
	}))
	for _, m := range module.Migrations() {
		if err := m.Up(ctx, connection); err != nil {
			return fmt.Errorf("applying %s: %w", m.GetName(), err)
		}
	}
	return nil
}

// serve boots the application over a fresh database and hands the kit what
// its composition built.
func serve(t *testing.T, engine test.Option) conformance.Fixture {
	t.Helper()

	accounts := &directory{accounts: map[string]security.Subject{}}
	var module *wallet.Module
	server := test.Serve(t, func(_ context.Context, env test.Env) (test.Application, error) {
		sessions := security.NewSessionStore([]byte(appKey), time.Hour, false, security.NewMemoryBackend())
		sessions.ResolveSubjects(accounts.resolve)

		var err error
		module, err = wallet.New(wallet.Config{Tenant: "guests"}, env.DB, sessions)
		if err != nil {
			return test.Application{}, err
		}
		router := fhttp.NewRouter()
		module.Routes(router.ForModule(module.Name()))
		csrf := security.NewCSRF([]byte(appKey), time.Hour).Secure(false)
		return test.Application{
			Handler:  middleware.CSRFProtect(csrf, sessions.IDFromRequest)(router),
			Sessions: sessions,
			Routes:   router.Routes(),
		}, nil
	}, test.Migrate(migrate), engine)

	return conformance.Fixture{
		Service: module.Service(),
		DB:      server.DB(),
		Account: func(_ *testing.T, organization string, operator bool) security.Subject {
			return accounts.create(organization, operator)
		},
		Server: server,
	}
}
