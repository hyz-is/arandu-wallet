<p align="center">
  <img src=".github/logo.svg" alt="Arandu" width="180">
</p>

# Arandu Wallet

An Arandu package. It registers its own routes, owns its own tables, and decides
for itself who may reach either.

## Install

```bash
go get github.com/hyz-is/arandu-wallet
```

## Wire it

An Arandu application registers a module explicitly. There is no service
provider, no container and no discovery, so these are the lines to paste into
`bootstrap/app.go` and there are no others.

The import, with the other module imports:

```go
import (
	wallet "github.com/hyz-is/arandu-wallet"
)
```

The construction, in `Build`, after the session store exists and before
`k.Register`:

```go
	walletModule, err := wallet.New(wallet.Config{
		Tenant: cfg.Auth.Tenant,
	}, db, sessions)
	if err != nil {
		return App{}, err
	}
```

And the registration, inside the `k.Register(...)` call already there:

```go
		walletModule,
```

Then, once, before the application serves:

```bash
aru migrate
```

This package owns eleven tables -- the wallets (`wallets`), the operations
(`wallet_operations`), the ledger (`wallet_entries`), the recorded rates
(`wallet_conversions`), the recorded charges (`wallet_charges`), the purchases
(`wallet_purchases`), the holds (`wallet_holds`), the payout accounts
(`wallet_payout_accounts`), the withdrawal requests
(`wallet_withdrawal_requests`), and the credit agreements and their
installments (`wallet_credit_agreements`, `wallet_credit_installments`) --
which is why the migration step is not optional and why `arandu.mod.toml` says
`migrations = true`. Run it again after every upgrade: a release that adds a
column ships the migration that adds it, and the CHANGELOG names it.

## Quote a rate, if your wallets are not all counted the same way

A transfer between two wallets that hold different currencies, or the same
currency at different scales, is an exchange. It needs a rate, and a rate comes
from outside the process, so this package does not fetch one: `arandu.mod.toml`
says `network = false` and means it.

Leave `Config.Rates` nil and every such transfer is refused with
`ErrCurrencyMismatch`. That is the ordinary case, not a degraded one -- an
application whose wallets all hold one currency never needs a rate.

To allow them, supply the provider you trust:

```go
type RateProvider interface {
	Rate(ctx context.Context, g security.Grant, from, to Currency) (Rate, error)
}
```

It answers with the rate, not with a converted amount. Applying it, rounding it
and recording it belong to this package, so every exchange in the application is
rounded the same way and leaves the same row behind whatever the provider is.
A `Rate` is the exact fraction `Numerator/Denominator` -- 5.4321 is
`54321/10000` -- because a rate held as a float is already a different rate than
the one somebody quoted.

The `Grant` comes first because a rate can be a tenant's own. A provider that
cannot tell whose rate it is asked for is a provider that answers with somebody
else's.

## Publish the views

This package carries the markup of its own pages and hands it over instead of
rendering it from the inside, because a page you cannot edit is a page that says
the wrong thing in your product.

Look at what would be written, then write it:

```bash
aru vendor:publish --tag=view
aru vendor:publish --tag=view --apply
```

Nothing is written without `--apply`. The preview lists every file as `create`,
`update`, `unchanged` or `conflict`, and running the command a second time
writes nothing. A file changed outside its `arandu:begin custom` markers is
reported as a conflict and left alone; `--force` publishes over one, and even
then what is inside the markers is carried forward.

The files land under `resources/views/modules/wallet/`, and from that point
they are yours. Nothing of this package is compiled beside them, so no view name
is registered twice and no rule has to decide which of two files won — the
consequence being that a view of this package that changes later does not reach
a project that already published it.

Two steps are left to you, and they are left to you because a command that
edited `bootstrap/app.go` behind your back is a command whose output nobody can
explain. Compile what was written:

```bash
aru view:build
```

and import the directory it wrote into, with the other imports:

```go
	_ "your/module/path/storage/framework/views/modules/wallet"
```

Without that import the views are not in the binary, and the module refuses to
boot rather than answering the first request that reaches one of them with a
500. The refusal names the view, the command and the import.

The three screens are drawn inside your layout with the `view.Page` that
`view.New` builds for your own screens. The CSRF token is the one
`middleware.CSRFProtect` issued for the request, so the routes have to sit
behind it, as every route of the application skeleton does. The brand, sign-in,
sign-out and register links come from the routes you named `home`,
`auth.login`, `auth.logout` and `auth.register`; one you did not register draws
no link.

