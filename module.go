// Package wallet keeps balances and the ledger that explains them.
//
// A wallet holds an integer of minor units in one currency; a holder may have
// several. Money enters and leaves through operations -- deposit, withdrawal,
// transfer, exchange, reversal, confirmation -- and every operation appends to a
// ledger that is never rewritten. The balance column is the projection of that
// ledger and is only ever moved by a statement carrying its own guard, so a
// withdrawal that would take the balance past what the wallet may hold is
// refused by the write itself rather than by a comparison made before it.
//
// How far past zero a wallet may go is its credit limit, a column the guard
// reads in the same statement that moves the money.
//
// A movement can be recorded without counting. Its entries are in the ledger
// saying what was proposed, the balance has not moved, and a confirmation
// appends the settled entry beside each of them under an operation naming the
// one it settles. Nothing is ever flipped: the sum of the settled entries is the
// balance, and what is still waiting is readable beside it.
//
// A transfer between wallets that are not counted the same way is an exchange,
// which is its own kind of operation and not a transfer with a note on it. The
// rate is quoted once, applied under one rounding rule, and written down beside
// the operation with both currencies, both scales, both amounts, the moment it
// was quoted and the part no minor unit could carry -- so the arithmetic can be
// done again from the row alone.
//
// A payment between two wallets can cost more or less than it moves. What the
// payer is charged less and what the receiver charges to be paid are both the
// application's to answer, through seams this package asks once and records: the
// share, its floor and its ceiling, what it was computed from and where it went
// are all on one row, so a receipt that says a different number from the request
// says why. What leaves is what arrives plus the fee, exactly, and the share
// that no minor unit could carry is written down rather than dropped.
//
// Every operation carries an idempotency key the caller chose. The same key
// twice moves money once: the second call answers with the first one's receipt,
// and for an exchange that means the first one's rate and the first one's
// price.
//
// The files are laid out by role rather than by layer, so the whole package
// reads top to bottom:
//
//	module.go      -> registration, routes, handlers and migrations
//	config.go      -> what the application passes in
//	money.go       -> the amount type, its scale and its arithmetic
//	model.go       -> the entities, and what they may answer with
//	policy.go      -> who may do what
//	rate.go        -> the rate, its arithmetic, and the seam that quotes it
//	fee.go         -> the fee, its arithmetic, and the seams that price a payment
//	service.go     -> the rules and Model access, after authorization
//	views.go       -> the files the application takes ownership of
//
// An application registers it explicitly. There is no service provider, no
// container and no discovery: the wiring is three lines somebody wrote, and
// reading them is how they learn what the application is made of.
package wallet

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strconv"
	"strings"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/foundation"
	fhttp "github.com/arandu-io/framework/http"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
	"github.com/arandu-io/hesape/database/migrations"
	"github.com/arandu-io/hesape/database/schema"
	"github.com/arandu-io/hesape/translation"
	"github.com/arandu-io/hesape/view"
)

// IdempotencyHeader is where a caller writes the name of its request.
//
// A header rather than a body field, because the key belongs to the delivery of
// the request and not to what it asks for: a retry is the same body sent again,
// and the thing that says "this is that same request" has to be readable
// without parsing the body twice.
const IdempotencyHeader = "Idempotency-Key"

// Module is what the application registers.
//
// It implements foundation.Module, which is Name and Routes and nothing else --
// that pair is the whole public contract between a package and the framework.
//
// It also implements foundation.Migratable, because it owns tables, and
// foundation.Publishable, because it hands view sources to the project. The
// other optional interfaces are declared beside Module in the framework and are
// opted into the same way, by implementing them: Bootable to prepare state at
// boot, Background to run a loop of its own, Schedulable to declare work for
// the scheduler, Health to report on the storage it depends on, Closable to
// give resources back at shutdown.
type Module struct {
	cfg      Config
	svc      *WalletService
	sessions *security.SessionStore
}

// Compile-time proof that the module honors the contracts it claims.
var (
	_ foundation.Module      = (*Module)(nil)
	_ foundation.Migratable  = (*Module)(nil)
	_ foundation.Bootable    = (*Module)(nil)
	_ foundation.Publishable = (*Module)(nil)
)

// New returns the module, or the reason it cannot be built.
//
// The collaborators are parameters and not fields somebody fills in afterwards:
// a module that could be registered half-wired is a module whose first request
// is the thing that reports the missing half.
//
// It returns an error rather than panicking or carrying on, because everything
// it refuses is a wiring mistake, and a wiring mistake found at boot costs one
// restart. The same mistake found later is a request that reached a nil handle.
func New(cfg Config, db *data.DB, sessions *security.SessionStore) (*Module, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, errors.New("wallet: New needs a database handle: this package owns its tables, and there is no in-memory mode that would let it start without one")
	}
	if sessions == nil {
		return nil, errors.New("wallet: New needs a session store: it is where the subject comes from, and a request with no subject cannot be authorized")
	}
	cfg = cfg.withDefaults()
	return &Module{
		cfg:      cfg,
		svc:      NewWalletService(db, cfg.Rates, cfg.Fees, cfg.Discounts, cfg.Listeners...),
		sessions: sessions,
	}, nil
}

// Service is the use cases this module holds, for the code an application
// writes beside the routes.
//
// One of it, holding one database handle, so a balance moved from a command, a
// checkout of the application's own and a request are the same rows decided by
// the same policy. It is exported because paying for a basket has no route
// here: a basket names products, a product is the application's type, and the
// handler that owns the catalogue is the one that calls Pay.
//
// It is the service and never the handle. What comes back still authorizes
// before it reaches a table, which is the difference between handing out a use
// case and handing out the database.
func (m *Module) Service() *WalletService { return m.svc }

// Name is the module identifier: a lowercase slug, stable, no spaces.
//
// It is what `aru route:list` groups by and what the route names are prefixed with,
// so changing it changes addresses that other code has already written down.
func (m *Module) Name() string { return "wallet" }

