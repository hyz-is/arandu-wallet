package feature_test

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/arandu-io/framework/data"
	fhttp "github.com/arandu-io/framework/http"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/database/migrations"

	wallet "github.com/hyz-is/arandu-wallet"
)

// These tests drive the module the way an application does: build it, register
// its routes on a router, and make a request.
//
// The database handle wraps nothing, and that is the assertion. A request that
// reached a statement would panic, so every answer below is proof that the
// refusal happened in the policy and not after a read.

// appKey is the key a session store is built over. Any thirty-two bytes will
// do here; a real application reads its own from the environment.
const appKey = "0123456789abcdef0123456789abcdef"

// reservedPrefix is the namespace the framework keeps for itself: the health
// probe, the reload endpoint, the development console, the addressed assets.
//
// A module that registers under it is refused when the application boots, by
// name -- and that refusal happens in the process of whoever installed this
// package, after it was published. Here the same rule is a failing test, in the
// repository that can still fix it.
const reservedPrefix = "/_arandu"

// csrf is the token issuer every configuration needs. Every screen this module
// draws moves money, so a page with no token is a page whose buttons the
// application refuses.
func csrf() *security.CSRF { return security.NewCSRF([]byte(appKey), time.Hour) }

// mount builds the module and returns a router with its routes registered.
func mount(t *testing.T, cfg wallet.Config) *fhttp.Router {
	t.Helper()

	if cfg.CSRF == nil {
		cfg.CSRF = csrf()
	}
	sessions := security.NewSessionStore([]byte(appKey), time.Hour, false, security.NewMemoryBackend())

	module, err := wallet.New(cfg, data.Wrap(nil, data.DialectSQLite), sessions)
	if err != nil {
		t.Fatalf("building the module: %v", err)
	}

	router := fhttp.NewRouter()
	module.Routes(router.ForModule(module.Name()))
	return router
}

// answer makes one request against the router and returns the recorder. Every
// movement of money carries an idempotency key, so the header goes on every
// request rather than on the three that need it: a request refused for the want
// of a key would be a 422 that proves nothing about the policy.
func answer(t *testing.T, router *fhttp.Router, method, target string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(wallet.IdempotencyHeader, "key-1")
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAVisitorWithNoSessionReachesNothing(t *testing.T) {
	t.Parallel()

	router := mount(t, wallet.Config{Tenant: "acme"})

	for _, request := range []struct {
		method string
		target string
		body   string
	}{
		{http.MethodGet, wallet.DefaultPrefix, ""},
		{http.MethodGet, wallet.DefaultPrefix + "/wallet-1", ""},
		{http.MethodGet, wallet.DefaultPrefix + "/wallet-1/entries", ""},
		{http.MethodPost, wallet.DefaultPrefix, "holder_id=user-1&slug=main&name=Main&currency=BRL"},
		{http.MethodPost, wallet.DefaultPrefix + "/wallet-1/deposits", "amount=1.00"},
		{http.MethodPost, wallet.DefaultPrefix + "/wallet-1/withdrawals", "amount=1.00"},
		{http.MethodPost, wallet.DefaultPrefix + "/wallet-1/transfers", "to_wallet_id=wallet-2&amount=1.00"},
		{http.MethodPost, wallet.DefaultPrefix + "/operations/operation-1/reversals", "reason=chargeback"},
		{http.MethodPost, wallet.DefaultPrefix + "/operations/operation-1/confirmations", ""},
		{http.MethodPut, wallet.DefaultPrefix + "/wallet-1/credit", "limit=10.00"},
	} {
		rec := answer(t, router, request.method, request.target, request.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s answered %d, want %d", request.method, request.target, rec.Code, http.StatusForbidden)
		}
	}
}

func TestARejectedInputIsAnsweredBeforeTheDatabase(t *testing.T) {
	t.Parallel()

	router := mount(t, wallet.Config{Tenant: "acme"})

	// The input is validated before anything is authorized, so this is the one
	// refusal that arrives as 422 rather than 403 -- and it still never reaches
	// a statement.
	rec := answer(t, router, http.MethodPost, wallet.DefaultPrefix, "holder_id=&slug=&name=&currency=")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an empty wallet answered %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}

	// A scale that is not a number is refused the same way, and before the
	// policy: a wallet opened at the wrong scale reinterprets every amount ever
	// written to it.
	rec = answer(t, router, http.MethodPost, wallet.DefaultPrefix,
		"holder_id=user-1&slug=main&name=Main&currency=BRL&decimal_places=two")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a scale that is not a number answered %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
}

