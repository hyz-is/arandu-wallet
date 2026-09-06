# Upgrade Guide

Every heading below is a tag of this repository. Up to `v0.4.0` this file also
carried two sections describing releases of the package template this repository
was configured from -- `v0.4.0` and `v0.2.0`, with the entity renamed into them,
describing a publishing migration and a Repository removal that both happened
before `v0.1.0` of this package. They are gone, and what this package actually
changed at each of its own versions is below.

## Unreleased

Nothing yet.

## v0.6.0

### One new method, and one thing it is not

`(*WalletService).CanWithdraw` answers whether a wallet could pay out an amount,
without moving it. Nothing else changes, and nothing that exists behaves
differently.

```go
can, err := service.CanWithdraw(ctx, actor, walletID, "10.00")
```

It is a photograph. Between the answer and a withdrawal the balance can change,
so a caller that treats a `true` as permission has written the read-then-check
this package exists to avoid. What decides a withdrawal is still the predicate
on the update inside `Withdraw`. Use it to draw a button, not to decide whether
money may move.

It answers the same arithmetic the guard does -- the balance plus the credit
limit, against the amount -- and answers false for a wallet that is frozen or
closed, because those are the other two reasons the write refuses. It authorizes
`WalletView`, so a subject who may read the wallet may ask.

### Nothing else moved

The Service is split across seven files instead of one, all of them still
`package wallet`. `go doc -all` is what it was, so a project that compiled
against `v0.5.0` compiles against this unchanged.

Every column width now names the bound that validates the same field. A schema
already applied keeps the columns it has, and no value this package would write
is refused by either.

## v0.5.0

### MySQL is supported

`New` refused it, and the refusal was wrong: `hesape` supports the engine, and
this framework's own rule treats PostgreSQL, MySQL and SQLite as one repository
behind one interface rather than as three modes. Nothing in an application
changes to take this release on PostgreSQL or SQLite.

An application on MySQL upgrades to `v0.5.0` and runs `aru migrate` from
scratch: two of the migrations could not have applied there before this, so a
MySQL installation of an earlier version does not exist.

### Existing schemas are unchanged, and new ones are narrower

Three migrations declare narrower columns than they did. An installation that
already applied them does not run them again and keeps the columns it has; a new
installation gets the narrower ones. Nothing about either behaves differently,
because the bounds are ones this package already enforced in Go before the
statement was built:

- `meta` on `wallet_operations`, `wallet_entries` and `wallets` is bounded at
  `MaxMetaBytes` (4096) rather than unbounded. `ErrMetaTooLarge` already refused
  anything larger.
- The identifier columns are declared at 64 characters, and the two application
  keys -- `idempotency_key` and `product_key` -- at 191. An identifier here is a
  version 4 UUID as text, which is thirty-six characters.

To bring an existing PostgreSQL schema in line, which is optional:

```sql
alter table wallet_operations alter column meta type varchar(4096);
alter table wallet_entries    alter column meta type varchar(4096);
alter table wallets           alter column meta type varchar(4096);
```

### The isolation level moved to where the transaction opens

It was a `SET TRANSACTION ISOLATION LEVEL READ COMMITTED` as the first statement
inside the transaction. It is `TransactionAt` now, which hands the level to
`BeginTx`. Applications see no difference; what changes is that MySQL is
expressible at all, since it refuses a `SET` once a transaction is in progress.

An application that opened its own transaction and let this package join it is
unaffected: the joined transaction keeps the level it was opened at, exactly as
before.

### Upgrade the two floors

```sh
go get github.com/arandu-io/hesape@v0.26.0
go get github.com/arandu-io/framework@v0.46.1
```

## v0.4.1

Nothing to change in an application. This release corrects what the previous
ones said about themselves.

`CHANGELOG.md` and this file described the releases of the package template this
repository was configured from, with the entity renamed into them: `v0.4.0`
shipped a changelog whose `## [0.4.0]` section described a publishing migration
of the template, and filed everything that version actually added -- the two new
actions among them -- under `[Unreleased]`. Every heading in both files is now a
tag of this repository, and three tests hold it: an action or a migration this
package declares has to be named under a version heading rather than an
unreleased one, and the two files have to describe the same set of versions.

`rates/frankfurter` requires its parent from the proxy instead of replacing it
with the directory above. A consumer ignored that replace -- Go applies one only
from the main module -- but this repository's own gates did not, so the
submodule was being tested against the parent on disk rather than against what
was published.

## v0.4.0

### Answer two new actions

`WalletDescribe` and `WalletClose`. An application using `WalletPolicy` as it
ships needs no change -- the rules travel with it. An application that wrote its
own `security.Policy[Wallet]` refuses both until it answers them, and a refusal
is what a caller sees:

```go
// After, in the application's own policy.
case wallet.WalletDescribe:
	// relabelling: not creating, and it moves no money
	return holderOrOperator(s, record)
case wallet.WalletClose:
	// taking a wallet out of service, and putting it back
	return operatorOnly(s)
```

`WalletDescribe` is separate from `WalletCreate` because relabelling an existing
wallet and opening a new one are different things to be trusted with.
`WalletClose` is one action for both directions: an action per direction would
let somebody hold the half that stops other people's money moving.