// Routes registers the module's routes under the configured prefix.
//
// They are named, so a URL is built from a name rather than written out a
// second time somewhere else -- two spellings of one address disagree, and the
// failure when they do is a link to a 404.
//
// Reading and moving money are different addresses and different methods. A
// deposit is a POST to a collection of deposits rather than a PATCH of a
// balance, because the thing being created is the movement: it has an
// identifier, it is in the ledger afterwards, and it can be undone by name.
// The method and the address of each one come from routePatterns, which is
// also the list Config.Validate proves the configured prefix can carry. Only
// the handler is written here, so the two cannot describe different sets of
// routes.
func (m *Module) Routes(r *fhttp.Router) {
	m.register(r, "wallet.index", m.index)
	m.register(r, "wallet.store", m.store)
	m.register(r, "wallet.show", m.show)
	m.register(r, "wallet.entries", m.entries)
	m.register(r, "wallet.credit", m.credit)
	m.register(r, "wallet.deposit", m.deposit)
	m.register(r, "wallet.withdraw", m.withdraw)
	m.register(r, "wallet.transfer", m.transfer)
	m.register(r, "wallet.purchases", m.purchases)
	m.register(r, "wallet.reverse", m.reverse)
	m.register(r, "wallet.confirm", m.confirm)
	m.register(r, "wallet.refund", m.refund)
}

// Paying for a basket has no route here, and its absence is a decision.
//
// A basket names products, and a product is the application's type: what is for
// sale, what it costs this customer and how many are left are three questions
// this package has never been able to answer and has no address to ask them at.
// A route that took a product identifier would need a catalogue seam to turn it
// back into a product, which is the application's own handler written twice --
// once here, badly, without the rest of the checkout around it.
//
// So Pay is a Go call the application makes from the handler that owns its
// catalogue, and what this package answers over HTTP is what it does know: what
// a wallet has bought, and giving a line of it back by identifier.

// register mounts the named route, reading its method and its address from
// routePatterns.
//
// A name that is not in that list registers nothing, rather than panicking at
// boot or inventing an address: it is a mistake inside this package and not a
// wiring mistake an installer made, so the place it has to be caught is the
// suite, where TestTheModuleRegistersItsRoutesUnderItsPrefix reads the whole
// table back and counts.
func (m *Module) register(r *fhttp.Router, name string, handler func(*fhttp.Context) error) {
	for _, route := range routePatterns {
		if route.name != name {
			continue
		}
		r.Action(route.method, m.cfg.Prefix+route.suffix, handler).Name(name)
		return
	}
}

// PublishCommand is what an application runs to take ownership of the views
// this package offers.
//
// It is spelled out as a constant so that whatever says it -- the refusal
// below, or a message an application writes for its own operators -- says one
// thing. A person who is told two different commands for one job tries both.
//
// The command reads the modules the application registered and writes what each
// one declares, which is why it is the application's command and not this
// package's: nothing outside the application knows which modules it holds.
const PublishCommand = "aru vendor:publish --apply"

// Boot refuses to serve when a view this package renders is not in the binary.
//
// A compiled view registers itself from init(), so by the time anything boots
// the question has one answer already: either the application published the
// files, compiled them and imported the package they became, or it did not.
// Asking here turns "did anybody run the install command" into one refusal at
// start-up that names the views and the command, instead of a 500 on the first
// request that reached one of them -- which is where it used to be answered,
// once per page, to whoever happened to open it.
//
// It also holds the destination. Every file this package offers has to land
// under the vendor directory named after this module: a publication that
// reached resources/views/home.kyse.go would land on a page the application
// wrote, and what publishes the files writes where the publication says.
func (m *Module) Boot(context.Context) error {
	// The destination and not the archive: the two differ because go mod
	// refuses to publish a path with a vendor segment, and the destination has
	// one.
	prefix := viewPrefix + "/"
	var stray []string
	for _, path := range PublishedPaths() {
		if !strings.HasPrefix(path, prefix) {
			stray = append(stray, path)
		}
	}
	if len(stray) > 0 {
		return fmt.Errorf("wallet: %s would be published outside %s, where it lands on a file the application wrote",
			strings.Join(stray, ", "), prefix)
	}

	registered := make(map[string]bool)
	for _, name := range view.Registered() {
		registered[name] = true
	}
	var missing []string
	for _, name := range ViewNames() {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		// The compiled packages are named because the import is the half of the
		// install nothing else can do: the command writes the sources, the view
		// compiler turns them into Go, and a package the application does not
		// import is not linked at all.
		imports := make([]string, 0, len(ViewPackages()))
		for _, pkg := range ViewPackages() {
			imports = append(imports, "<module path>/"+pkg)
		}
		return fmt.Errorf("wallet: no view is registered as %s. Run `%s`, then `aru view:build`, then import %s in bootstrap/app.go",
			strings.Join(missing, ", "), PublishCommand, strings.Join(imports, ", "))
	}
	return nil
}

// Handlers are thin on purpose: read the input, ask the service, answer. No
// rule, database handle or Model construction lives here. A handler that
// reached data directly would skip the service's policy boundary, and the
// layout makes that visible rather than relying on review.

// index answers a page of wallets.
func (m *Module) index(ctx *fhttp.Context) error {
	in := ListRequest{
		Query: data.Query{
			Sort:   ctx.Query("sort"),
			Cursor: ctx.Query("cursor"),
			Limit:  m.cfg.PageSize,
		},
		HolderID: ctx.Query("holder_id"),
	}

	records, err := m.svc.List(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}

	// A full page is the only one that can have a successor. A short page is
	// the last one, and offering a cursor for it would be offering a next page
	// that comes back empty.
	cursor := ""
	if len(records) == m.cfg.PageSize {
		cursor = records[len(records)-1].ID
	}
	if ctx.WantsJSON() {
		return ctx.JSON(stdhttp.StatusOK, collectionFromPointers(records, cursor))
	}

	labels := m.Labels(m.locale(ctx.Request))
	return ctx.View(ViewIndex, IndexPageData{
		Page:   m.page(ctx, labels.T("screen.index_title")),
		Prefix: m.cfg.Prefix,
		Labels: labels,
		Holder: in.HolderID,
		Rows:   walletRows(records),
		Next:   cursor,
	})
}