func TestTheModuleRegistersItsRoutesUnderItsPrefix(t *testing.T) {
	t.Parallel()

	router := mount(t, wallet.Config{Tenant: "acme", Prefix: "/widgets"})

	got := make([]string, 0, 8)
	for _, route := range router.Routes() {
		if route.Module != "wallet" {
			t.Errorf("the route %s %s is not tagged with the module name: %q", route.Method, route.Pattern, route.Module)
		}
		got = append(got, route.Method+" "+route.Pattern)
	}
	sort.Strings(got)

	// The whole surface, spelled out. Module.Routes attaches a handler to each
	// address by name and registers nothing for a name it has no handler for,
	// so a route that lost its handler disappears from this list rather than
	// answering with a panic.
	want := []string{
		"GET /widgets",
		"GET /widgets/holders/{holder}/{slug}",
		"GET /widgets/{id}",
		"GET /widgets/{id}/entries",
		"GET /widgets/{id}/purchases",
		"POST /widgets",
		"POST /widgets/operations/{operation}/confirmations",
		"POST /widgets/operations/{operation}/reversals",
		"POST /widgets/purchases/refunds",
		"POST /widgets/{id}/deposits",
		"POST /widgets/{id}/transfers",
		"POST /widgets/{id}/withdrawals",
		"PUT /widgets/{id}/credit",
		"PUT /widgets/{id}/description",
	}
	if len(got) != len(want) {
		t.Fatalf("registered %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered %v, want %v", got, want)
		}
	}
}

// TestNoRouteLandsInTheFrameworkNamespace is the one property of this package
// that syntax cannot hold, and it is why it is checked here rather than beside
// the other four in tests/Unit/audit_test.go: a prefix arrives through
// configuration, so the only way to know where the routes ended up is to
// register them and read the table back.
//
// Both the default and a configured prefix are mounted, because the two reach
// the router by different paths and only one of them is written in this
// repository.
func TestNoRouteLandsInTheFrameworkNamespace(t *testing.T) {
	t.Parallel()

	for _, cfg := range []wallet.Config{
		{Tenant: "acme"},
		{Tenant: "acme", Prefix: "/widgets"},
	} {
		router := mount(t, cfg)

		registered := 0
		for _, route := range router.Routes() {
			registered++
			if route.Pattern == reservedPrefix || strings.HasPrefix(route.Pattern, reservedPrefix+"/") {
				t.Errorf("the route %s %s is registered under %s/, which the framework keeps for itself and refuses at boot",
					route.Method, route.Pattern, reservedPrefix)
			}
		}
		if registered == 0 {
			t.Fatal("the module registered no route, so this test proved nothing")
		}
	}
}

func TestNewRefusesAWiringThatCannotWork(t *testing.T) {
	t.Parallel()

	sessions := security.NewSessionStore([]byte(appKey), time.Hour, false, security.NewMemoryBackend())
	handle := data.Wrap(nil, data.DialectSQLite)
	valid := wallet.Config{Tenant: "acme", CSRF: csrf()}

	if _, err := wallet.New(wallet.Config{}, handle, sessions); err == nil {
		t.Error("a configuration with no tenant was accepted")
	}
	if _, err := wallet.New(valid, nil, sessions); err == nil {
		t.Error("a nil database handle was accepted")
	}
	if _, err := wallet.New(valid, handle, nil); err == nil {
		t.Error("a nil session store was accepted")
	}
	if _, err := wallet.New(wallet.Config{Tenant: "acme"}, handle, sessions); err == nil {
		t.Error("a configuration with no token issuer was accepted, and every form it draws would be refused")
	}
	if _, err := wallet.New(valid, handle, sessions); err != nil {
		t.Fatalf("a valid wiring was refused: %v", err)
	}
}

func TestNewRefusesARoutePrefixThatCannotBeRegistered(t *testing.T) {
	t.Parallel()

	sessions := security.NewSessionStore([]byte(appKey), time.Hour, false, security.NewMemoryBackend())
	handle := data.Wrap(nil, data.DialectSQLite)

	for _, prefix := range []string{"/widgets{", "/widgets/{id}"} {
		if _, err := wallet.New(wallet.Config{Tenant: "acme", Prefix: prefix, CSRF: csrf()}, handle, sessions); err == nil {
			t.Errorf("New accepted route prefix %q, which would panic during route registration", prefix)
		}
	}
}

func TestTheModuleDeclaresItsSchema(t *testing.T) {
	t.Parallel()

	sessions := security.NewSessionStore([]byte(appKey), time.Hour, false, security.NewMemoryBackend())
	module, err := wallet.New(wallet.Config{Tenant: "acme", CSRF: csrf()}, data.Wrap(nil, data.DialectSQLite), sessions)
	if err != nil {
		t.Fatalf("building the module: %v", err)
	}

	declared := module.Migrations()
	if len(declared) == 0 {
		t.Fatal("the module declares migrations = true and returns none")
	}

	names := make([]string, 0, len(declared))
	for _, migration := range declared {
		name := migration.GetName()
		if name == "" {
			t.Fatal("a migration has no name, and the name is what carries the order")
		}
		names = append(names, name)

		// A migration that cannot be rolled back is a deploy that cannot be
		// undone. The migrator finds Down by type assertion, so a Down with the
		// wrong signature is a rollback that silently does nothing.
		if _, ok := migration.(migrations.ReversibleMigration); !ok {
			t.Errorf("the migration %s has no Down", name)
		}
	}

	if !sort.StringsAreSorted(names) {
		t.Fatalf("the migrations are not returned in the order their names sort in: %v", names)
	}
}