## Configuration

| field | required | meaning |
| --- | --- | --- |
| `Tenant` | yes | the customer a visitor with no session is read as. From the application's configuration, never from the request. |
| `Prefix` | no | where the routes are mounted. Defaults to `/wallet`. |
| `PageSize` | no | how many records one page answers with. Defaults to 25, refused above 200. |
| `Rates` | no | quotes the rate between two currencies. Nil refuses every transfer that would need one. |
| `Fees` | no | answers with what a wallet charges to be paid. Nil charges nothing. |
| `Discounts` | no | answers with what one payer is charged less. Nil discounts nothing. |
| `Translator` | no | your own catalogue, asked before the one this package ships. |
| `Listeners` | no | told what the money did, after the write has committed. |

`New` returns an error rather than starting half-wired, so a setting that
cannot work fails where it is written instead of on the first request that
needed it.

## Routes

| method | path | name |
| --- | --- | --- |
| `GET` | `/wallet` | `wallet.index` |
| `POST` | `/wallet` | `wallet.store` |
| `GET` | `/wallet/{id}` | `wallet.show` |
| `GET` | `/wallet/{id}/entries` | `wallet.entries` |
| `PUT` | `/wallet/{id}/credit` | `wallet.credit` |
| `POST` | `/wallet/{id}/deposits` | `wallet.deposit` |
| `POST` | `/wallet/{id}/withdrawals` | `wallet.withdraw` |
| `POST` | `/wallet/{id}/transfers` | `wallet.transfer` |
| `GET` | `/wallet/{id}/purchases` | `wallet.purchases` |
| `POST` | `/wallet/operations/{operation}/reversals` | `wallet.reverse` |
| `POST` | `/wallet/operations/{operation}/confirmations` | `wallet.confirm` |
| `POST` | `/wallet/purchases/refunds` | `wallet.refund` |
| `GET` | `/wallet/payouts/accounts` | `wallet.payout_accounts` |
| `POST` | `/wallet/payouts/accounts` | `wallet.payout_accounts.store` |
| `DELETE` | `/wallet/payouts/accounts/{account}` | `wallet.payout_accounts.disable` |
| `POST` | `/wallet/{id}/withdrawal-requests` | `wallet.withdrawals.request` |
| `GET` | `/wallet/payouts/requests` | `wallet.withdrawals.index` |
| `DELETE` | `/wallet/payouts/requests/{request}` | `wallet.withdrawals.cancel` |
| `PUT` | `/wallet/payouts/requests/{request}/processing` | `wallet.withdrawals.process` |
| `POST` | `/wallet/payouts/requests/{request}/payment` | `wallet.withdrawals.pay` |
| `POST` | `/wallet/payouts/requests/{request}/rejection` | `wallet.withdrawals.reject` |
| `GET` | `/wallet/payouts/requests/{request}/receipt` | `wallet.withdrawals.receipt` |

Every one of them is refused until the policy is opened. That is the state the
package ships in, and it is deliberate.

Each `GET` answers JSON to a client that asks for it and a page to a browser, so
there is one address per thing rather than one for people and one for programs.

Paying for a basket has no route. A basket names products, and a product is your
type: what is for sale, what it costs this customer and how many are left are
three questions this package has never been able to answer. `Pay` is a Go call
you make from the handler that owns your catalogue.

## Sell something

What is for sale is yours, through an interface this package declares and never
implements:

```go
type Book struct{ ... }

func (b *Book) ProductKey() string       { return b.sku }
func (b *Book) ReceiverWalletID() string { return b.shopWalletID }
func (b *Book) Price(ctx context.Context, g security.Grant, buyer wallet.Wallet) (wallet.Amount, error) {
	return b.price, nil
}
```

A catalogue that keeps a stock answers `LimitedProduct.CanBuy` as well, and it is
asked before a single balance is touched.

```go
receipt, err := svc.Pay(ctx, actor, wallet.PayRequest{
	IdempotencyKey: key,
	PayerWalletID:  buyer.ID,
	Cart: wallet.NewCart(
		wallet.CartItem{Product: book, Quantity: 2},
		wallet.CartItem{Product: pen, BeneficiaryWalletID: friend.ID},
	),
})
```

Every line is one movement out of the payer, one into the wallet that sells it
and one into whoever collects the fee, all in one operation and one transaction.
A line bought for somebody else is a gift: the money still leaves the payer, and
the record says the beneficiary bought it -- which is what `Bought` reads
afterwards, one statement for a whole page of questions.

