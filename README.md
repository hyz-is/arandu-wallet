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
		CSRF:   cfg.CSRF,
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

This package owns six tables -- the wallets, the operations, the ledger, the
recorded rates, the recorded charges and the purchases -- which is why the
migration step is not optional and why `arandu.mod.toml` says
`migrations = true`.

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

The files land under `resources/views/vendor/wallet/`, and from that point
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
	_ "your/module/path/storage/framework/views/vendor/wallet"
```

Without that import the views are not in the binary, and the module refuses to
boot rather than answering the first request that reaches one of them with a
500. The refusal names the view, the command and the import.

## Configuration

| field | required | meaning |
| --- | --- | --- |
| `Tenant` | yes | the customer a visitor with no session is read as. From the application's configuration, never from the request. |
| `Prefix` | no | where the routes are mounted. Defaults to `/wallet`. |
| `PageSize` | no | how many records one page answers with. Defaults to 25, refused above 200. |
| `Rates` | no | quotes the rate between two currencies. Nil refuses every transfer that would need one. |
| `Fees` | no | answers with what a wallet charges to be paid. Nil charges nothing. |
| `Discounts` | no | answers with what one payer is charged less. Nil discounts nothing. |
| `CSRF` | yes | issues the token every form on the screens carries. Every screen here moves money. |
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

## Be told what the money did

```go
	Listeners: []wallet.Listener{func(ctx context.Context, e wallet.Event) {
		log.Printf("%s %s on %s", e.Kind, e.Amount.Format(e.DecimalPlaces), e.WalletID)
	}},
```

A listener is called after the write has committed, never inside it. A basket
that runs out of money on its last line announces nothing at all, though every
earlier line really moved a balance for as long as the transaction lasted.

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

## Model-first data path

`Wallet` embeds `model.Model[Wallet]`, and `Wallets(db)` is the one
configured entry point for its table. `WalletService` owns `*data.DB` and
follows `validate -> security.Authorize -> Grant -> Model terminal`; handlers
never hold the database or construct a Model.

Create writes `TenantID` from `data.Tenant(g)`. Find authorizes before reading
and again against the row it found. List authorizes before building its scoped,
allowlisted query. The Model keeps its default `tenant_id` scope on every
terminal.

Terminals return `*Wallet` and `[]*Wallet`. Keep those pointers intact:
copying an embedded Model leaves its `Entity` pointer aimed at the original
allocation. `Resource` and `Collection` are explicit response snapshots and do
not expose tenant or Model internals.

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
policy.go      who may do what
service.go     the rules and authorized Model access
event.go       what a listener is told, once the write has committed
commands.go    what an operator runs from a terminal
translation.go the sentences the screens say
views.go       the screens, and the files the application takes ownership of
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
