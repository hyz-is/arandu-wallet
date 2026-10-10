package feature_test

import (
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/foundation"
	"github.com/arandu-io/framework/foundation/bootstrap"
	fhttp "github.com/arandu-io/framework/http"
	"github.com/arandu-io/framework/http/middleware"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/config"
	hhttp "github.com/arandu-io/hesape/http"
	"github.com/arandu-io/hesape/view"

	wallet "github.com/hyz-is/arandu-wallet"
)

// These tests serve the screens the way an application does: behind the
// middleware that protects forms, over a migrated database, with the policy
// this package ships. What they hold is the round trip a person makes -- load
// a screen, submit the form it drew -- and the requests that must still be
// turned away.
//
// The renderer below stands in for the application's layout, and it draws what
// that layout draws from the page: the brand link, the sign-in link while nobody
// is signed in, the sign-out link while somebody is, and the hidden _token field
// @csrf writes. It reads them through view.Layout, the interface the layout
// itself is compiled against, so a field the layout would show empty is shown
// empty here.

// chrome is the application's layout, reduced to the parts these tests read.
type chrome struct{}

func (chrome) Render(_ context.Context, w http.ResponseWriter, status int, name string, data any) error {
	page, ok := data.(view.Layout)
	if !ok {
		return fmt.Errorf("%s was handed %T, which the layout cannot draw", name, data)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	var b strings.Builder
	fmt.Fprintf(&b, "<a data-brand href=\"%s\">%s</a>\n", html.EscapeString(page.HomeLink()), html.EscapeString(page.BrandName()))
	if page.SignedIn() {
		fmt.Fprintf(&b, "<a data-logout href=\"%s\">Sign out</a>\n", html.EscapeString(page.LogoutLink()))
	} else {
		fmt.Fprintf(&b, "<a data-login href=\"%s\">Sign in</a>\n", html.EscapeString(page.LoginLink()))
	}
	fmt.Fprintf(&b, "<form method=\"post\"><input type=\"hidden\" name=\"_token\" value=\"%s\"></form>\n", html.EscapeString(page.CSRFToken()))

	// The operations screen's forms that move money, each with the address it
	// posts to and the idempotency key it carries, as the published markup
	// draws them from the same fields.
	if screen, ok := data.(wallet.OperationsPageData); ok {
		forms := []struct{ name, action, key string }{
			{"deposit", screen.DepositURL, screen.DepositKey},
			{"withdrawal", screen.WithdrawalURL, screen.WithdrawalKey},
			{"transfer", screen.TransferURL, screen.TransferKey},
		}
		for _, line := range screen.Purchases {
			if !line.Refunded {
				forms = append(forms, struct{ name, action, key string }{"refund-" + line.ID, screen.RefundURL, line.RefundKey})
			}
		}
		for _, form := range forms {
			fmt.Fprintf(&b, "<form data-form=\"%s\" method=\"post\" action=\"%s\"><input type=\"hidden\" name=\"%s\" value=\"%s\"></form>\n",
				html.EscapeString(form.name), html.EscapeString(form.action), wallet.IdempotencyField, html.EscapeString(form.key))
		}
	}
	_, err := w.Write([]byte(b.String()))
	return err
}

// servedWallet is one served instance: the handler the server would run, the
// session store a sign-in writes to, and the module behind both.
type servedWallet struct {
	handler  http.Handler
	sessions *security.SessionStore
	module   *wallet.Module
}

// serveWallet builds the application. withNavigation registers the routes the
// layout links to, under the names the application skeleton gives them; without
// it the application has none of them, which is the case where no link is the
// right answer.
//
// The home route draws a form of the application's own, with the token the
// middleware issued for the request. It is where a visitor with no session gets
// a token at all: no screen of this package is drawn for a guest, because there
// is no money a guest owns.
func serveWallet(t *testing.T, withNavigation bool) servedWallet {
	t.Helper()

	sessions := security.NewSessionStore([]byte(appKey), time.Hour, false, security.NewMemoryBackend())
	module, err := wallet.New(wallet.Config{Tenant: tenant}, database(t), sessions)
	if err != nil {
		t.Fatalf("building the module: %v", err)
	}

	router := fhttp.NewRouter().WithRenderer(chrome{})
	if withNavigation {
		home := func(w http.ResponseWriter, r *http.Request) {
			token, _ := hhttp.CSRFTokenFrom(r.Context())
			fmt.Fprintf(w, "<form method=\"post\"><input type=\"hidden\" name=\"_token\" value=\"%s\"></form>\n", html.EscapeString(token))
		}
		nothing := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
		router.Get("/{$}", home).Name("home")
		router.Get("/auth/login", nothing).Name("auth.login")
		router.Post("/auth/logout", nothing).Name("auth.logout")
	}
	module.Routes(router.ForModule(module.Name()))

	// The middleware the application skeleton mounts, built the way it builds
	// it: over the session store's own reader of the session cookie.
	csrf := security.NewCSRF([]byte(appKey), time.Hour).Secure(false)
	return servedWallet{
		handler:  middleware.CSRFProtect(csrf, sessions.IDFromRequest)(router),
		sessions: sessions,
		module:   module,
	}
}

// visit makes one request carrying the cookies given, and returns the answer.
func (a servedWallet) visit(t *testing.T, method, target string, form url.Values, cookies []*http.Cookie, header http.Header) *httptest.ResponseRecorder {
	t.Helper()

	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

// signIn starts a session for the subject and answers the cookies a browser
// would keep from the answer.
func (a servedWallet) signIn(t *testing.T, subject security.Subject) []*http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if _, err := a.sessions.Start(context.Background(), rec, subject); err != nil {
		t.Fatalf("signing in: %v", err)
	}
	return rec.Result().Cookies()
}

// draw loads a screen and answers the body, the token its form carries and the
// cookies the browser holds afterwards.
func (a servedWallet) draw(t *testing.T, target string, cookies []*http.Cookie) (string, string, []*http.Cookie) {
	t.Helper()

	rec := a.visit(t, http.MethodGet, target, nil, cookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s answered %d: %s", target, rec.Code, rec.Body)
	}
	body := rec.Body.String()
	return body, hiddenField(t, body, "_token"), append(append([]*http.Cookie(nil), cookies...), rec.Result().Cookies()...)
}

// hiddenField is the value of the hidden input called name in a drawn screen.
func hiddenField(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the screen draws no %s field:\n%s", name, body)
	}
	return html.UnescapeString(m[1])
}

