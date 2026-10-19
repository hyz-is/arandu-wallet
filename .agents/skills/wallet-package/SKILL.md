---
name: wallet-package
description: Install, wire and use the Arandu Wallet package (Go, Arandu) in an application. Use when the request is to "install Arandu Wallet", "add wallet to the app", "go get github.com/hyz-is/arandu-wallet", "wire it into bootstrap/app.go", "register the module", "use the wallet routes", "everything under /wallet returns 403", "403 forbidden from wallet", "the table does not exist", "no such table", "change where it is mounted", "let admins read it", or when a project's go.mod already requires github.com/hyz-is/arandu-wallet. Covers the three lines of wiring and where each one goes, the Config fields and which one is required, the routes and their names, why the policy refuses everything until somebody opens it, and the migration step that is not optional.
license: MIT
metadata:
  audience: app
---

# Using Arandu Wallet

An Arandu package with one entity, its own table, its own routes and its own
policy. It is registered by hand — there is no service provider, no container
and no discovery, so if a line is not written in the application, it does not
happen.

## Install

```bash
go get github.com/hyz-is/arandu-wallet
```

## The three lines of wiring, and where each one goes

All three are in `bootstrap/app.go`. Nothing else in the application changes,
and `aru make:module` does not edit this file for you.

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

`New` returns an error rather than starting half-wired. It refuses a
configuration that cannot work, a nil database handle and a nil session store,
so a wiring mistake costs one restart instead of arriving as a nil dereference
on the first request that needed it.

## Then run the migrations

```bash
aru migrate
```

Not optional, and not automatic. The package owns a table, which is why its
`arandu.mod.toml` declares `migrations = true` — that declaration is how an
installer finds out before deploying rather than from the first request.

`aru migrate` is a step in the pipeline and never a call at the start of the
process: with N replicas, N migrations race each other.

**Symptom to recognise:** `no such table`, or `relation does not exist`, from a
route that authorized correctly. The migration has not run.

## Configuration

| field | required | meaning |
| --- | --- | --- |
| `Tenant` | yes | the customer a visitor with no session is read as. From the application's configuration, never from the request |
| `Prefix` | no | where the routes are mounted. Defaults to `/wallet` |
| `PageSize` | no | how many records one page answers with. Defaults to 25, refused above 200 |

`Tenant` is the one place a tenant does not come from a `Grant`, and it is
because a visitor with no session has no `Grant` yet. Everywhere else the tenant
comes from `data.Tenant(g)`. Passing a value the request named — a path segment,
a header, a subdomain read off the URL — is what `TestNoTenantIsReadOutOfTheRequest`
fails on. In an application the same mistake is `aru doctor`'s
`tenant-from-request`; in a package nothing but the package's own suite looks.

`PageSize` above 200 is an error from `New`, not a silent 200. A number somebody
wrote and did not get is worse than a number somebody wrote and was told about.

## Routes

| method | path | name |
| --- | --- | --- |
| `GET` | `/wallet` | `wallet.index` |
| `GET` | `/wallet/{id}` | `wallet.show` |
| `POST` | `/wallet` | `wallet.store` |

Build URLs from the names, never by writing the path a second time. `aru
route:list` shows them grouped by module. Under a custom `Prefix` the paths move
and the names do not.

`GET /wallet` answers `{"data": {"items": [...]}}`, and adds
`"next_cursor"` beside `data` when a further page exists. Pass it back as
`?cursor=`. A page shorter than `PageSize` is the last one and carries no
cursor.

`TenantID` is not in any response. That is deliberate: it names a customer's
identifier and the response is a declared list of fields rather than the entity.

## Who the policy lets through

`WalletPolicy` checks the tenant first, on every action, and then knows two
subjects and nobody else:

- **The holder** (`s.ID == wallet.HolderID`) views, reads the history, opens,
  deposits, withdraws, transfers, confirms, pays, reads purchases, describes,
  and holds, captures and releases on their own wallet.
- **The operator** (`s.HasRole(wallet.OperatorRole)`) does all of that across
  the tenant, and also reverses (whole or in part), voids a pending operation,
  refunds, sets credit limits, forces, closes, reconciles and sweeps expired
  holds.

