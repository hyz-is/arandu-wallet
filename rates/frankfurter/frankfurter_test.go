package frankfurter_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/cache"

	wallet "github.com/hyz-is/arandu-wallet"
	"github.com/hyz-is/arandu-wallet/rates/frankfurter"
)

// The suite runs against a recorded answer and not against the service.
//
// A test that needed the internet would be a test that fails when somebody
// else's server is down, on a change that touched nothing, and the thing it
// would be reporting is the weather. What is worth checking here is this
// package's own arithmetic and its classification of failures, and both are
// exact against a body that never changes. One test does talk to the real
// service, and it is skipped unless somebody asks for it by name.

// tenant is the customer every test runs as. It matters only where the cache
// does, because a Repository keys everything by it.
const tenant = "acme"

// grant is a Grant for the tenant above.
//
// It comes from the system rather than from a policy, because what is being
// tested has no records to decide about: this package reads a Grant for the
// tenant a cache key is built from and for nothing else.
func grant(t *testing.T) security.Grant {
	t.Helper()

	return security.SystemGrant("wallet.transfer", tenant)
}

// recorded serves one body, and counts how many times it was asked.
func recorded(t *testing.T, status int, file string) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("reading the recorded answer: %v", err)
	}
	var asked atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &asked
}

func provider(t *testing.T, cfg frankfurter.Config) *frankfurter.Provider {
	t.Helper()

	p, err := frankfurter.New(cfg)
	if err != nil {
		t.Fatalf("wiring the provider: %v", err)
	}
	return p
}

// TestAPublishedDecimalBecomesTheFractionItSpells is the one that matters.
//
// 5.4321 is 54321 over 10000, exactly. Read as a float64 first it would be
// 5.4320999999999998, and every amount converted through it would be wrong by
// the difference -- invisibly, and always in the same direction.
func TestAPublishedDecimalBecomesTheFractionItSpells(t *testing.T) {
	t.Parallel()

	server, asked := recorded(t, http.StatusOK, "latest.json")
	p := provider(t, frankfurter.Config{Endpoint: server.URL})

	rate, err := p.Rate(context.Background(), grant(t), "USD", "BRL")
	if err != nil {
		t.Fatalf("quoting: %v", err)
	}
	if rate.Numerator != 54321 || rate.Denominator != 10000 {
		t.Errorf("the rate is %d/%d, want 54321/10000", rate.Numerator, rate.Denominator)
	}
	if rate.From != "USD" || rate.To != "BRL" {
		t.Errorf("the rate converts %s into %s", rate.From, rate.To)
	}
	if want := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.UTC); !rate.QuotedAt.Equal(want) {
		t.Errorf("the rate is dated %s, want %s", rate.QuotedAt, want)
	}
	if err := rate.Validate(); err != nil {
		t.Errorf("the rate this package built is one the wallet refuses: %v", err)
	}
	if got := asked.Load(); got != 1 {
		t.Errorf("one quote asked the service %d times", got)
	}
}

// TestTheRateIsWhatTheWalletThenComputesWith closes the loop: the fraction is
// applied by the wallet, under the wallet's rounding rule, and comes out as the
// number the published figure says it should.
func TestTheRateIsWhatTheWalletThenComputesWith(t *testing.T) {
	t.Parallel()

	server, _ := recorded(t, http.StatusOK, "latest.json")
	p := provider(t, frankfurter.Config{Endpoint: server.URL})

	rate, err := p.Rate(context.Background(), grant(t), "USD", "BRL")
	if err != nil {
		t.Fatalf("quoting: %v", err)
	}

	// Ten dollars at two places, into reais at two places: 1000 * 54321 / 10000
	// is 5432.1, truncated to 5432.
	converted, err := rate.Convert(wallet.Money{Amount: 1000, Currency: "USD", DecimalPlaces: 2}, "BRL", 2)
	if err != nil {
		t.Fatalf("converting: %v", err)
	}
	if converted.Money.Amount != 5432 {
		t.Errorf("ten dollars came to %d, want 5432", converted.Money.Amount)
	}
	if converted.Exact() {
		t.Error("the conversion says it divided evenly, and a tenth of a centavo was left over")
	}
}

func TestTheSameCurrencyIsOneToOneAndAsksNobody(t *testing.T) {
	t.Parallel()

	server, asked := recorded(t, http.StatusOK, "latest.json")
	p := provider(t, frankfurter.Config{Endpoint: server.URL})

	rate, err := p.Rate(context.Background(), grant(t), "USD", "USD")
	if err != nil {
		t.Fatalf("quoting: %v", err)
	}
	if rate.Numerator != 1 || rate.Denominator != 1 {
		t.Errorf("the rate is %d/%d, want 1/1", rate.Numerator, rate.Denominator)
	}
	if got := asked.Load(); got != 0 {
		t.Errorf("a pair of one currency asked the service %d times", got)
	}
}

// TestWhatCannotBeQuotedIsSaidInTheWalletsOwnValues holds the classification.
// Each answer is a different thing to do, and the caller reads them against the
// wallet package rather than against this one.
func TestWhatCannotBeQuotedIsSaidInTheWalletsOwnValues(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		status int
		file   string
		want   error
	}{
		{"a pair the service does not publish", http.StatusOK, "unknown.json", wallet.ErrRatePairUnknown},
		{"a pair it answers 404 for", http.StatusNotFound, "unknown.json", wallet.ErrRatePairUnknown},
		{"a request it will not take", http.StatusBadRequest, "unknown.json", wallet.ErrRateRequestRefused},
		{"a service that is rate limiting", http.StatusTooManyRequests, "unknown.json", wallet.ErrRateProviderUnavailable},
		{"a service that has fallen over", http.StatusBadGateway, "unknown.json", wallet.ErrRateProviderUnavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			server, _ := recorded(t, c.status, c.file)
			p := provider(t, frankfurter.Config{Endpoint: server.URL})

			_, err := p.Rate(context.Background(), grant(t), "USD", "BRL")
			if !errors.Is(err, c.want) {
				t.Fatalf("answered %v, want %v", err, c.want)
			}
		})
	}
}