// link is the target of the link marked with attr in a drawn screen, and
// whether the screen drew that link at all.
func link(body, attr string) (string, bool) {
	m := regexp.MustCompile(`<a ` + regexp.QuoteMeta(attr) + ` href="([^"]*)"`).FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	return html.UnescapeString(m[1]), true
}

// wallets counts the wallets of the tenant, read as the operator.
func (a servedWallet) wallets(t *testing.T) int {
	t.Helper()
	records, err := a.module.Service().List(context.Background(), staff(), wallet.ListRequest{Query: data.Query{Limit: wallet.MaxPageSize}})
	if err != nil {
		t.Fatalf("listing what the forms wrote: %v", err)
	}
	return len(records)
}

// opening is what the form on the listing sends to open a wallet for holder.
func opening(holder, token string) url.Values {
	form := url.Values{
		"holder_id":      {holder},
		"slug":           {"main"},
		"name":           {"Main"},
		"currency":       {"BRL"},
		"decimal_places": {"2"},
	}
	if token != "" {
		form.Set("_token", token)
	}
	return form
}

// succeeded reports whether a submission was taken: a page or a redirect, and
// neither a refusal of the pipeline nor one of the policy.
func succeeded(code int) bool { return code >= 200 && code < 400 }

func TestAGuestFormPassesThePipelineAndStopsAtThePolicy(t *testing.T) {
	t.Parallel()

	app := serveWallet(t, true)

	// A guest asking for the listing is answered by the policy, which has no
	// rule that lets a visitor with no session see money. A GET is never
	// refused for a token, so the refusal is the policy's.
	if rec := app.visit(t, http.MethodGet, wallet.DefaultPrefix, nil, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a guest's GET of the listing answered %d, want the policy's %d", rec.Code, http.StatusForbidden)
	}

	// The token a guest holds comes from a page of the application, bound to
	// the guest cookie set with it.
	home := app.visit(t, http.MethodGet, "/", nil, nil, nil)
	if home.Code != http.StatusOK {
		t.Fatalf("the application's home answered %d: %s", home.Code, home.Body)
	}
	token := hiddenField(t, home.Body.String(), "_token")
	if token == "" {
		t.Fatal("the application drew an empty token for a guest, so the submissions below prove nothing")
	}
	cookies := home.Result().Cookies()

	// Without it the middleware decides, and answers 419.
	if rec := app.visit(t, http.MethodPost, wallet.DefaultPrefix, opening("guest-1", ""), cookies, nil); rec.Code != middleware.StatusCSRFExpired {
		t.Fatalf("a guest's submission with no token answered %d, want %d", rec.Code, middleware.StatusCSRFExpired)
	}

	// With it the request passes the middleware and reaches the module, and the
	// policy turns it away there. It carries no Origin and no Sec-Fetch-Site,
	// so the middleware's own 403 for a cross-site page cannot be the one.
	rec := app.visit(t, http.MethodPost, wallet.DefaultPrefix, opening("guest-1", token), cookies, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a guest's submission with the token the pipeline issued answered %d, want the policy's %d: %s", rec.Code, http.StatusForbidden, rec.Body)
	}
	if got := app.wallets(t); got != 0 {
		t.Fatalf("a guest's submissions wrote %d wallet(s), want none", got)
	}
}