### Run the two new migrations

`20260906_0011_add_wallet_description` and `20260906_0012_add_wallet_closure`
have to be applied with `aru migrate` before this version serves. The first adds
`description` and `meta` to `wallets`, both defaulting to empty; the second adds
`closed`, defaulting to the wallet being in service. Every row written before
them keeps exactly what it meant.

### A closed wallet refuses a movement, at the write

`ErrWalletClosed` joins `ErrWalletFrozen` as a reason a balance does not move,
and both are read by the statement that writes rather than by a check before it.
A caller that tested only `ErrWalletFrozen` now has a second value to test:

```go
// Before.
if errors.Is(err, wallet.ErrWalletFrozen) { /* out of service */ }

// After.
if errors.Is(err, wallet.ErrWalletFrozen) { /* the ledger stopped explaining it */ }
if errors.Is(err, wallet.ErrWalletClosed) { /* somebody took it out of service */ }
```

The two are different to act on: a freeze is lifted by `Rebuild`, a closure by
`Reopen`. `Resource` answers with both.

### Tell an empty wallet from a wallet that is merely short

A withdrawal from a wallet holding nothing, with no credit limit, now answers
`ErrBalanceEmpty` as well as `ErrInsufficientFunds`. Both are wrapped, so code
testing `ErrInsufficientFunds` reads exactly as it did; code that wants to tell a
first-time holder from an overdrawn one tests the new value first.

### Quote real rates, if you want them

`rates/frankfurter` is a Go module of its own inside this repository, and it is
the only part of it that talks to a network. An application that never crosses a
currency imports nothing new; one that does adds a second dependency:

```sh
go get github.com/hyz-is/arandu-wallet/rates/frankfurter@latest
```

The parent's manifest still says `network = false` and means it.

## v0.3.0

### Check the engine before upgrading

`New` now refuses a database handle speaking an engine this package's suite has
never run against, and it refuses it at construction rather than at the first
withdrawal:

```go
module, err := wallet.New(cfg, db, sessions)
// err wraps ErrUnsupportedDialect on anything but PostgreSQL and SQLite
```

The value is `ErrUnsupportedDialect`, and it is testable with `errors.Is`.

PostgreSQL and SQLite are what it covers. **An application on MySQL that upgrades
to this version fails to boot**, and that is deliberate: the guard on a
withdrawal is a predicate on an update, what an update sees of a row another
transaction is changing is the engine's answer, and an engine no test here has
interleaved two withdrawals on is an engine whose answer nobody has read. The SQL
would compile; the guarantee would not travel.

### Answer one new action

`WalletReconcile`, which `Reconcile` and `Rebuild` ask about. It is the
operator's and not the holder's: what those two write is a wallet that no longer
moves, or a ledger row no request produced. `Reconcile` asked about
`WalletHistory` before this version, when all it did was read and report; it
freezes now, so the decision it needs is no longer a share of reading a ledger.

```go
// After, in the application's own policy.
case wallet.WalletReconcile:
	return operatorOnly(s)
```

### Run the new migration

`20260906_0010_add_wallet_freeze` adds the `frozen` column to `wallets`,
defaulting to the wallet being in service. `aru migrate` before serving.

### A conflict is retried, and what survives it has a name

A movement the engine refuses as a conflict with another transaction --
serialization failure or deadlock -- is sent again with a widening random pause.
Sending it again is safe: the operation and the movements reach the transaction
as values, so no rate, fee, discount or product is asked twice, and an attempt
that did commit is answered by its own idempotency key rather than repeated.

A conflict surviving four attempts is `ErrConcurrencyConflict`, which says
nothing was written and the same request can be sent again:

```go
if errors.Is(err, wallet.ErrConcurrencyConflict) {
	// nothing moved; send it again
}
```

Code that caught the driver's own error to detect this can stop.

### A wallet whose ledger stopped explaining it stops moving

`Reconcile` now freezes a wallet whose ledger and balance disagree, and every
balance statement carries the column in its own predicate -- so a wallet frozen
between a read and a write is refused at the write, not before it.

`Rebuild` closes the difference by **appending** the settled entry the ledger was
missing. The balance column is not touched: the repair is a row somebody can
read rather than a value somebody changed. It refuses a wallet that is not
frozen (`ErrWalletNotFrozen`), one whose ledger already balances
(`ErrLedgerBalanced`), and one that moved between the reconciliation and the
repair (`ErrLedgerMoved`).

### Isolation is declared, not inherited

Every transaction this package opens now names read committed as its first
statement instead of taking the engine's default, which an operator can change
for a whole cluster. A transaction the application had already opened is joined
and left at the level it chose.

## v0.2.1

Nothing to change, and everything to reinstall. The published `v0.2.0` archive
was missing its view sources -- `go mod` drops every path with a segment named
`vendor` when it packs a module, so a project that imported the package failed to
build with `pattern resources/views: no matching files found`. The files land at
the same addresses under the same view names; what changed is where the archive
carries them.

## v0.2.0