A basket is undone line by line, with `Refund`, so a basket half of which was
already given back cannot be given back whole.

## Pay out through requests

Earnings somebody else pays out -- an investor's share, an affiliate's
commission -- go in a wallet opened with `PayoutByRequest: true`. Its holder
cannot withdraw, pay, transfer or hold from it; the only way out is a request
an operator pays:

```go
account, err := svc.AddPayoutAccount(ctx, holder, wallet.PayoutAccountRequest{
	HolderID: holder.ID, Kind: "pix", Label: "Minha chave",
	Details: wallet.Meta{"key": "ana@example.com", "key_type": "email"},
	HolderName: "Ana", HolderDocument: "52998224725",
})
req, err := svc.RequestWithdrawal(ctx, holder, wallet.WithdrawalRequestInput{
	IdempotencyKey: key, WalletID: w.ID, Amount: "250.00", AccountID: account.ID,
	Reference: "invoice:2026-10",
})
// the operator, after sending the PIX:
paid, err := svc.PayWithdrawal(ctx, operator, wallet.PaymentInput{
	RequestID: req.ID, EndToEndID: e2e, ReceiptName: "comprovante.pdf", ReceiptType: "application/pdf",
}, receipt)
```

The amount is held when the request is made, captured when it is paid, and
released when it is rejected or cancelled. Pass `Config.Receipts`, a Disk, for
the receipts. A consumer's own rules -- a minimum, one request a day, the whole
of an invoice -- are checked by the consumer before it asks.

A consumer that keeps such rules opens the wallet with `PayoutVia` naming
itself, and asks with the same name in `WithdrawalRequestInput.Via`. Any other
request for that wallet -- this package's own `POST /{id}/withdrawal-requests`
sets no `Via` -- is refused with `ErrWithdrawalElsewhere`, a 403, so a holder
cannot step around the consumer by asking the wallet directly:

```go
w, err := svc.Open(ctx, operator, wallet.OpenRequest{
	HolderID: userID, Slug: "affiliate", Name: "Comissões", Currency: "BRL", DecimalPlaces: 2,
	PayoutVia: "affiliate", // implies PayoutByRequest
})
req, err := svc.RequestWithdrawal(ctx, holder, wallet.WithdrawalRequestInput{
	IdempotencyKey: key, WalletID: w.ID, Amount: "150.00", AccountID: account.ID, Via: "affiliate",
})
```

## Sell on credit

A store that sells on its own credit -- a crediário -- approves a holder by
setting the limit of a wallet opened for them, and opens an agreement for each
purchase. The total is drawn from the wallet, so the balance is the debt, and
each installment paid is deposited, so the ledger explains it.

```go
statement, err := walletService.OpenCredit(ctx, wallet.OperatorFor(g), wallet.CreditOpenRequest{
	IdempotencyKey: "order-123",
	WalletID:       credit.ID,
	Principal:      "1200.00",
	Installments:   6,
	FirstDue:       "2026-11-10",
	TimeZone:       "America/Sao_Paulo",
	LateFeeBP:      200, // 2%
	MoraRateBP:     100, // 1% a month, by the day
	Terms:          wallet.Meta{"text": termsShown},
	AcceptedAt:     time.Now(),
	AcceptedFrom:   clientIP,
})
```

`QuoteInstallment` says what an installment costs on a day -- its amount, and
after its date a late fee once and interest by the day -- and `PayInstallment`
records it paid. `SweepCredit`, once a day, marks what is overdue and what is
delinquent and tells the listeners, once each. `PayOffCredit` clears an
agreement taking off the interest not yet due, and `CancelCredit` clears what
is still owed. Reporting a holder to a credit bureau is the creditor's act;
`MarkCreditReported` keeps when they did it and when they withdrew it.

`Credits` lists agreements newest first, a page at a time, and finds them by
the creditor's reference -- exactly, or by its start:

```go
page, err := walletService.Credits(ctx, operator, wallet.CreditQuery{
	Reference: "order-2026-10", ReferencePrefix: true, // every order of October
	Limit:     50,                                     // at most wallet.MaxPageSize
})
// a full page has a next one: pass page[len(page)-1].ID as Cursor
```

## Hold before work whose outcome is unknown

Some work has a known price and an unknown outcome: a lookup that may fail, a
job that may not run. Hold the money first, then capture or release it:

