# Upgrade Guide

## Unreleased

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

## v0.4.0

Version 0.4.0 hands publishing to the framework. The package no longer defines
the contract or carries the command that writes the files. Upgrade Framework to
`v0.46.0` and Hesape to `v0.25.0` before changing anything below.

### Publish with the CLI

```sh
# Before.
go run github.com/hyz-is/arandu-wallet/publish@latest
go run github.com/hyz-is/arandu-wallet/publish@latest --force

# After.
aru vendor:publish --tag=view
aru vendor:publish --tag=view --apply
aru vendor:publish --tag=view --apply --force
```

The `publish` command of this module was removed. `aru vendor:publish` asks the
application which modules it registered and writes what each of them declares,
so one command publishes every installed package instead of one command per
package. Without `--apply` it writes nothing and prints what each file would
become; running it twice changes nothing the second time.

`PublishCommand` changed from `go run <module>/publish@latest` to
`aru vendor:publish --apply`. It is what `(*Module).Boot` names in its refusal,
and an application that prints it anywhere of its own gets the new spelling by
recompiling.

### Answer the framework's publishing contract

`Publishable`, declared by this package, was removed. The contract is
`foundation.Publishable` from `github.com/arandu-io/framework/foundation`, and
what it asks for is a list rather than a tree:

```go
// Before.
type Publishable interface {
	Name() string
	Publishes() fs.FS
}

// After.
type Publishable interface {
	Publishes() []foundation.Publication
}
```

`Module.Publishes` changed from `func() io/fs.FS` to
`func() []foundation.Publication`. A `Publication` carries the tag — one of
`view`, `component`, `config`, `migration`, `translation`, `asset` — the tree,
and optionally the directory to read it from and the directory it lands in. This
package declares one, tagged `foundation.PublishView`, with neither directory
set, because every path in its archive is already the path the file takes in the
project.

The package-level `Publishes` function was removed with the command that needed
it: it existed because a `package main` with no database handle could never hold
a `Module`, and there is no such command any more. Reach the declaration through
the module.

### Contracts that did not move

`PublishedPaths`, `ViewNames` and `ViewPackages` are unchanged, and so are the
paths the views land under. A project that already published them is holding the
same files at the same addresses; `aru vendor:publish` reports them as
unchanged rather than rewriting them.

## v0.2.0

Version 0.2.0 replaces the generic CRUD Repository with the configured
Model-first data path. Upgrade Framework to `v0.41.0` and Hesape to `v0.19.1`
before changing the package wiring.

### Replace Repository wiring

Construct the Service with the application database handle:

```go
// Before.
repository := NewWalletRepository(db)
service := NewWalletService(repository)

// After.
service := NewWalletService(db)
```

`WalletRepository` and `NewWalletRepository` were removed. The removed
generic CRUD methods are `(*WalletRepository).Create`,
`(*WalletRepository).Delete`, `(*WalletRepository).Find`,
`(*WalletRepository).List`, and `(*WalletRepository).Update`. Use
`Wallets(db)` after authorization for generic CRUD. Add a Repository only for
a specialized query, report, projection, read model, export, or external
storage boundary.

### Keep Model results as pointers

The Service now returns the entities owned by the configured Model:

- `(*WalletService).Create` changed from `(Wallet, error)` to
  `(*Wallet, error)`;
- `(*WalletService).Find` changed from `(Wallet, error)` to
  `(*Wallet, error)`;
- `(*WalletService).List` changed from `([]Wallet, error)` to
  `([]*Wallet, error)`;
- `NewWalletService` changed from accepting `*WalletRepository` to
  accepting `*data.DB`.

Keep those pointers intact until converting them to `Resource` or `Collection`.
Copying an entity with an embedded Model can leave its internal entity pointer
attached to the original allocation.

`Wallet`: old is comparable; new is not because it embeds
`model.Model[Wallet]`. Do not use the entity as a map key or compare it with
`==`; compare stable fields such as `ID` instead.

### Contracts that did not move

`ErrNotFound`, route names, migration identity, `DefaultPrefix`, and
`DefaultPageSize` remain unchanged. Existing URLs and applied migrations do not
need translation.