### Give the module a token issuer

`Config.CSRF` is required. Every screen this module draws moves money, so a page
rendered without a token is a page whose forms the application refuses -- and
finding that out from a button that does nothing is worse than finding it out at
boot.

```go
// Before.
module, err := wallet.New(wallet.Config{Tenant: "acme"}, db, sessions)

// After.
module, err := wallet.New(wallet.Config{
	Tenant: "acme",
	CSRF:   security.NewCSRF(appKey, time.Hour),
}, db, sessions)
```

### Say which side of a transfer waits

`TransferRequest.Pending` is gone, and the two sides answer separately. A payment
where what leaves counts now and what arrives waits is money held until somebody
says it may be delivered; the other way round is a delivery on credit. Neither is
expressible by one flag over the pair, and a shorthand beside the two would be a
second way to say what one of them already says.

```go
// Before.
in := wallet.TransferRequest{..., Pending: true}

// After.
in := wallet.TransferRequest{...,
	Withdrawal: wallet.Leg{Pending: true},
	Deposit:    wallet.Leg{Pending: true},
}
```

Over HTTP the transfer route reads `withdrawal_pending` and `deposit_pending`
instead of `pending`. The deposit and withdrawal routes are unchanged: a movement
with one side has nothing to say separately about it.

### Nothing else has to change for the listeners

`NewWalletService` gained a variadic parameter, so every existing call compiles
as it stands:

```go
// Before, and still correct.
service := wallet.NewWalletService(db, rates, fees, discounts)

// After, for an application that wants to be told what the money did.
service := wallet.NewWalletService(db, rates, fees, discounts, audit.Record)
```

### Run the two new migrations

`20260905_0008_add_wallet_metadata` and `20260905_0009_create_wallet_purchases`
have to be applied with `aru migrate` before this version serves. The first adds
a `meta` column to the operations table and to the ledger, defaulting to the
empty string, so every row written before it keeps exactly what it meant. The
second creates the table a purchase is recorded in, which nothing older wrote.

### Read a rate, and let this package apply it

`RateProvider` answers with a rate instead of a converted amount. Applying it,
rounding it and recording it belong here now, so every exchange in an
application is rounded the same way and leaves the same row behind whatever the
provider is -- a provider that returned the amount left no rate to record.

```go
// Before.
ConvertTo(ctx context.Context, g security.Grant, m Money, to Currency, places int) (Money, error)

// After.
Rate(ctx context.Context, g security.Grant, from, to Currency) (Rate, error)
```

### Give the response builders what an exchange needs

An exchange writes two entries counted differently, and one scale for both moves
the decimal point on one of them. `NewEntryResource`, `NewEntryCollection` and
`NewReceiptResource` take what they were missing:

```go
// Before.
NewEntryResource(entry, scale)
NewEntryCollection(entries, scale, path)
NewReceiptResource(receipt, scale)

// After.
NewEntryResource(entry, scale, operationKind)
NewEntryCollection(statement, path)
NewReceiptResource(receipt, map[string]int{walletID: scale})
```

### Hand the service its two new seams

`NewWalletService` takes what prices a payment beside what quotes a rate. An
application that charges nothing passes nil twice, which is what it did before
without having to say so:

```go
// Before.
service := wallet.NewWalletService(db, rates)

// After.
service := wallet.NewWalletService(db, rates, nil, nil)
```

A module built through `wallet.New` needs no change: it reads `Config.Rates`,
`Config.Fees` and `Config.Discounts`, and the two new fields default to nil.

### Read what an operation settles under its new name

`Operation.ReversesID` is now `Operation.SettlesID`. The column behind it keeps
the name it was created under, so no data moves and no migration renames
anything; what changed is the field, because the value it holds is now the
operation a row reverses **or** the one it confirms.

```go
// Before.
if operation.ReversesID != operation.ID {
	// it undoes something
}

// After.
if undone := operation.Reverses(); undone != "" {
	// it undoes that
}
if settled := operation.Confirms(); settled != "" {
	// it makes that count
}
```

`Reverses()` answers exactly what it answered before. `Confirms()` is its twin
for the new kind, and each is empty where the row is not that.

### Sum the ledger the way it was always summed

`Entry.Signed()` now answers zero for an entry that has not settled, so the sum
of it over a wallet's whole history is still the balance column. Code that
totals `Signed()` needs no change. Code that reads `Entry.Amount` and applies
the sign itself has to read `Entry.Settled` as well, or it counts money that has
not moved.

```go
// Before, and still correct.
total += entry.Signed()

// Before, and now wrong.
if entry.Kind == wallet.EntryWithdraw {
	total -= entry.Amount
} else {
	total += entry.Amount
}
```

### Run the new migrations

`aru migrate` before serving this version:
`20260905_0005_add_wallet_credit_limit`,
`20260905_0006_add_wallet_entry_settlement` and
`20260905_0007_create_wallet_charges`. The first two add a column with a default
that leaves every existing row meaning exactly what it meant -- a floor of zero,
and a movement that counted -- and the third is a new table nothing reads until
something charges.
## v0.1.0

The first release. Nothing to upgrade from.