// show answers one wallet, as the resource or as the screen that moves its
// money.
func (m *Module) show(ctx *fhttp.Context) error {
	actor := m.subject(ctx.Request)
	record, err := m.svc.Find(ctx.Ctx(), actor, ctx.Param("id"))
	if err != nil {
		return m.answer(ctx, err)
	}
	if ctx.WantsJSON() {
		return ctx.JSON(stdhttp.StatusOK, resourceFromPointer(record))
	}

	// What was bought with this wallet goes on the screen because the
	// identifier a refund names is on it: a control whose argument the reader
	// has to fetch from somewhere else is a control nobody uses.
	labels := m.Labels(m.locale(ctx.Request))
	lines, err := m.svc.PurchasesOf(ctx.Ctx(), actor, record.ID, data.Query{Limit: m.cfg.PageSize})
	if err != nil && !errors.Is(err, security.ErrForbidden) {
		return m.answer(ctx, err)
	}
	return ctx.View(ViewOperations, OperationsPageData{
		Page:      m.page(ctx, labels.T("screen.operations_title")),
		Prefix:    m.cfg.Prefix,
		Labels:    labels,
		Wallet:    walletRow(record),
		Purchases: purchaseRows(labels, lines),
		// Asked before the page is drawn rather than after the button is
		// pressed. It is the same policy the write would consult, so a control
		// that is drawn is a control that works.
		MaySetCredit: m.allowed(ctx, actor, WalletCredit, *record),
	})
}

// allowed reports whether the policy would let this subject do this to this
// wallet.
//
// It is the same call the write makes, and that is the point: a screen that
// decided for itself which controls to draw would be a second copy of the rules,
// and the copy that fell behind would be the one drawing a button that answers
// 403.
func (m *Module) allowed(ctx *fhttp.Context, actor security.Subject, action security.Action, record Wallet) bool {
	_, err := security.Authorize(ctx.Ctx(), WalletPolicy{}, actor, action, record)
	return err == nil
}