```go
h, err := svc.Hold(ctx, actor, wallet.HoldRequest{
	IdempotencyKey: "lookup:" + lookupID, WalletID: w.ID, Amount: "1",
	ExpiresAt: time.Now().Add(10 * time.Minute),
})
// ... the work ...
_, err = svc.Capture(ctx, actor, wallet.CaptureRequest{IdempotencyKey: "capture:" + lookupID, HoldID: h.ID})
// or, when it did not happen:
_, err = svc.Release(ctx, actor, wallet.ReleaseRequest{HoldID: h.ID, Reason: "the lookup failed"})
```

A hold sets the amount aside without moving the balance: `Wallet.Held` rises,
and every withdrawal and every other hold is guarded on the balance less it, in
the statement that writes. A capture is one transaction with the withdrawal; a
release writes nothing to the ledger, because nothing moved. A hold past its
`ExpiresAt` is never captured, and `ReleaseExpired`, run by an operator on a
schedule, gives the expired ones back.

A capture may take part of a hold, and may send the money to another wallet
instead of out of the ledger -- a buyer's hold paid to the seller:

```go
_, err = svc.Capture(ctx, actor, wallet.CaptureRequest{
	IdempotencyKey: "capture:" + orderID, HoldID: h.ID,
	ToWalletID: seller.ID, // a transfer, counted the same way; empty is a withdrawal
	Amount:     "80.00",   // part of the hold; empty is all of it
})
```

What a partial capture leaves of the hold is given back by the same statement
that takes the rest, so a hold settles once; `Hold.CapturedAmount` says what was
taken.

## Withdraw a proposal, or give back part of a payment

A movement recorded with `Pending` ends in one of two ways: `Confirm` makes it
count, and `Void` withdraws it. A void moves nothing -- it appends the opposite
pending entry beside each proposed one -- and the database keeps a void and a
confirmation of the same operation from both succeeding.

`ReversePartially` gives back part of what a deposit, a withdrawal or a
transfer without a fee moved, as many times as it takes, never past what it
moved in total; `Operation.ReversedAmount` is what was given back. An operation
reversed in part is no longer reversed whole with `Reverse`. The wallet the
money leaves is held to its balance and credit limit; `Force: true` gives the
part back anyway -- a refund owed after the payee spent the money -- and is
answered by `WalletForce`, so it stays the operator's and the cap still holds.

```go
_, err = svc.Void(ctx, operator, wallet.VoidRequest{
	IdempotencyKey: "void:" + orderID, OperationID: pendingID, Reason: "order cancelled",
})
_, err = svc.ReversePartially(ctx, operator, wallet.PartialReversalRequest{
	IdempotencyKey: "refund:" + itemID, OperationID: paymentID, Amount: "25.00",
	Reason: "one item returned",
})
```

Both are the operator's (`WalletVoid`, `WalletReverse`).

## Read a period

`History` narrows a statement to a period -- `From` inclusive, `Until`
exclusive -- read by when each entry was recorded, or with `Basis:
wallet.DateAccrued` by the date its operation said the money is about. Every
movement request carries an optional `AccruedAt` for that; a confirmation keeps
the date of what it confirms, and an operation without one accrues when it was
recorded. `BalanceAt` answers what a wallet held at an instant, read off the
last entry at or before it. The ledger keeps its timestamps to the microsecond
on PostgreSQL and MySQL, and to the second on SQLite.

`OperationByKey` finds what a request did by the idempotency key it was sent
with -- the question to ask after a timeout -- showing the subject only the
wallets they may read.

## Page through operations for a data table

`ListOperations` is the query a server-side data table pages through: the
operations of the subject's tenant, narrowed, ordered and counted in the
database. A row (`OperationRow`) is an operation as it moved one wallet -- a
deposit is one row, a transfer between two wallets read together is two -- and
carries the wallet's currency and scale, the operation's kind and metadata, and
its status (`OperationPending`, `OperationConfirmed`, `OperationVoided`,
`OperationSettled`, `OperationPartiallyReversed`, `OperationReversed`), derived
from the confirmations, voids and reversals naming it.

```go
page, err := walletService.ListOperations(ctx, subject, wallet.OperationFilter{
	WalletIDs: []string{credits.ID, reais.ID},
	Kinds:     []wallet.OperationKind{wallet.OperationDeposit},
	Meta:      wallet.Meta{"reason": "payment"}, // equality on every name
	Statuses:  []wallet.OperationStatus{wallet.OperationPending, wallet.OperationConfirmed},
	Search:    ctx.Query("search"),               // ids, key, reason, metadata
	Sort:      wallet.OperationsNewest,           // or OperationSortFor(column, dir)
	Cursor:    ctx.Query("cursor"),               // page.NextCursor / page.PrevCursor
	Limit:     25,
	Count:     true,                              // page.Total, one more statement
})
```

