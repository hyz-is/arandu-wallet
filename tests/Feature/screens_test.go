package feature_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	fhttp "github.com/arandu-io/framework/http"
	"github.com/arandu-io/framework/security"
	hhttp "github.com/arandu-io/hesape/http"

	wallet "github.com/hyz-is/arandu-wallet"
)

// screen is what a page is drawn from, as a client that draws for itself is
// handed it: the name of the view and the values, with the addresses among
// them. Only the fields this file reads are declared.
type screen struct {
	View string `json:"view"`
	Data struct {
		Next    string
		NextURL string
		Rows    []struct{ ID, URL, StatementURL string }
		Wallet  struct{ ID, URL, StatementURL string }

		DepositURL    string
		WithdrawalURL string
		TransferURL   string
		CreditURL     string
		RefundURL     string
	} `json:"data"`
}

// TestEveryAddressOnAScreenIsTheRouteItLinksTo drives the three screens through
// the router, under a prefix of the installer's own, and reads every address
// the page data carries.
//
// The addresses are handed to the views whole, because the view compiler
// refuses a value written behind text in an address. They used to be composed
// in the markup -- the prefix, a slash, the identifier, the rest of the path --
// and each one below is required to be exactly what that composition wrote, so
// a page that published the new views links where the old ones did.
//
// The values are asked for through the media type a client that draws for
// itself sends, which answers with the data the markup would have been built
// from. That is the handler's own answer, so it holds which address went into
// which field, and it needs no compiled view in a package that is not an
// application.
func TestEveryAddressOnAScreenIsTheRouteItLinksTo(t *testing.T) {
	t.Parallel()

	const prefix = "/money"
	ctx := context.Background()
	sessions := security.NewSessionStore([]byte(appKey), time.Hour, false, security.NewMemoryBackend())
	module, err := wallet.New(wallet.Config{Tenant: tenant, Prefix: prefix, PageSize: 1, CSRF: csrf()}, database(t), sessions)
	if err != nil {
		t.Fatalf("building the module: %v", err)
	}
	router := fhttp.NewRouter()
	module.Routes(router.ForModule(module.Name()))

	// Two wallets and two movements, so that a page of one has a successor on
	// the listing and on the ledger alike.
	service := module.Service()
	alice := openWallet(t, service, "alice", "main", 2)
	openWallet(t, service, "bob", "main", 2)
	deposit(t, service, alice.ID, "screens-1", "10.00")
	deposit(t, service, alice.ID, "screens-2", "2.50")

	signedIn := httptest.NewRecorder()
	if _, err := sessions.Start(ctx, signedIn, staff()); err != nil {
		t.Fatalf("starting a session: %v", err)
	}
	cookies := signedIn.Result().Cookies()

	draw := func(target, want string) screen {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Accept", hhttp.ViewDataMediaType)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s answered %d: %s", target, rec.Code, rec.Body)
		}
		var got screen
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("GET %s did not answer with page data: %v\n%s", target, err, rec.Body)
		}
		if got.View != want {
			t.Fatalf("GET %s drew %q, want %q", target, got.View, want)
		}
		return got
	}
	same := func(field, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s is %q, want %q", field, got, want)
		}
	}

	listing := draw(prefix+"?holder_id=alice", wallet.ViewIndex)
	if len(listing.Data.Rows) != 1 {
		t.Fatalf("the listing narrowed to alice has %d row(s), want 1", len(listing.Data.Rows))
	}
	row := listing.Data.Rows[0]
	same("the listing's Rows[0].URL", row.URL, prefix+"/"+row.ID)
	same("the listing's Rows[0].StatementURL", row.StatementURL, prefix+"/"+row.ID+"/entries")

	everyone := draw(prefix, wallet.ViewIndex)
	if everyone.Data.Next == "" {
		t.Fatal("a full page of the listing offers no next page, so NextURL was never exercised")
	}
	same("the listing's NextURL", everyone.Data.NextURL, prefix+"?holder_id=&cursor="+everyone.Data.Next)
	draw(everyone.Data.NextURL, wallet.ViewIndex)

	operations := draw(row.URL, wallet.ViewOperations)
	id := operations.Data.Wallet.ID
	if id != alice.ID {
		t.Fatalf("%s drew wallet %s, want %s", row.URL, id, alice.ID)
	}
	same("the operations screen's Wallet.URL", operations.Data.Wallet.URL, prefix+"/"+id)
	same("the operations screen's Wallet.StatementURL", operations.Data.Wallet.StatementURL, prefix+"/"+id+"/entries")
	same("DepositURL", operations.Data.DepositURL, prefix+"/"+id+"/deposits")
	same("WithdrawalURL", operations.Data.WithdrawalURL, prefix+"/"+id+"/withdrawals")
	same("TransferURL", operations.Data.TransferURL, prefix+"/"+id+"/transfers")
	same("CreditURL", operations.Data.CreditURL, prefix+"/"+id+"/credit")
	same("RefundURL", operations.Data.RefundURL, prefix+"/purchases/refunds")

	statement := draw(operations.Data.Wallet.StatementURL, wallet.ViewStatement)
	same("the statement's Wallet.URL", statement.Data.Wallet.URL, prefix+"/"+id)
	if statement.Data.Next == "" {
		t.Fatal("a full page of the ledger offers no next page, so NextURL was never exercised")
	}
	same("the statement's NextURL", statement.Data.NextURL, prefix+"/"+id+"/entries?cursor="+statement.Data.Next)
	draw(statement.Data.NextURL, wallet.ViewStatement)

	// A short page is the last one, and it offers no successor in either field.
	nobody := draw(prefix+"?holder_id=nobody", wallet.ViewIndex)
	if nobody.Data.Next != "" || nobody.Data.NextURL != "" {
		t.Errorf("an empty listing offers a next page: Next %q, NextURL %q", nobody.Data.Next, nobody.Data.NextURL)
	}
}