// store opens one wallet.
func (m *Module) store(ctx *fhttp.Context) error {
	places, err := decimalPlaces(ctx.Input("decimal_places"))
	if err != nil {
		return m.answer(ctx, err)
	}
	in := OpenRequest{
		HolderID:      ctx.Input("holder_id"),
		Slug:          ctx.Input("slug"),
		Name:          ctx.Input("name"),
		Currency:      Currency(ctx.Input("currency")),
		DecimalPlaces: places,
	}

	record, err := m.svc.Open(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	if !ctx.WantsJSON() {
		return ctx.Redirect(m.cfg.Prefix + "/" + record.ID)
	}
	return ctx.JSON(stdhttp.StatusCreated, resourceFromPointer(record))
}

// entries answers a page of one wallet's ledger.
func (m *Module) entries(ctx *fhttp.Context) error {
	in := HistoryRequest{
		WalletID: ctx.Param("id"),
		Query: data.Query{
			Cursor: ctx.Query("cursor"),
			Limit:  m.cfg.PageSize,
		},
	}

	statement, err := m.svc.History(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}

	cursor := ""
	if len(statement.Entries) == m.cfg.PageSize {
		cursor = statement.Entries[len(statement.Entries)-1].ID
	}
	if ctx.WantsJSON() {
		return ctx.JSON(stdhttp.StatusOK, NewEntryCollection(statement, cursor))
	}

	labels := m.Labels(m.locale(ctx.Request))
	return ctx.View(ViewStatement, StatementPageData{
		Page:        m.page(ctx, labels.T("screen.statement_title")),
		Prefix:      m.cfg.Prefix,
		Labels:      labels,
		Wallet:      walletRow(&statement.Wallet),
		Rows:        statementRows(labels, statement),
		Conversions: conversionRows(statement),
		Charges:     chargeRows(statement),
		Next:        cursor,
	})
}

// credit sets how far below zero one wallet may go.
func (m *Module) credit(ctx *fhttp.Context) error {
	in := CreditRequest{
		WalletID: ctx.Param("id"),
		Limit:    ctx.Input("limit"),
	}

	record, err := m.svc.SetCredit(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	if !ctx.WantsJSON() {
		return ctx.Redirect(m.cfg.Prefix + "/" + record.ID)
	}
	return ctx.JSON(stdhttp.StatusOK, resourceFromPointer(record))
}

// deposit puts money into one wallet.
func (m *Module) deposit(ctx *fhttp.Context) error {
	pending, err := readFlag("pending", ctx.Input("pending"))
	if err != nil {
		return m.answer(ctx, err)
	}
	in := DepositRequest{
		IdempotencyKey: ctx.Header(IdempotencyHeader),
		WalletID:       ctx.Param("id"),
		Amount:         ctx.Input("amount"),
		Pending:        pending,
		Meta:           m.meta(ctx, "meta"),
	}

	receipt, err := m.svc.Deposit(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	return m.receipt(ctx, receipt)
}

// withdraw takes money out of one wallet.
func (m *Module) withdraw(ctx *fhttp.Context) error {
	pending, err := readFlag("pending", ctx.Input("pending"))
	if err != nil {
		return m.answer(ctx, err)
	}
	force, err := readFlag("force", ctx.Input("force"))
	if err != nil {
		return m.answer(ctx, err)
	}
	in := WithdrawRequest{
		IdempotencyKey: ctx.Header(IdempotencyHeader),
		WalletID:       ctx.Param("id"),
		Amount:         ctx.Input("amount"),
		Pending:        pending,
		Force:          force,
		Meta:           m.meta(ctx, "meta"),
	}

	receipt, err := m.svc.Withdraw(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	return m.receipt(ctx, receipt)
}

// transfer moves money from the wallet in the path into the one in the body.
//
// The two sides are asked about separately, and there is no field that answers
// for both: a payment where what leaves counts now and what arrives waits is an
// arrangement a single flag cannot spell, and a shorthand beside the two would
// be a second way to say what one of them already says.
func (m *Module) transfer(ctx *fhttp.Context) error {
	withdrawal, err := readFlag("withdrawal_pending", ctx.Input("withdrawal_pending"))
	if err != nil {
		return m.answer(ctx, err)
	}
	deposit, err := readFlag("deposit_pending", ctx.Input("deposit_pending"))
	if err != nil {
		return m.answer(ctx, err)
	}
	force, err := readFlag("force", ctx.Input("force"))
	if err != nil {
		return m.answer(ctx, err)
	}
	in := TransferRequest{
		IdempotencyKey: ctx.Header(IdempotencyHeader),
		FromWalletID:   ctx.Param("id"),
		ToWalletID:     ctx.Input("to_wallet_id"),
		Amount:         ctx.Input("amount"),
		Withdrawal:     Leg{Pending: withdrawal, Meta: m.meta(ctx, "withdrawal_meta")},
		Deposit:        Leg{Pending: deposit, Meta: m.meta(ctx, "deposit_meta")},
		Force:          force,
		Meta:           m.meta(ctx, "meta"),
	}

	receipt, err := m.svc.Transfer(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	return m.receipt(ctx, receipt)
}

// purchases answers a page of what one wallet bought.
func (m *Module) purchases(ctx *fhttp.Context) error {
	records, err := m.svc.PurchasesOf(ctx.Ctx(), m.subject(ctx.Request), ctx.Param("id"), data.Query{
		Cursor: ctx.Query("cursor"),
		Limit:  m.cfg.PageSize,
	})
	if err != nil {
		return m.answer(ctx, err)
	}

	// A full page is the only one that can have a successor. A short page is
	// the last one, and offering a cursor for it would be offering a next page
	// that comes back empty.
	cursor := ""
	if len(records) == m.cfg.PageSize {
		cursor = records[len(records)-1].ID
	}
	return ctx.JSON(stdhttp.StatusOK, NewPurchaseCollection(records, cursor))
}

// refund gives back the lines the request names.
func (m *Module) refund(ctx *fhttp.Context) error {
	force, err := readFlag("force", ctx.Input("force"))
	if err != nil {
		return m.answer(ctx, err)
	}
	in := RefundRequest{
		IdempotencyKey: ctx.Header(IdempotencyHeader),
		PurchaseIDs:    m.list(ctx, "purchase_ids"),
		Reason:         ctx.Input("reason"),
		Force:          force,
		Meta:           m.meta(ctx, "meta"),
	}

	receipt, err := m.svc.Refund(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	return m.receipt(ctx, receipt)
}

// reverse undoes one operation.
func (m *Module) reverse(ctx *fhttp.Context) error {
	in := ReverseRequest{
		IdempotencyKey: ctx.Header(IdempotencyHeader),
		OperationID:    ctx.Param("operation"),
		Reason:         ctx.Input("reason"),
		Meta:           m.meta(ctx, "meta"),
	}

	receipt, err := m.svc.Reverse(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	return m.receipt(ctx, receipt)
}

// confirm makes one operation count.
func (m *Module) confirm(ctx *fhttp.Context) error {
	force, err := readFlag("force", ctx.Input("force"))
	if err != nil {
		return m.answer(ctx, err)
	}
	in := ConfirmRequest{
		IdempotencyKey: ctx.Header(IdempotencyHeader),
		OperationID:    ctx.Param("operation"),
		Force:          force,
		Meta:           m.meta(ctx, "meta"),
	}

	receipt, err := m.svc.Confirm(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	return m.receipt(ctx, receipt)
}

// receipt answers a movement of money.
//
// A replay answers 200 and a first run answers 201, which is the difference the
// caller asked about by sending the key: one says the money moved just now, the
// other says it moved earlier and this request changed nothing.
//
// Each amount is rendered at the scale of the wallet it moved, looked up once
// per wallet: an exchange writes two entries counted differently, and one scale
// for both would print one of them with the point in the wrong place. A wallet
// that cannot be read falls back to the integer, because a scale guessed at is
// worse than an integer nobody can misread.
func (m *Module) receipt(ctx *fhttp.Context, receipt Receipt) error {
	// A form has nowhere to put a receipt, so it is answered with the page the
	// numbers are on. It is a redirect and not a rendered page, because a form
	// answered with a body is a form the browser offers to send again.
	if !ctx.WantsJSON() {
		return ctx.Redirect(m.pageOf(ctx, receipt))
	}

	actor := m.subject(ctx.Request)
	places := make(map[string]int, len(receipt.Entries))
	for _, entry := range receipt.Entries {
		if _, known := places[entry.WalletID]; known {
			continue
		}
		if record, err := m.svc.Find(ctx.Ctx(), actor, entry.WalletID); err == nil && record != nil {
			places[entry.WalletID] = record.DecimalPlaces
		}
	}
	status := stdhttp.StatusCreated
	if receipt.Replayed {
		status = stdhttp.StatusOK
	}
	return ctx.JSON(status, NewReceiptResource(receipt, places))
}

// locale is what language the request asked for, as the negotiation middleware
// left it on the context.
//
// A request that went through no such middleware carries none, and the screens
// are drawn in the shipped locale. That is a screen somebody can read, where a
// refusal would not be.
func (m *Module) locale(r *stdhttp.Request) string { return translation.Locale(r.Context()) }

// page is the chrome the application's layout draws around a screen.
func (m *Module) page(ctx *fhttp.Context, title string) view.Page {
	actor := m.subject(ctx.Request)
	token, err := m.cfg.CSRF.Issue(m.sessions.IDFromRequest(ctx.Request))
	if err != nil {
		// An unissued token is left empty rather than reported. The page still
		// renders and every form on it is refused, which is what a missing
		// session means -- and the alternative, failing the read because the
		// write would fail, is a blank screen where a sign-in prompt belongs.
		token = ""
	}
	return view.Page{
		Title:         title,
		Token:         token,
		Authenticated: actor.ID != "",
		Path:          ctx.Request.URL.Path,
	}
}

// pageOf is where a form is sent back to once its movement is done.
//
// The wallet the request named, where there is one, because that is the screen
// the person was looking at and the one the new balance is on. A movement that
// named no wallet in its path -- a reversal, a confirmation, a refund -- goes to
// the listing, which is the only page that is right for all of them.
func (m *Module) pageOf(ctx *fhttp.Context, receipt Receipt) string {
	if id := ctx.Param("id"); id != "" {
		return m.cfg.Prefix + "/" + id
	}
	for _, entry := range receipt.Entries {
		if entry.WalletID != "" {
			return m.cfg.Prefix + "/" + entry.WalletID
		}
	}
	return m.cfg.Prefix
}

// subject reads who is acting from the session, and from nowhere else.
//
// A request with no readable session is a declared guest and not an empty
// subject. The difference matters: an empty subject is refused before the
// policy is consulted, because it is almost always a session that failed to
// load, and a policy asked about nobody answers about nobody. A guest reaches
// the policy and is refused there, by a rule somebody wrote -- and here that
// rule refuses, because there is no money a visitor with no session owns.
//
// The tenant of that guest is the application's, from configuration. It is the
// one place a tenant does not come from a Grant, and it is because there is no
// Grant yet: everywhere downstream, data.Tenant is what the statements take.
func (m *Module) subject(r *stdhttp.Request) security.Subject {
	sub, err := m.sessions.Load(r.Context(), r)
	if err != nil || sub.ID == "" {
		return security.Guest(m.cfg.Tenant)
	}
	return sub
}

// meta reads what the application attached under one field.
//
// The form is already parsed by the time this runs, because every handler that
// calls it has read a field of its own first. It is read out of the parsed form
// rather than asked for by name, because the names belong to the application
// and this package has never heard of them.
func (m *Module) meta(ctx *fhttp.Context, field string) Meta {
	if ctx.Request.Form == nil {
		if err := ctx.Request.ParseForm(); err != nil {
			// A body that does not parse is a body no field of it was read
			// from either, and the handler is already answering about that.
			return nil
		}
	}
	return metaFrom(ctx.Request.Form, field)
}

// list reads the repeated field a request wrote.
//
// A form spells several of one thing by sending the field several times, which
// is what a set of checkboxes produces and what a client library writes for a
// list. It is read out of the parsed form because Input answers with the first
// value only, and a refund of six lines that gave back one is worse than one
// that gave back none.
func (m *Module) list(ctx *fhttp.Context, field string) []string {
	if ctx.Request.Form == nil {
		if err := ctx.Request.ParseForm(); err != nil {
			return nil
		}
	}
	return ctx.Request.Form[field]
}

// readFlag reads a boolean a request wrote.
//
// An empty field is false, which is what a client that never heard of the field
// sends. Anything that is neither a yes nor a no is refused rather than read as
// false: a request that asked for something in a spelling this package does not
// know is a request that would otherwise be answered by quietly doing something
// else.
func readFlag(field, text string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "":
		return false, nil
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	errs := validation.Errors{}
	errs.Add(field, "has to be true or false")
	return false, errs
}

// decimalPlaces reads the scale a wallet is being opened at.
//
// An empty field is two places, which is what most currencies are counted in.
// Anything that is not a number is refused rather than read as zero: a wallet
// opened at the wrong scale reinterprets every amount ever written to it, and
// the scale cannot be changed afterwards.
func decimalPlaces(text string) (int, error) {
	if strings.TrimSpace(text) == "" {
		return 2, nil
	}
	places, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		errs := validation.Errors{}
		errs.Add("decimal_places", "has to be a whole number")
		return 0, errs
	}
	return places, nil
}

// answer turns what the service refused into something the client can act on.
//
// A refusal about who is asking is answered with a status and no detail:
// telling the client why a policy said no is telling them what exists and what
// does not, one request at a time.
//
// A refusal about the money is answered by name, and that is deliberate. The
// caller has already been authorized on the wallet in question, so "the balance
// is not enough" tells them nothing they could not read from it -- and a
// payment that fails without saying why is a payment somebody retries until it
// works or until the support queue explains it.
//
// An error this package did not expect is returned rather than swallowed: the
// framework turns it into the error page in development and a 500 in
// production, which is the honest outcome. Answering 200 with an empty body is
// the failure nobody debugs.
func (m *Module) answer(ctx *fhttp.Context, err error) error {
	switch {
	case errors.Is(err, security.ErrForbidden):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusForbidden, "forbidden")
		return nil
	case errors.Is(err, ErrNotFound):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusNotFound, "not found")
		return nil

	// The state of a record that already exists is a conflict: the request was
	// well formed and the answer is that it has already been settled one way.
	case errors.Is(err, ErrWalletExists):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusConflict, "that holder already has a wallet with this slug")
		return nil
	case errors.Is(err, ErrAlreadyReversed):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusConflict, "that operation has already been reversed")
		return nil
	case errors.Is(err, ErrAlreadyConfirmed):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusConflict, "that operation has already been confirmed")
		return nil
	case errors.Is(err, ErrAlreadyRefunded):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusConflict, "that line has already been refunded")
		return nil
	case errors.Is(err, ErrOperationConflict):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusConflict, "that idempotency key belongs to a different request")
		return nil
	case errors.Is(err, ErrCreditBelowBalance):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusConflict, "that wallet is already further below zero than the new credit limit allows")
		return nil

	// The request cannot be carried out against the money as it stands.
	case errors.Is(err, ErrInsufficientFunds):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "the balance and the credit limit are not enough")
		return nil
	case errors.Is(err, ErrCreditNegative):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "a credit limit is how far below zero a wallet may go, and cannot be negative")
		return nil
	case errors.Is(err, ErrCurrencyMismatch):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "those wallets are not counted the same way, and no rate provider is configured")
		return nil
	case errors.Is(err, ErrFeeExceedsAmount):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "the fee is not smaller than the payment it is charged on")
		return nil

	// An amount worth less than one minor unit of the target is the caller's
	// amount and not a fault of the configuration, so it is answered by name.
	// A rate that is malformed, undated or quoted for another pair is not here
	// on purpose: those are the configured provider misbehaving, the caller
	// cannot fix any of them, and answering 422 would tell them their own
	// request was wrong. They fall through, and the framework reports them as
	// what they are.
	case errors.Is(err, ErrConversionUnderflow):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "that amount is worth less than one minor unit of the receiving wallet")
		return nil
	case errors.Is(err, ErrSameWallet):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "a transfer needs two different wallets")
		return nil
	case errors.Is(err, ErrNotReversible):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "a reversal cannot itself be reversed")
		return nil
	case errors.Is(err, ErrNotRefundable):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "a refund cannot itself be refunded")
		return nil
	case errors.Is(err, ErrPurchaseOperation):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "a purchase is undone line by line, with a refund")
		return nil
	case errors.Is(err, ErrProductStock):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, err.Error())
		return nil
	case errors.Is(err, ErrCartEmpty), errors.Is(err, ErrCartTooLarge),
		errors.Is(err, ErrItemQuantity), errors.Is(err, ErrProductWallet),
		errors.Is(err, ErrPaysItself):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, err.Error())
		return nil
	case errors.Is(err, ErrNotSettled):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "that operation has not moved any money, so there is nothing to undo")
		return nil
	case errors.Is(err, ErrNotPending):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "that operation has nothing waiting to be confirmed")
		return nil
	case errors.Is(err, ErrAmountNotPositive), errors.Is(err, ErrAmountScale),
		errors.Is(err, ErrAmountSyntax), errors.Is(err, ErrAmountOverflow),
		errors.Is(err, ErrDecimalPlaces):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, err.Error())
		return nil
	}

	// A rejected input is the answer rather than a failure, and the fields that
	// were rejected are the client's own, so naming them gives nothing away.
	var rejected validation.Errors
	if errors.As(err, &rejected) {
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, rejected.Error())
		return nil
	}
	return err
}