Pages are a keyset by default, which costs the same on every page of a ledger
of millions; `Page: n` asks for a numbered page read by offset instead, for a
table that draws page numbers. `HolderID`, `Direction`, `From`/`Until` and
`Basis` narrow further. The policy is asked what `History` asks, for every
wallet named; a holder reads only their own wallets whatever the filter says.

`ListAllOperations` is the same listing across every tenant, with
`PlatformOperationFilter.Tenants` to keep some: the platform's console. It is
answered for `WalletOperationsAll`, which a tenant's operator does not hold --
only a platform operator: `wallet.PlatformRole`, `wallet.WalletOperationsAll`
granted through arandu-permission, or the subject `wallet.PlatformOperatorFor(g)`
derives from a Grant your own platform policy issued. Each row says its tenant.

Do not read a page of `History` and filter it in memory: whatever falls off that
page is gone from the screen. Run `aru migrate` after upgrading to v0.18.0:
`20261023_0025_add_wallet_listing_indexes` adds the indexes these filters read.

## Act as the operator after your own policy decided

Money that belongs to nobody who signs in -- an organisation's wallet -- is
moved by the operator. Ask your own policy first and derive the operator from
the Grant it issued:

```go
g, err := security.Authorize(ctx, creditPolicy, person, CreditManage, scope)
if err != nil {
	return err
}
operator, err := wallet.OperatorFor(g)
```

The operator reaches the wallets of the Grant's tenant and no other: the wallet
policy checks the tenant before the role on every call. An application whose
permissions arandu-permission administers grants `wallet.OperatorAction`
(`wallet.operator`) instead of the role, and `wallet.IsOperator` answers both.

## Be told what the money did

```go
	Listeners: []wallet.Listener{func(ctx context.Context, e wallet.Event) {
		log.Printf("%s %s on %s", e.Kind, e.Amount.Format(e.DecimalPlaces), e.WalletID)
	}},
```

A listener is called after the write has committed, never inside it. A basket
that runs out of money on its last line announces nothing at all, though every
earlier line really moved a balance for as long as the transaction lasted. A
listener that panics is recovered and logged through `log/slog`; the listeners
after it are still told, and the caller still gets the receipt of the write
that committed.

There is no dispatcher and no queue here: an application that wants the work off
the request hands it to whatever it already uses.

## Give an operator something to run

```go
	commands, err := wallet.Commands(wallet.Deps{
		Service:  walletModule.Service(),
		Operator: func(tenant string) security.Subject { return app.Operator(tenant) },
	})
```

`wallet:wallets`, `wallet:statement`, `wallet:purchases` and `wallet:audit`.
Every one of them reads, and the last one reports what a ledger adds up to
without ever repairing it. Who a command runs as is yours to answer: a package
that minted a subject for itself would be a package that authorizes itself.

## Open the policy

`policy.go` denies every action and has no branch that allows one. Open what
this package needs, one action at a time, inside the custom block:

```go
	// arandu:begin custom
	if a == WalletView && (s.ID == record.ID || s.HasRole("admin")) {
		return nil
	}
	// arandu:end custom
```

What is not written there stays closed, including every action added later.

## Prove your wiring keeps the wallet's promises

`github.com/hyz-is/arandu-wallet/conformance` checks the package's promises
over your own composition: the ledger is append-only, every balance is the sum
of its entries, one idempotency key is one operation, a hold settles exactly
once, another Organization reaches no wallet, and a Grant issued to read cannot
write. Call it in one line from `tests/Conformance`, with a fixture built from
your own wiring over a fresh database:

```go
func TestWallet(t *testing.T) { conformance.Wallet(t, tests.WalletFixture) }

// in tests/: the Service and DB of the Module your bootstrap built, an Account
// function that creates an account where your SubjectResolver reads it and
// answers the subject it resolves, and -- when you register the wallet's
// routes -- the server test.Serve booted.
func WalletFixture(t *testing.T) conformance.Fixture {
	app := Boot(t) // your composition over test.Serve
	return conformance.Fixture{
		Service: app.Wallet.Service(),
		DB:      app.Server.DB(),
		Account: func(t *testing.T, organization string, operator bool) security.Subject {
			return CreateAccount(t, app, organization, operator)
		},
		Server: app.Server,
	}
}
```