func TestAForgedSubmissionIsStillRefused(t *testing.T) {
	t.Parallel()

	app := serveWallet(t, true)
	session := app.signIn(t, person("user-1"))
	_, token, cookies := app.draw(t, wallet.DefaultPrefix, session)
	if token == "" {
		t.Fatal("the screen drew no token, so the refusals below would prove nothing about one")
	}
	// Another person's browser, signed in to a session of its own.
	stranger := app.signIn(t, person("user-2"))

	for _, forged := range []struct {
		why     string
		form    url.Values
		cookies []*http.Cookie
		header  http.Header
		want    int
	}{
		{"no token", opening("user-1", ""), cookies, nil, middleware.StatusCSRFExpired},
		{"a token and not the browser it was issued to", opening("user-1", token), nil, nil, middleware.StatusCSRFExpired},
		{"a token issued to another browser", opening("user-2", token), stranger, nil, middleware.StatusCSRFExpired},
		{"a token that was altered", opening("user-1", token+"x"), cookies, nil, middleware.StatusCSRFExpired},
		{"a page of another site", opening("user-1", token), cookies, http.Header{"Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
	} {
		rec := app.visit(t, http.MethodPost, wallet.DefaultPrefix, forged.form, forged.cookies, forged.header)
		if rec.Code != forged.want {
			t.Errorf("a submission with %s answered %d, want %d", forged.why, rec.Code, forged.want)
		}
	}
	if got := app.wallets(t); got != 0 {
		t.Fatalf("the forged submissions wrote %d wallet(s), want none", got)
	}
}

func TestASignedInHolderSubmitsTheFormsTheScreensDrew(t *testing.T) {
	t.Parallel()

	app := serveWallet(t, true)
	session := app.signIn(t, person("user-1"))

	body, token, cookies := app.draw(t, wallet.DefaultPrefix, session)
	if _, drawn := link(body, "data-login"); drawn {
		t.Error("the listing offers a signed-in holder the sign-in link")
	}
	if token == "" {
		t.Fatal("the listing drew an empty token for a signed-in holder")
	}

	// A guest's token is bound to a guest, and is refused on the session.
	guest := app.visit(t, http.MethodGet, "/", nil, nil, nil)
	guestToken := hiddenField(t, guest.Body.String(), "_token")
	if rec := app.visit(t, http.MethodPost, wallet.DefaultPrefix, opening("user-1", guestToken), cookies, nil); rec.Code != middleware.StatusCSRFExpired {
		t.Errorf("a guest's token on a session answered %d, want %d", rec.Code, middleware.StatusCSRFExpired)
	}

	rec := app.visit(t, http.MethodPost, wallet.DefaultPrefix, opening("user-1", token), cookies, nil)
	if !succeeded(rec.Code) {
		t.Fatalf("the form the listing drew answered %d, want a success or a redirect: %s", rec.Code, rec.Body)
	}
	if got := app.wallets(t); got != 1 {
		t.Fatalf("the accepted form wrote %d wallet(s), want 1", got)
	}

	// The screen the redirect lands on is the one that moves money, and its
	// forms carry a token of the same binding. The idempotency key goes in the
	// header a client sends it in.
	opened := rec.Header().Get("Location")
	if opened == "" {
		t.Fatalf("opening a wallet answered %d with no Location to the wallet's screen", rec.Code)
	}
	body, token, cookies = app.draw(t, opened, cookies)
	if token == "" {
		t.Fatal("the operations screen drew an empty token for its holder")
	}
	if _, drawn := link(body, "data-login"); drawn {
		t.Error("the operations screen offers its holder the sign-in link")
	}
	rec = app.visit(t, http.MethodPost, opened+"/deposits", url.Values{"_token": {token}, "amount": {"10.00"}}, cookies,
		http.Header{wallet.IdempotencyHeader: {"pipeline-deposit-1"}})
	if !succeeded(rec.Code) {
		t.Fatalf("the deposit form the operations screen drew answered %d, want a success or a redirect: %s", rec.Code, rec.Body)
	}
	record, err := app.module.Service().Find(context.Background(), staff(), strings.TrimPrefix(opened, wallet.DefaultPrefix+"/"))
	if err != nil {
		t.Fatalf("reading the wallet back: %v", err)
	}
	if record.Balance != 1000 {
		t.Fatalf("the accepted deposit left a balance of %d minor units, want 1000", record.Balance)
	}
}

func TestTheLayoutLinksWhereTheApplicationRegisteredItsRoutes(t *testing.T) {
	t.Parallel()

	app := serveWallet(t, true)
	opened := openWallet(t, app.module.Service(), "user-1", "main", 2)
	session := app.signIn(t, person("user-1"))
	for _, target := range []string{
		wallet.DefaultPrefix,
		wallet.DefaultPrefix + "/" + opened.ID,
		wallet.DefaultPrefix + "/" + opened.ID + "/entries",
	} {
		body, _, _ := app.draw(t, target, session)
		if got, _ := link(body, "data-brand"); got != "/" {
			t.Errorf("%s links the brand to %q, want the route named home, /", target, got)
		}
		if got, _ := link(body, "data-logout"); got != "/auth/logout" {
			t.Errorf("%s links Sign out to %q, want the route named auth.logout, /auth/logout", target, got)
		}
	}

	// An application that registered none of the routes gets no address made
	// up for it: the module reads the route table and does not guess at paths.
	bare := serveWallet(t, false)
	theirs := openWallet(t, bare.module.Service(), "user-1", "main", 2)
	signedIn := bare.signIn(t, person("user-1"))
	for _, target := range []string{
		wallet.DefaultPrefix,
		wallet.DefaultPrefix + "/" + theirs.ID,
		wallet.DefaultPrefix + "/" + theirs.ID + "/entries",
	} {
		body, _, _ := bare.draw(t, target, signedIn)
		if got, _ := link(body, "data-brand"); got != "" {
			t.Errorf("with no route named home %s links the brand to %q, want nothing", target, got)
		}
		if got, _ := link(body, "data-logout"); got != "" {
			t.Errorf("with no route named auth.logout %s links Sign out to %q, want nothing", target, got)
		}
	}
}

// chromeModule hands the application the layout above, the way an
// application's view module hands it its own renderer.
type chromeModule struct{}

func (chromeModule) Name() string             { return "chrome" }
func (chromeModule) Routes(*fhttp.Router)     {}
func (chromeModule) Renderer() fhttp.Renderer { return chrome{} }

// linkViews registers the screens under their names once, which is what the
// compiled views do from init() in an application that imported them: the
// module refuses to boot without them, and the registry is the process's.
var linkViews sync.Once

// serveApplication builds the application the way bootstrap/app.go does --
// the framework's Application, the layout module, this module, and the
// middleware that protects forms -- with name as the configured APP_NAME, and
// serves it through the Application's own handler, which is where the
// configured name is put on every request.
func serveApplication(t *testing.T, name string) servedWallet {
	t.Helper()

	sessions := security.NewSessionStore([]byte(appKey), time.Hour, false, security.NewMemoryBackend())
	module, err := wallet.New(wallet.Config{Tenant: tenant}, database(t), sessions)
	if err != nil {
		t.Fatalf("building the module: %v", err)
	}

	linkViews.Do(func() {
		for _, name := range wallet.ViewNames() {
			view.Register(name, func(io.Writer, any) error { return nil })
		}
	})
	app := foundation.New(bootstrap.Configuration{
		App:           config.App{Name: name, Env: config.EnvProd, Key: []byte(appKey)},
		Observability: bootstrap.Observability{LogLevel: slog.LevelError},
	})
	csrf := security.NewCSRF([]byte(appKey), time.Hour).Secure(false)
	app.Register(chromeModule{}, module).Use(middleware.CSRFProtect(csrf, sessions.IDFromRequest))
	if err := app.Boot(context.Background()); err != nil {
		t.Fatalf("booting the application: %v", err)
	}
	return servedWallet{handler: app.Handler(), sessions: sessions, module: module}
}

// brand is the text of the brand link in a drawn screen.
func brand(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`<a data-brand href="[^"]*">([^<]*)</a>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the screen draws no brand link:\n%s", body)
	}
	return html.UnescapeString(m[1])
}

// TestTheScreensDrawTheNameTheApplicationIsConfiguredWith holds that the brand
// on these screens is the application's APP_NAME, read off the request. This
// module never reads the application's configuration, so the name can reach
// its screens only by the request the Application served.
func TestTheScreensDrawTheNameTheApplicationIsConfiguredWith(t *testing.T) {
	t.Parallel()

	const name = "Ledgerly Books"
	app := serveApplication(t, name)
	opened := openWallet(t, app.module.Service(), "user-1", "main", 2)
	session := app.signIn(t, person("user-1"))
	targets := []string{
		wallet.DefaultPrefix,
		wallet.DefaultPrefix + "/" + opened.ID,
		wallet.DefaultPrefix + "/" + opened.ID + "/entries",
	}
	for _, target := range targets {
		body, _, _ := app.draw(t, target, session)
		if got := brand(t, body); got != name {
			t.Errorf("%s draws the brand %q, want the configured APP_NAME %q", target, got, name)
		}
	}

	// An application configured with no name puts none on the request, and
	// the screens make none up.
	bare := serveApplication(t, "")
	theirs := openWallet(t, bare.module.Service(), "user-1", "main", 2)
	signedIn := bare.signIn(t, person("user-1"))
	for _, target := range []string{
		wallet.DefaultPrefix,
		wallet.DefaultPrefix + "/" + theirs.ID,
		wallet.DefaultPrefix + "/" + theirs.ID + "/entries",
	} {
		body, _, _ := bare.draw(t, target, signedIn)
		if got := brand(t, body); got != "" {
			t.Errorf("with no APP_NAME %s draws the brand %q, want none", target, got)
		}
	}
}