// Migrations declares the schema this module owns.
//
// They are returned in the order their names sort in, which is the order they
// apply in: the name carries the order, and nothing else decides it.
func (m *Module) Migrations() []foundation.Migration {
	return []foundation.Migration{
		createWallets{},
		createWalletOperations{},
		createWalletEntries{},
		createWalletConversions{},
		addWalletCreditLimit{},
		addWalletEntrySettlement{},
		createWalletCharges{},
		addWalletMetadata{},
		createWalletPurchases{},
	}
}

// The migrations are reversible, and the assertions are here rather than
// discovered at rollback: the migrator tests for Down with a type assertion, so
// a Down with the wrong signature would leave a rollback that silently does
// nothing.
var (
	_ migrations.ReversibleMigration = createWallets{}
	_ migrations.ReversibleMigration = createWalletOperations{}
	_ migrations.ReversibleMigration = createWalletEntries{}
	_ migrations.ReversibleMigration = createWalletConversions{}
	_ migrations.ReversibleMigration = addWalletCreditLimit{}
	_ migrations.ReversibleMigration = addWalletEntrySettlement{}
	_ migrations.ReversibleMigration = createWalletCharges{}
	_ migrations.ReversibleMigration = addWalletMetadata{}
	_ migrations.ReversibleMigration = createWalletPurchases{}
)