Each failure is a sentence naming what the application broke: an account
resolved with the operator role it was never granted, an Organization not
carried as the tenant, a module built over a session store the guards do not
read. `tests/Conformance` here runs the kit against a small application on
SQLite and, with `ARANDU_TEST_DATABASE_URL` or `ARANDU_TEST_POSTGRES_DSN` set,
on PostgreSQL.

## Model-first data path

`Wallet` embeds `model.Model`, and `Wallets(db)` is the one entry point for its
table: the `WalletQuery` that `aru model:build` generates in `WalletQuery.go`,
over the `walletTable` declared beside the entity. `WalletService` owns
`*data.DB` and follows
`validate -> security.Authorize -> Grant -> Model terminal`; handlers never
hold the database or construct a Model.

Create writes `TenantID` from `data.Tenant(g)`. Find authorizes before reading
and again against the row it found. List authorizes before building its scoped,
allowlisted query. The Model keeps its default `tenant_id` scope on every
terminal.

Terminals return `*Wallet` and `WalletCollection` (`[]*Wallet`). Keep those
pointers intact: a copy of an entity shares the row state of the original, so a
promoted read answers from the original and a promoted write refuses with
`model.ErrUnwired`. `Resource` and `Collection` are explicit response snapshots
and do not expose tenant or Model internals.

There is no CRUD Repository. Add one only for a complex query, read model,
report, export or raw SQL contract that the common Model path cannot express.

## Layout

```
module.go      registration, routes, handlers and migrations
config.go      what the application passes in
money.go       the amount type, its scale and its arithmetic
rate.go        the rate, its arithmetic, and the seam that quotes it
fee.go         the fee, its arithmetic, and the seams that price a payment
meta.go        what the application attaches to a movement
cart.go        the basket, and the seams that say what is for sale
model.go       the entities, and what they may answer with
purchase.go    the record of what was bought, and its own read model
wallet.go      opening a wallet, naming it, finding it, taking it out of service
movement.go    deposit, withdrawal, transfer, reversal, confirmation, statement
undo.go        a proposal withdrawn, and part of an operation given back
payment.go     a basket paid for, and a line given back
reconcile.go   a ledger that stopped explaining its balance, and its repair
commit.go      the engine: one operation, one transaction, the guarded write
withdrawal.go  payout accounts and the withdrawal requests an operator pays
credit.go      credit agreements: installments against a credit limit
hold.go        money set aside before it is taken, and how a hold settles
operator.go    the operator derived from an application's own decision
listing.go     the operations a data table pages through, in a tenant and in all
policy.go      who may do what
service.go     the rules and authorized Model access
event.go       what a listener is told, once the write has committed
commands.go    what an operator runs from a terminal
translation.go the sentences the screens say
views.go       the screens, and the files the application takes ownership of
conformance/   the kit an application runs over its own wiring
```

See it run, against SQLite in a temporary directory, with nothing to configure:

```bash
go run -tags example ./example
```

## What is already correct, and has to stay that way

**The policy denies everything.** There is no permit-all branch to delete
later. The Service calls `security.Authorize` before its first `Wallets(db)`
reach, and every Model terminal requires the Grant that call produced.

**Authorization precedes the Model.** The package audit checks that order in
every exported Service method. A Model terminal enforces tenant scope; the
preceding Policy call decides whether the action itself is allowed.

**The tenant comes from `data.Tenant(g)`.** Never from the path, the body, the
query string or a header. The value on the Grant came from the session; a value
that arrived with the request is a value the caller chose.

**`arandu.mod.toml` declares what the package does** — network, filesystem,
exec, migrations — and the suite compares the declaration against what the code
*calls*, not against what it imports: `net/http` is imported by everything with
a route and says nothing. A package that says it makes no outbound calls and
then opens one fails its own tests, and that is the only place the comparison
happens. `aru doctor` audits the application it is run inside and never loads a
dependency, so nothing audits an installed package except the package itself.

## Tests

```bash
go test -race ./...
```

The denial suite constructs the Service with a nil database, so even building
`Wallets(nil)` would panic. The structural twin reads the allowed path and
rejects any Service method that reaches the Model before `Authorize`.

## Licence

MIT. See [LICENSE.md](LICENSE.md). Copyright Paulo R. Lima.
