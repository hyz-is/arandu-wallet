// Package wallet keeps balances and the ledger that explains them.
//
// A wallet holds an integer of minor units in one currency; a holder may have
// several. Money enters and leaves through operations -- deposit, withdrawal,
// transfer, reversal -- and every operation appends to a ledger that is never
// rewritten. The balance column is the projection of that ledger and is only
// ever moved by a statement carrying its own guard, so a withdrawal that would
// overdraw is refused by the write itself rather than by a comparison made
// before it.
//
// Every operation carries an idempotency key the caller chose. The same key
// twice moves money once: the second call answers with the first one's receipt.
//
// The files are laid out by role rather than by layer, so the whole package
// reads top to bottom:
//
//	module.go      -> registration, routes, handlers and migrations
//	config.go      -> what the application passes in
//	money.go       -> the amount type, its scale and its arithmetic
//	model.go       -> the entities, and what they may answer with
//	policy.go      -> who may do what
//	rate.go        -> the seam for converting between currencies
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
		svc:      NewWalletService(db, cfg.Rates),
		sessions: sessions,
	}, nil
}

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
	m.register(r, "wallet.deposit", m.deposit)
	m.register(r, "wallet.withdraw", m.withdraw)
	m.register(r, "wallet.transfer", m.transfer)
	m.register(r, "wallet.reverse", m.reverse)
}

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
// It also holds the destination. Every file the archive offers has to land
// under the vendor directory named after this module: an archive that reached
// resources/views/home.kyse.go would land on a page the application wrote, and
// what publishes the files writes what the archive says.
func (m *Module) Boot(context.Context) error {
	prefix := viewRoot + "/" + vendorDir + "/" + m.Name() + "/"
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
	return ctx.JSON(stdhttp.StatusOK, collectionFromPointers(records, cursor))
}

// show answers one wallet.
func (m *Module) show(ctx *fhttp.Context) error {
	record, err := m.svc.Find(ctx.Ctx(), m.subject(ctx.Request), ctx.Param("id"))
	if err != nil {
		return m.answer(ctx, err)
	}
	return ctx.JSON(stdhttp.StatusOK, resourceFromPointer(record))
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
	return ctx.JSON(stdhttp.StatusOK, NewEntryCollection(statement.Entries, statement.Wallet.DecimalPlaces, cursor))
}

// deposit puts money into one wallet.
func (m *Module) deposit(ctx *fhttp.Context) error {
	in := DepositRequest{
		IdempotencyKey: ctx.Header(IdempotencyHeader),
		WalletID:       ctx.Param("id"),
		Amount:         ctx.Input("amount"),
	}

	receipt, err := m.svc.Deposit(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	return m.receipt(ctx, receipt)
}

// withdraw takes money out of one wallet.
func (m *Module) withdraw(ctx *fhttp.Context) error {
	in := WithdrawRequest{
		IdempotencyKey: ctx.Header(IdempotencyHeader),
		WalletID:       ctx.Param("id"),
		Amount:         ctx.Input("amount"),
	}

	receipt, err := m.svc.Withdraw(ctx.Ctx(), m.subject(ctx.Request), in)
	if err != nil {
		return m.answer(ctx, err)
	}
	return m.receipt(ctx, receipt)
}

// transfer moves money from the wallet in the path into the one in the body.
func (m *Module) transfer(ctx *fhttp.Context) error {
	in := TransferRequest{
		IdempotencyKey: ctx.Header(IdempotencyHeader),
		FromWalletID:   ctx.Param("id"),
		ToWalletID:     ctx.Input("to_wallet_id"),
		Amount:         ctx.Input("amount"),
	}

	receipt, err := m.svc.Transfer(ctx.Ctx(), m.subject(ctx.Request), in)
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
	}

	receipt, err := m.svc.Reverse(ctx.Ctx(), m.subject(ctx.Request), in)
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
// The amounts are rendered at the scale of the wallet the entries moved, and a
// receipt with no entries -- which nothing here produces -- falls back to the
// integer, because a scale guessed at is worse than an integer nobody can
// misread.
func (m *Module) receipt(ctx *fhttp.Context, receipt Receipt) error {
	places := 0
	if len(receipt.Entries) > 0 {
		if record, err := m.svc.Find(ctx.Ctx(), m.subject(ctx.Request), receipt.Entries[0].WalletID); err == nil && record != nil {
			places = record.DecimalPlaces
		}
	}
	status := stdhttp.StatusCreated
	if receipt.Replayed {
		status = stdhttp.StatusOK
	}
	return ctx.JSON(status, NewReceiptResource(receipt, places))
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
	case errors.Is(err, ErrOperationConflict):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusConflict, "that idempotency key belongs to a different request")
		return nil

	// The request cannot be carried out against the money as it stands.
	case errors.Is(err, ErrInsufficientFunds):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "the balance is not enough")
		return nil
	case errors.Is(err, ErrCurrencyMismatch):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "those wallets are not counted the same way, and no rate provider is configured")
		return nil
	case errors.Is(err, ErrSameWallet):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "a transfer needs two different wallets")
		return nil
	case errors.Is(err, ErrNotReversible):
		fhttp.Refuse(ctx.Response, ctx.Request, stdhttp.StatusUnprocessableEntity, "a reversal cannot itself be reversed")
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
	return []foundation.Migration{createWallets{}, createWalletOperations{}, createWalletEntries{}}
}

// The migrations are reversible, and the assertions are here rather than
// discovered at rollback: the migrator tests for Down with a type assertion, so
// a Down with the wrong signature would leave a rollback that silently does
// nothing.
var (
	_ migrations.ReversibleMigration = createWallets{}
	_ migrations.ReversibleMigration = createWalletOperations{}
	_ migrations.ReversibleMigration = createWalletEntries{}
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