// createWallets is the balances table.
type createWallets struct{ migrations.BaseMigration }

// GetName is the migration's identity, and it carries the order. It is fixed
// once the package is published: changing what an applied name means leaves the
// change missing everywhere it already ran, and nothing says so.
func (createWallets) GetName() string { return "20260905_0001_create_wallets" }

// Up creates the wallets table.
//
// The balance is a big integer and not a decimal, and that is the decision this
// whole package rests on: minor units counted in an int64, so that arithmetic
// is exact, comparison is exact, and the guard on a withdrawal is a comparison
// the database can make without a numeric library. A decimal column would be
// exact too and would arrive in Go as text or as a float, which is where the
// exactness is lost.
//
// The Blueprint spells each column for the engine the migration is running on,
// which is what lets one application develop on a file and deploy on Postgres
// without a second schema.
//
// The timestamps have no database default: the values come from Go.
func (createWallets) Up(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Create(ctx, walletsTable, func(table *schema.Blueprint) {
		table.String("id").Primary()
		table.String("tenant_id")
		table.String("holder_id")
		table.String("slug")
		table.String("name")
		table.String("currency", 12)
		table.UnsignedSmallInteger("decimal_places").Default(2)
		table.BigInteger("balance").Default(0)
		table.BigInteger("last_sequence").Default(0)
		table.Timestamp("created_at")
		table.Timestamp("updated_at")

		// One wallet per holder per slug, per tenant. It is a unique index and
		// not a check in Go, because a check in Go is a check two concurrent
		// requests both pass.
		table.Unique([]string{"tenant_id", "holder_id", "slug"}, "wallets_tenant_holder_slug_uq")

		// The index matches the ORDER BY of the listing, tenant first. Without
		// it every page is a scan of every customer's rows.
		table.Index([]string{"tenant_id", "created_at", "id"}, "wallets_tenant_created_idx")
	})
}

// Down drops the table, which takes its indexes with it.
func (createWallets) Down(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().DropIfExists(ctx, walletsTable)
}

// createWalletOperations is the table of requests that moved money.
type createWalletOperations struct{ migrations.BaseMigration }

// GetName is the migration's identity, and it carries the order.
func (createWalletOperations) GetName() string { return "20260905_0002_create_wallet_operations" }

// Up creates the operations table and the two unique indexes that are the whole
// of this package's protection against a request being carried out twice.
//
// The first is the idempotency key: one key, one operation, per tenant.
//
// The second is the operation being settled, which every row carries -- its own
// identifier where it settles nothing, and the identifier of the operation it
// undoes where it is a reversal. The kind is in the index with it, and it has
// to be: without it a reversal naming its original would collide with the
// original's own row, which holds the same identifier for settling itself. With
// it, the index says that one operation is reversed at most once, and says it
// in the database rather than in a check that two concurrent reversals would
// both walk past.
func (createWalletOperations) Up(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Create(ctx, operationsTable, func(table *schema.Blueprint) {
		table.String("id").Primary()
		table.String("tenant_id")
		table.String("idempotency_key", 128)
		table.String("kind", 16)
		table.String("reverses_id")
		table.String("reason", 255).Default("")
		table.Timestamp("created_at")

		table.Unique([]string{"tenant_id", "idempotency_key"}, "wallet_operations_key_uq")
		table.Unique([]string{"tenant_id", "kind", "reverses_id"}, "wallet_operations_reverses_uq")
	})
}

// Down drops the table, which takes its indexes with it.
func (createWalletOperations) Down(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().DropIfExists(ctx, operationsTable)
}

// createWalletEntries is the ledger.
type createWalletEntries struct{ migrations.BaseMigration }

// GetName is the migration's identity, and it carries the order.
func (createWalletEntries) GetName() string { return "20260905_0003_create_wallet_entries" }

// Up creates the entries table.
//
// There is no updated_at, and its absence is the append-only rule written into
// the schema: an update through the Model would try to stamp a column that does
// not exist and fail, so a change to a ledger row is not something review has
// to catch.
//
// The order of a statement is the sequence and not the timestamp. Two entries
// written inside one tick of the clock are two rows a timestamp cannot order,
// and the engines do not even agree on how much of a tick they keep -- so the
// column a statement is read by is one this package writes and the database
// holds unique.
func (createWalletEntries) Up(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Create(ctx, entriesTable, func(table *schema.Blueprint) {
		table.String("id").Primary()
		table.String("tenant_id")
		table.String("operation_id")
		table.String("wallet_id")
		table.String("kind", 16)
		table.BigInteger("sequence")
		table.UnsignedSmallInteger("position").Default(0)
		table.BigInteger("amount")
		table.BigInteger("balance_after")
		table.Timestamp("created_at")

		// The index matches the ORDER BY of a statement -- one wallet's rows in
		// the order they were written -- and it is unique, which is the second
		// half of the ledger's own check on itself: the balance column has to
		// equal the last entry, and no two entries may claim one position.
		table.Unique([]string{"tenant_id", "wallet_id", "sequence"}, "wallet_entries_wallet_sequence_uq")
		// And the one a receipt reads: every entry of one operation.
		table.Index([]string{"tenant_id", "operation_id"}, "wallet_entries_operation_idx")
	})
}