A guest, a subject from another tenant and a signed-in person reaching for
somebody else's wallet are refused.

**Do not open it from the application.** There is no hook, no override and no
config flag for the policy. When your application decides something about money
that belongs to nobody who signs in, ask your own policy and pass its Grant to
`wallet.OperatorFor(g)`; do not build a subject with the role by hand. If the
rules your application needs are not these, the change belongs in the package.

A holder may deposit into their own wallet. When a wallet holds credits that are
sold rather than earned, do not register this package's routes: call the Service
from your own handlers, behind your own policy.

## Selling on credit (crediário)

A store that sells on its own credit opens a wallet per buyer, approves them
with `SetCredit` (the limit), and opens an agreement per purchase with
`OpenCredit`: the total is drawn, so the balance is the debt. Record each
installment's money with `PayInstallment` (refused with `ErrUnderpaid` below
`QuoteInstallment`, which adds a late fee once and interest by the day after
the due date), run `SweepCredit` once a day, and listen for
`wallet.credit_installment_overdue`, `wallet.credit_delinquent` and
`wallet.credit_settled`. `PayOffCredit` takes off the interest not yet due and
`CancelCredit` clears what is still owed. All of it is the operator's: reach it
through `wallet.OperatorFor(g)` after your own policy decided. The holder reads
their own with `Credit` and `Credits`. Reporting a debtor to a credit bureau is
the store's act; `MarkCreditReported` only keeps the record.

The late fee, the interest for lateness and the financing's interest are
capped at the Brazilian consumer ceilings (`MaxLateFeeBP`, `MaxMoraRateBP`,
`MaxInterestRateBP`); asking for more is a validation error.

## Holds

`Hold` sets an amount aside without moving the balance, `Capture` turns it into
the withdrawal, `Release` gives it back, and `ReleaseExpired` sweeps the ones past
`ExpiresAt`. `Wallet.Held` is what open holds set aside and `Wallet.Available()`
what may still be spent. Run `aru migrate` after upgrading to v0.11.0, or every
withdrawal fails on the missing `held` column.

`CaptureRequest.Amount` captures part of a hold and gives the rest back in the
same statement; `CaptureRequest.ToWalletID` captures into another wallet (a
transfer, same currency and scale) instead of out of the ledger -- a buyer's
hold paid to a seller.

## Pending, voids, partial reversals and statements by period

A movement recorded with `Pending` ends with `Confirm` or with `Void` (the
operator's), never both. `ReversePartially` gives back part of a deposit,
withdrawal or fee-free transfer, repeatedly, never past what it moved; with
`Force: true` (v0.16.0, answered by `WalletForce`) it does so even where the
payee no longer holds the money, instead of a forced `Transfer` beside it. Every
movement request takes an optional `AccruedAt`; `History` reads a period with
`From`/`Until` by record or, with `Basis: wallet.DateAccrued`, by accrual, and
`BalanceAt` answers the balance at an instant. `OperationByKey` finds what a
request did by its idempotency key -- ask it after a timeout before sending
again. Run `aru migrate` after upgrading to v0.15.0: the columns these need are
added by `20261019_0021_add_wallet_operation_settlement` and
`20261019_0022_add_wallet_hold_captured_amount`.

Do not keep a pending/void flag, a refunded-amount column or a competence date
of your own beside an operation: they are here, and a second copy is a second
answer to what happened to the money.

## What the answers mean

| status | what happened |
| --- | --- |
| `403` | the policy refused, or no rule allows the action yet |
| `404` | no row with that id in this tenant |
| `422` | the input was rejected; the body names the fields |

A refusal carries no detail beyond the status and a word. Telling a client why a
policy said no tells it what exists and what does not, one request at a time.
The reason is in the log.

Anything else is a 500 and the framework's error page in development. The
package returns unexpected errors rather than swallowing them, so a route that
answers 200 with an empty body is not something it does.

## Reporting a problem

Issues and pull requests go to the repository the module path names,
`github.com/hyz-is/arandu-wallet`, which belongs to `hyz-is`. A vulnerability goes to the
private advisory form named in that repository's `SECURITY.md`, and never into
an issue.

MIT licensed. Copyright Paulo R. Lima.