func TestAnEmptyCurrencyIsRefusedBeforeAnybodyIsAsked(t *testing.T) {
	t.Parallel()

	server, asked := recorded(t, http.StatusOK, "latest.json")
	p := provider(t, frankfurter.Config{Endpoint: server.URL})

	if _, err := p.Rate(context.Background(), grant(t), "", "BRL"); !errors.Is(err, wallet.ErrRateRequestRefused) {
		t.Fatalf("answered %v, want ErrRateRequestRefused", err)
	}
	if got := asked.Load(); got != 0 {
		t.Errorf("a request with no currency reached the service %d times", got)
	}
}

// TestASlowServiceIsGivenUpOn holds the deadline. The wallet quotes before it
// opens its transaction, so a slow answer holds no row lock -- but only while
// something ends the wait.
func TestASlowServiceIsGivenUpOn(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	p := provider(t, frankfurter.Config{Endpoint: server.URL, Timeout: 50 * time.Millisecond})

	start := time.Now()
	_, err := p.Rate(context.Background(), grant(t), "USD", "BRL")
	if !errors.Is(err, wallet.ErrRateProviderUnavailable) {
		t.Fatalf("answered %v, want ErrRateProviderUnavailable", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the quote waited %s, and the deadline was 50ms", waited)
	}
}

// TestTheDeadlineIsThisPackagesAndNotTheClients holds the half a caller can get
// wrong: a client handed in with a longer timeout of its own does not lengthen
// a quote.
func TestTheDeadlineIsThisPackagesAndNotTheClients(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	p := provider(t, frankfurter.Config{
		Endpoint: server.URL,
		Timeout:  50 * time.Millisecond,
		Client:   &http.Client{Timeout: time.Hour},
	})

	start := time.Now()
	if _, err := p.Rate(context.Background(), grant(t), "USD", "BRL"); err == nil {
		t.Fatal("a quote against a server that never answers came back with a rate")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the quote waited %s, so the client's hour won over this package's 50ms", waited)
	}
}

func TestATimeoutNobodyCanServeIsRefusedWhereItIsWired(t *testing.T) {
	t.Parallel()

	if _, err := frankfurter.New(frankfurter.Config{Timeout: 2 * frankfurter.MaxTimeout}); err == nil {
		t.Error("a timeout above the maximum was accepted, and a wiring mistake is worth one restart")
	}
	if _, err := frankfurter.New(frankfurter.Config{Timeout: -time.Second}); err == nil {
		t.Error("a negative timeout was accepted")
	}
	if _, err := frankfurter.New(frankfurter.Config{TTL: -time.Second}); err == nil {
		t.Error("a negative cache lifetime was accepted")
	}
}

// TestAKeptQuoteIsTheSameFractionAndAsksOnce holds the cache: it is the
// application's own, it is keyed per tenant, and what comes back out of it is
// the fraction that went in rather than a decimal read a second time.
func TestAKeptQuoteIsTheSameFractionAndAsksOnce(t *testing.T) {
	t.Parallel()

	server, asked := recorded(t, http.StatusOK, "latest.json")
	held := cache.New(cache.NewArrayStore()).Namespace("rates")
	p := provider(t, frankfurter.Config{Endpoint: server.URL, Cache: held, TTL: time.Hour})

	g := grant(t)
	first, err := p.Rate(context.Background(), g, "USD", "BRL")
	if err != nil {
		t.Fatalf("quoting: %v", err)
	}
	second, err := p.Rate(context.Background(), g, "USD", "BRL")
	if err != nil {
		t.Fatalf("quoting again: %v", err)
	}

	if got := asked.Load(); got != 1 {
		t.Errorf("two quotes asked the service %d times, want 1", got)
	}
	if first.Numerator != second.Numerator || first.Denominator != second.Denominator ||
		!first.QuotedAt.Equal(second.QuotedAt) {
		t.Errorf("the kept quote is %s and the fetched one was %s", second, first)
	}
}

// TestTheLiveServiceStillAnswersWhatThisReads is the only test that needs the
// internet, and it is skipped unless somebody asks for it.
//
// It exists because a recorded body proves this package reads that shape and
// says nothing about whether the service still publishes it. Somebody about to
// release runs it; nobody's pull request is failed by somebody else's outage.
func TestTheLiveServiceStillAnswersWhatThisReads(t *testing.T) {
	if os.Getenv("ARANDU_TEST_LIVE_RATES") == "" {
		t.Skip("no live call: set ARANDU_TEST_LIVE_RATES=1 to ask the real service whether it still publishes what this reads")
	}

	p := provider(t, frankfurter.Config{Timeout: 10 * time.Second})
	rate, err := p.Rate(context.Background(), grant(t), "USD", "BRL")
	if err != nil {
		t.Fatalf("quoting the live service: %v", err)
	}
	if err := rate.Validate(); err != nil {
		t.Fatalf("the live service answered a rate the wallet refuses: %v", err)
	}
	fmt.Printf("live: %s quoted at %s\n", rate, rate.QuotedAt.Format(time.DateOnly))
}