// Down drops the table, which takes its indexes with it.
func (createWalletEntries) Down(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().DropIfExists(ctx, entriesTable)
}

// createWalletConversions is the table of rates that were applied.
type createWalletConversions struct{ migrations.BaseMigration }

// GetName is the migration's identity, and it carries the order.
func (createWalletConversions) GetName() string { return "20260905_0004_create_wallet_conversions" }

// Up creates the conversions table.
//
// Every column is a number or a code, and there is not a floating point one
// among them. The rate is two big integers because a fraction is what a rate
// is, the amounts are big integers of minor units like every other amount in
// this package, and the leftover is two more integers so that what rounding
// dropped is a value rather than a discrepancy. A decimal column for the rate
// would arrive in Go as text or as a float, and the float is where an audit
// stops being able to reproduce the row it is auditing.
//
// One conversion per operation, held by a unique index: an operation applies
// one rate, and a second row against it would be a second answer to what an
// exchange was worth. The index also carries the read a receipt makes, which is
// the rate of one operation.
//
// There is no updated_at, and its absence is the append-only rule written into
// the schema, exactly as it is on the ledger: a rate that could be corrected in
// place is a rate that says what somebody later wished it had been.
func (createWalletConversions) Up(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Create(ctx, conversionsTable, func(table *schema.Blueprint) {
		table.String("id").Primary()
		table.String("tenant_id")
		table.String("operation_id")

		table.String("from_currency", 12)
		table.UnsignedSmallInteger("from_decimal_places").Default(2)
		table.BigInteger("from_amount")

		table.String("to_currency", 12)
		table.UnsignedSmallInteger("to_decimal_places").Default(2)
		table.BigInteger("to_amount")

		table.BigInteger("rate_numerator")
		table.BigInteger("rate_denominator")
		table.Timestamp("quoted_at")

		table.String("rounding", 16)
		table.BigInteger("remainder_numerator").Default(0)
		table.BigInteger("remainder_denominator").Default(1)

		table.Timestamp("created_at")

		table.Unique([]string{"tenant_id", "operation_id"}, "wallet_conversions_operation_uq")
	})
}

// Down drops the table, which takes its index with it.
func (createWalletConversions) Down(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().DropIfExists(ctx, conversionsTable)
}

// addWalletCreditLimit is how far below zero a wallet may go.
type addWalletCreditLimit struct{ migrations.BaseMigration }

// GetName is the migration's identity, and it carries the order.
func (addWalletCreditLimit) GetName() string { return "20260905_0005_add_wallet_credit_limit" }

// Up adds the credit limit to the balances table.
//
// A big integer of minor units like the balance it is compared against, and
// with a default of zero, which is the wallet that may not go below zero at all
// -- so every row that existed before this ran keeps exactly the rule it had.
//
// It is a column rather than a value an application answers for on each call
// because the guard on a withdrawal reads it: the statement that moves the
// money compares the balance against the amount less this column, in one
// statement, so the limit that decides is the one the row holds at that
// instant.
func (addWalletCreditLimit) Up(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Table(ctx, walletsTable, func(table *schema.Blueprint) {
		table.BigInteger("credit_limit").Default(0)
	})
}

// Down drops the column, which puts every wallet back at a floor of zero.
func (addWalletCreditLimit) Down(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Table(ctx, walletsTable, func(table *schema.Blueprint) {
		table.DropColumn("credit_limit")
	})
}

// addWalletEntrySettlement is whether a movement counted.
type addWalletEntrySettlement struct{ migrations.BaseMigration }

// GetName is the migration's identity, and it carries the order.
func (addWalletEntrySettlement) GetName() string { return "20260905_0006_add_wallet_entry_settlement" }

// Up adds to the ledger the column that says whether a row moved the balance.
//
// A small integer and not a boolean column, which is the decision Flag carries:
// a yes-or-no is written as 0 or 1, every engine holds that, and the Go value
// that spells it cannot be spelled differently by a driver.
//
// It defaults to one, which is what every row written before this ran is: the
// ledger had no other kind. So the sum of the settled entries of any wallet is
// what the sum of all of them was, and the balance column still equals it.
//
// The column is written once, with the row, and never updated -- the ledger is
// appended to, and a confirmation appends the settled entry beside the pending
// one rather than rewriting it. The table still has no updated_at, which is how
// the schema says so.
func (addWalletEntrySettlement) Up(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Table(ctx, entriesTable, func(table *schema.Blueprint) {
		table.UnsignedSmallInteger("settled").Default(1)
	})
}

// Down drops the column, which leaves every movement counting.
func (addWalletEntrySettlement) Down(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Table(ctx, entriesTable, func(table *schema.Blueprint) {
		table.DropColumn("settled")
	})
}

// createWalletCharges is the table of what payments cost.
type createWalletCharges struct{ migrations.BaseMigration }

// GetName is the migration's identity, and it carries the order.
func (createWalletCharges) GetName() string { return "20260905_0007_create_wallet_charges" }

// Up creates the charges table.
//
// Every column is a number, a code or a yes-or-no integer, and there is not a
// floating point one among them. The share is two big integers because a
// fraction is what a share is, the amounts are big integers of minor units like
// every other amount in this package, and the leftover is two more integers so
// that what truncation dropped is a value rather than a discrepancy. A decimal
// column for the share would arrive in Go as text or as a float, and the float
// is where an audit stops being able to reproduce the row it is auditing.
//
// One charge per operation, held by a unique index: an operation charges once,
// and a second row against it would be a second answer to what a payment cost.
// The index also carries the read a receipt makes, which is the charge of one
// operation.
//
// There is no updated_at, and its absence is the append-only rule written into
// the schema, exactly as it is on the ledger and on the recorded rates.
func (createWalletCharges) Up(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Create(ctx, chargesTable, func(table *schema.Blueprint) {
		table.String("id").Primary()
		table.String("tenant_id")
		table.String("operation_id")

		table.String("currency", 12)
		table.UnsignedSmallInteger("decimal_places").Default(2)

		table.BigInteger("requested_amount")
		table.BigInteger("discount").Default(0)
		table.BigInteger("base_amount")

		table.BigInteger("fee_numerator").Default(0)
		table.BigInteger("fee_denominator").Default(0)
		table.BigInteger("fee_minimum").Default(0)
		table.BigInteger("fee_maximum").Default(0)
		table.UnsignedSmallInteger("fee_deductible").Default(0)
		table.BigInteger("fee_amount").Default(0)
		table.String("fee_wallet_id").Default("")

		table.String("rounding", 16)
		table.BigInteger("remainder_numerator").Default(0)
		table.BigInteger("remainder_denominator").Default(1)

		table.Timestamp("created_at")

		table.Unique([]string{"tenant_id", "operation_id"}, "wallet_charges_operation_uq")
	})
}

// Down drops the table, which takes its index with it.
func (createWalletCharges) Down(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().DropIfExists(ctx, chargesTable)
}

// addWalletMetadata is what the application attaches to a movement.
type addWalletMetadata struct{ migrations.BaseMigration }

// GetName is the migration's identity, and it carries the order.
func (addWalletMetadata) GetName() string { return "20260905_0008_add_wallet_metadata" }

// Up adds the metadata column to the operations table and to the ledger.
//
// Text and not a document type. The engines spell one differently, only some of
// them have it, and nothing in this package reads what is inside: the column is
// carried, shown and exported, and every decision about the money is made from
// the columns beside it. An application that wants to query its own facts keeps
// them in a table of its own, against rows it owns.
//
// It defaults to the empty string, which is what every row written before this
// ran holds and what a movement nobody attached anything to holds afterwards --
// so no row is left saying that somebody attached nothing, which is a different
// statement from saying nothing.
//
// The ledger still has no updated_at, and the column is written once with the
// row like every other one there.
func (addWalletMetadata) Up(ctx context.Context, conn migrations.Connection) error {
	if err := conn.Schema().Table(ctx, operationsTable, func(table *schema.Blueprint) {
		table.Text("meta").Default("")
	}); err != nil {
		return err
	}
	return conn.Schema().Table(ctx, entriesTable, func(table *schema.Blueprint) {
		table.Text("meta").Default("")
	})
}

// Down drops both columns, which leaves every movement carrying nothing.
func (addWalletMetadata) Down(ctx context.Context, conn migrations.Connection) error {
	if err := conn.Schema().Table(ctx, operationsTable, func(table *schema.Blueprint) {
		table.DropColumn("meta")
	}); err != nil {
		return err
	}
	return conn.Schema().Table(ctx, entriesTable, func(table *schema.Blueprint) {
		table.DropColumn("meta")
	})
}

// createWalletPurchases is what was bought from whom.
type createWalletPurchases struct{ migrations.BaseMigration }

// GetName is the migration's identity, and it carries the order.
func (createWalletPurchases) GetName() string { return "20260905_0009_create_wallet_purchases" }

// Up creates the purchases table.
//
// It is a projection of the ledger and the receipt of one line at once. The
// ledger already holds every movement a basket made; what it cannot say is what
// any of them was for, and assembling that from a catalogue on every read is
// the query every application would write and none of them should have to.
//
// Every number the arithmetic used is a column, and there is not a floating
// point one among them: the price and the amounts are big integers of minor
// units like every other amount here, the share is two big integers because a
// fraction is what a share is, and the leftover is two more so that what
// truncation dropped is a value rather than a discrepancy. That is what lets a
// refund move back exactly what moved by reading one row.
//
// The two indexes are the two questions. The first answers "has this wallet
// already got this from that seller", newest first, which is the read a shop
// makes before it sells the same licence twice; the second answers "what has
// this seller sold", which is the read a statement makes. Both start with the
// tenant, because without it every page is a scan of every customer's rows.
//
// There is no updated_at, and its absence is the append-only rule written into
// the schema, exactly as it is on the ledger, the recorded rates and the
// charges: a line that was given back is a second row naming the first, so what
// was bought stays readable after it is undone.
func (createWalletPurchases) Up(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().Create(ctx, purchasesTable, func(table *schema.Blueprint) {
		table.String("id").Primary()
		table.String("tenant_id")
		table.String("operation_id")
		table.UnsignedSmallInteger("position").Default(0)

		table.String("payer_wallet_id")
		table.String("owner_wallet_id")
		table.String("receiver_wallet_id")

		table.String("product_key")
		table.UnsignedInteger("quantity").Default(1)

		table.String("currency", 12)
		table.UnsignedSmallInteger("decimal_places").Default(2)

		table.BigInteger("price_per_item")
		table.BigInteger("requested_amount")
		table.BigInteger("discount").Default(0)
		table.BigInteger("base_amount")

		table.BigInteger("fee_numerator").Default(0)
		table.BigInteger("fee_denominator").Default(0)
		table.BigInteger("fee_minimum").Default(0)
		table.BigInteger("fee_maximum").Default(0)
		table.UnsignedSmallInteger("fee_deductible").Default(0)
		table.BigInteger("fee_amount").Default(0)
		table.String("fee_wallet_id").Default("")

		table.String("rounding", 16)
		table.BigInteger("remainder_numerator").Default(0)
		table.BigInteger("remainder_denominator").Default(1)

		table.BigInteger("paid_amount")
		table.BigInteger("credited_amount")

		table.String("kind", 16)
		table.String("settles_id")
		table.BigInteger("sequence").Default(0)

		table.Timestamp("created_at")

		// One line is given back at most once, and the database says so rather
		// than a check two concurrent refunds would both walk past. The kind is
		// in the index because every row carries the line it settles -- its own
		// identifier where it settles nothing -- so without it a refund naming
		// its purchase would collide with the purchase's own row.
		table.Unique([]string{"tenant_id", "kind", "settles_id"}, "wallet_purchases_settles_uq")

		// A line is written once per operation and position, which is what makes
		// a basket paid twice under one key impossible to record twice.
		table.Unique([]string{"tenant_id", "operation_id", "position"}, "wallet_purchases_operation_uq")

		table.Index([]string{"tenant_id", "owner_wallet_id", "receiver_wallet_id", "product_key", "sequence"},
			"wallet_purchases_owner_product_idx")
		table.Index([]string{"tenant_id", "receiver_wallet_id", "sequence"},
			"wallet_purchases_receiver_idx")
	})
}

// Down drops the table, which takes its indexes with it.
func (createWalletPurchases) Down(ctx context.Context, conn migrations.Connection) error {
	return conn.Schema().DropIfExists(ctx, purchasesTable)
}
