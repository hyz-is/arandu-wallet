# Working on Arandu Wallet

This is an Arandu package: one entity with an embedded Hesape Model, one policy
that decides about it, one service that owns the database handle, and the routes
that reach them.
It is a Go module somebody `go get`s and registers by hand in their own
`bootstrap/app.go`, which is the whole difference from working in an
application. There is no service provider, no container and no discovery — if a
line of wiring is not written in the installer's repository, it does not happen.

Read `.agents/skills/` before writing code. Each skill is a procedure, and the
one you need is named by the situation you are in.

## The gates

Nothing is finished until all four exit zero.

```sh
export GOWORK=off
gofmt -l $(find . -name '*.go' -not -path '*/testdata/*' -not -name '*.kyse.go' -not -path './.views-compile-check/*')
go build ./...
go vet ./...
go test -race ./...
```

The third filter is the staging directory `tests/Unit/published_views_compile_test.go`
writes and removes. It exists because the go command skips any directory named
`vendor` at any depth, and the view compiler mirrors a project's view tree into
`storage/framework/views/vendor/<module>` -- so `go build ./...` never sees a
generated view there, and a type error in one would surface when somebody opened
the page and nowhere earlier. The test copies the tree to a path with no such
segment and compiles it there, and skips when nothing has been built.

Nothing is built there in this repository any more. The view sources are kept at
`resources/publish`, because go mod publishes no path with a segment named
`vendor` and the address a project keeps them at has one; a directory that is not
a project's view directory makes `aru view:build` write the compiled view beside
the source it read. That output is gitignored and compiled by `go build ./...`
like any other file, so running the command is still what puts a type error in
front of a compiler -- what changed is which command reports it. It is never
committed: the archive carries the sources by name, and a compiled view in it
would be published over a page the project owns.

`GOWORK=off` is not borrowed from somewhere else, and here it is not a
preference either. This checkout may sit beside a Go workspace that lists the
framework repositories and does not list this one; when it does, every command
above fails before it compiles anything:

```
pattern ./...: directory prefix . does not contain modules listed in go.work
or their selected dependencies
```

With the workspace off, the module resolves the framework version in `go.mod` —
which is what CI compiles against, and what somebody's `go get` will get.

Both filters on `gofmt` are load-bearing in the toolchain even where this
repository has nothing for them to skip: `gofmt` is the only tool in the chain
that ignores build tags, and `testdata/` is where a fixture is allowed to be
invalid on purpose.

`aru doctor` is not one of the gates, and running it here costs a minute and
answers nothing:

```
this is not an Arandu project: no go.mod, main.go and arandu.toml together.
Run it from inside a project, or create one with `aru new`
```

It exits 1. It reads applications, and this is a library.

It does not read this one after it is installed either, and that is why
`tests/Unit/audit_test.go` exists. The doctor walks the application's own tree,
skips `vendor/`, and opens the one `arandu.mod.toml` at its root; it never loads
a dependency. So `tenant-from-request`, `system-grant-without-tenant` and
`permission-not-declared` never see a line of an installed package, and whatever
this one must prove about itself it proves in its own suite or nowhere.

## What this repository holds

| | measured with |
| --- | --- |
| 15 Go files, one per role, all in one package at the root, and one test beside them | `grep -l '^package wallet' *.go` |
| 34 test files under `tests/` and one `_internal_test.go` beside the code, 237 passing tests and subtests, and 12 more when `ARANDU_TEST_POSTGRES_DSN` names a server | `find tests -name '*_test.go'` · `go test -count=1 ./... -v \| grep -cE '^( *)--- PASS'` |
| 16 routes | `grep -c 'm.register(r,' module.go` |
| 17 actions the policy answers about | `grep -cE '^\t[A-Za-z]+ security.Action = ' policy.go` |
| 5 direct dependencies, all under `arandu-io` | `go list -m -f '{{if and (not .Indirect) (not .Main)}}{{.Path}} {{.Version}}{{end}}' all` |
| 1 nested module, `rates/frankfurter`, with gates of its own | `find . -mindepth 2 -name go.mod` |

Two of those five are database connectors, imported by the test suite and by
nothing the compiler links into an application: SQLite for the suite that runs
anywhere, PostgreSQL for the one that needs transactions which really
interleave. A test-only import of a module is pruned out of an installer's build
list, so what a project that installs this package compiles is still the
framework and Hesape.

The fifth is `kyse`, and the compiler here never reads it either: the component
library is imported by the `.kyse.go` sources, which a build tag keeps out of
every build this repository runs. It is in `go.mod` because the suite compiles
the generated views, and the generated views are what an application links after
it publishes them.

The layout is by role rather than by layer, so the package reads top to bottom:

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

`Wallets(db)`, `Operations(db)`, `Entries(db)`, `Conversions(db)`, `Charges(db)`
and `Purchases(db)` configure the six tables, each with a string primary key and
the default
`tenant_id` scope. Its terminals return `*Wallet`/`[]*Wallet`; keep those
pointers intact because copying an embedded Model leaves its `Entity` pointer
aimed at the original allocation. `Resource` and `Collection` are the deliberate
response snapshot boundary.

`rates/frankfurter` is a Go module of its own, and the four gates do not reach
it: `./...` stops at a directory with its own `go.mod`, so it is built, vetted
and tested from inside its own directory. It is separate because it is the only
part of this repository that talks to a network and Go has no optional
dependency -- a client held in the package above would be a client in the build
of everybody who installed the wallet. Its manifest declares `network = true`
and the parent's goes on declaring `network = false`; `productionGoFiles` skips
nested modules for the same reason it skips a `main` package, which is that
neither is something `go get` of this module compiles.

## What does not exist here

Reaching for one of these is the most common way to write a package that is
rejected in review. None of them is missing by accident.

| A model reaches for | What is here instead |
| --- | --- |
| a service provider, a container, a `Register()` that discovers things | `New(cfg, db, sessions)`, called by hand in the installer's `bootstrap/app.go`. Everything the package touches is a parameter |
| a global `DB`, an `init()` that opens a connection | the `*data.DB` handed to `New`. A package that opened its own connection would be a package the application cannot point at a test database |
| a CRUD Repository beside the Model | `Wallets(db)`, reached only by `WalletService` after `security.Authorize` |
| a tenant read from the path, the body, the query or a header | `data.Tenant(g)`, from the Grant, which came from the session |
| a permit-all branch in the policy "for now" | nothing. The policy denies, and an action is opened by writing the rule that opens it |
| an `interface{}` config, a map of options, an env var read at call time | the typed `Config` struct, validated by `New` |
| a `panic` on bad wiring | an `error` from `New`. A wiring mistake found at boot costs one restart |
| a third dependency | an argument, first. This module is imported into other people's builds |
| a command of its own that copies files into a project | `Publishes()`, which declares a tagged tree and nothing more. `aru vendor:publish` asks the application which modules it registered and writes what each one declares, so one command serves every installed package instead of one command per package |

There is no `arandu-swap`, and there will not be one. Swap, exchange, quotation
and conversion are capabilities *inside* this package, beside transfer and
bookkeeping: `RateProvider` is the seam that quotes, `Rate` is the exact
fraction, `Rate.Convert` is the one rounding rule, and `wallet_conversions` is
the row that records what an exchange was worth. A second package holding half
of that would mean two answers to "what is this amount worth" and two places a
rounding rule could be changed, and the ledger would carry both. A rate source
that talks to a network belongs in a submodule of this repository with its own
`go.mod`, implementing `RateProvider`; it does not belong in a package of its
own, and it never re-implements the arithmetic.

## The properties

These are the reason the package is shaped the way it is. A change that breaks
one of them is not merged, whatever else it improves. `tests/Unit/policy_test.go`
checks the first four against the code.

1. **The policy denies by default**, and has no branch that allows an action.
   `TestThePolicyDeniesEveryActionByDefault` walks every action with an
   administrator subject and requires
   `security.ErrForbidden` from each.
2. **Every Service method authorizes before it reaches the Model.**
   `TestEveryServiceMethodAuthorizesBeforeTheModel` checks the source, and
   `TestTheServiceRefusesBeforeReachingTheModel` gives the Service a nil handle
   so even constructing `Wallets` in the wrong order fails.
3. **The tenant comes from `data.Tenant(g)`**, on every path, read and write.
   `TestTheTenantComesFromTheGrant` and
   `TestTheServiceWritesTenantOnlyFromTheGrant` hold both halves.
4. **Nothing reaches the Model without passing the first two.** The denial
   suite constructs the Service with a nil database, so a call to
   `Wallets(nil)` would panic. Every refusal it asserts is therefore proof
   that authorization happened before Model construction.
5. **A generic financial capability is fixed here, never in the consumer.**
   A retry that belongs around every money movement, a repair for a balance
   that stopped matching its ledger, an isolation level the guard depends on,
   a currency conversion, a fee, a refund — none of them is one application's
   problem, and every one of them is this package's. An application that works
   around a gap by writing its own is a second engine for money: it has its own
   rounding, its own idempotency and its own idea of when a balance is safe to
   spend, and the ledger here cannot explain what that engine did. The next
   consumer then inherits the divergence, because the gap is still here and the
   workaround is not. So the answer to "this package cannot do X yet" is a
   change to this package, in this repository, with a test — and if that is not
   possible today, an issue that says so, never a copy of the movement code
   living outside it.

`policy_test.go` holds the first four by calling the code. `tests/Unit/audit_test.go`
holds the same shape by *reading* it: every exported Service method must call
`Authorize` before its first `Wallets`, every tenant write in the Service
comes from `data.Tenant(g)`, and no tenant accessor reads request input.

It also compares `arandu.mod.toml` against what the code *calls* —
`os.WriteFile`, `exec.Command`, `http.Get`, a method named `Migrations` — rather
than against what it imports, because `net/http` is imported by everything with
a route and says nothing. Both directions fail: used and not declared, which is
`permission-not-declared` where the doctor runs it, and declared and not used,
which is a warning there and a failure here because `go test` has one outcome.
Adding an outbound call, a file write or a process means declaring it in the
same commit, and the suite is what says so.

The fifth is the one no test can hold, and saying so is part of it: nothing in
this repository can see the code an installer writes. It is held by review, by
the parity table below, and by whoever reads a consumer's diff. The table exists
so that a consumer can tell a gap from a decision without auditing this package
again, because "I audited it and it was missing" is how the second engine gets
written.

The sixth property is not syntax either, so it is held where the routes exist.
`TestNoRouteLandsInTheFrameworkNamespace`, in `tests/Feature/routes_test.go`,
registers the module and reads the table back: a prefix arrives through
configuration, and `/_arandu/` is refused when the application boots — in the
installer's process, after publication.

What the audit does not reach is written at the top of the file it lives in. It
reads syntax, so dynamic dispatch, reflection, and wrappers around the named
seams are invisible to it. A green run means no such thing was found written
down, not that none exists.

## Parity, and the limits that are not gaps

The table is here so that a consumer does not audit this package to find out
what it does. Read it before writing anything that moves money outside this
repository; the fifth property is what the answer has to be when a row says
*open*.

| Capability | Here | Reached through |
| --- | --- | --- |
| several wallets per holder, one per slug | yes | `Open`, and the unique index on tenant, holder and slug |
| balance as an integer of minor units | yes | `Amount`, an `int64`, with `ParseAmount` at the border |
| deposit, withdrawal | yes | `Deposit`, `Withdraw` |
| transfer between two wallets | yes | `Transfer` |
| exchange across currency or scale | yes | `Transfer` again: the wallets decide, and the operation is recorded as an exchange with its rate |
| the rate, recorded and reproducible | yes | `wallet_conversions`: both currencies, both scales, both amounts, the fraction, the moment, the remainder |
| a source that quotes rates | yes, in a module of its own | `rates/frankfurter`, with its own `go.mod` and its own manifest saying `network = true`. The parent still says `network = false` and means it: an application that never crosses a currency never imports it and compiles no HTTP client |
| overdraft, and moving past it | yes | `SetCredit`, the `credit_limit` column read by the guard, and `WalletForce` |
| record without counting, then settle | yes | `Pending` on the request, then `Confirm` |
| undo an operation | yes | `Reverse`, which appends the opposite and changes nothing already written |
| basket, gift, refund by line | yes | `Pay` with a `Cart`, `BeneficiaryWalletID`, and `Refund` |
| "has this wallet already bought that" | yes | `Bought`, `PurchasesOf` |
| one round trip per balance moved | yes on PostgreSQL and SQLite, two on MySQL | the guarded update reports the row it left through a `returning` clause, so nothing reads it back; a forty-line basket saves a hundred and twenty statements inside its transaction. MySQL has no such clause, so the row is read back inside the same transaction, where the update that matched holds an exclusive lock on it -- the guard is unchanged, and only the count of statements is |
| a fee somebody charges to be paid | yes | `FeeProvider`, and the fee is credited to a third wallet |
| a discount one payer is charged less | yes | `DiscountProvider`, recorded on the charge |
| the same request twice moves money once | yes | the idempotency key, under a unique index, answered by replay |
| the application's own facts on a movement | yes | `Meta`, on operations, entries and the wallet row itself |
| what a wallet is called and what it is for | yes | `Open` takes them, `Describe` changes them, and neither touches money |
| a retry when the engine reports a conflict | yes | classified around `commit` by SQLSTATE, and `ErrConcurrencyConflict` when the attempts run out |
| a balance repaired after it stops matching its ledger | yes | `Reconcile` reports and freezes the wallet; `Rebuild` closes the difference by appending one settled entry and touching no balance |
| an isolation level the guard can be read against | yes | read committed, handed to `BeginTx` when the transaction opens. Not a `SET` inside it: PostgreSQL takes that and MySQL refuses it, since a transaction's characteristics cannot be changed once it is in progress. Measured, the guard is exact on MySQL at read committed and at InnoDB's repeatable read alike -- an update re-reads the row it is about to write at both -- so on that engine the level is not what makes the count exact. The predicate is |
| PostgreSQL, MySQL and SQLite | all three | `New` admits them and refuses anything else with `ErrUnsupportedDialect`. The concurrency suite runs against real PostgreSQL and MySQL servers -- `ARANDU_TEST_POSTGRES_DSN` and `ARANDU_TEST_MYSQL_DSN` -- because SQLite serializes writers and would report the engine's behaviour as this package's. Identifiers are quoted by the connection's grammar rather than by a rule written here |
| statement, ledger, running balance | yes | `History`, `Statement`, `Entry.BalanceAfter` |
| told what the money did, after it did it | yes | `Listener` |
| lookup by holder and slug, and a name for the default one | yes | `FindBySlug`, `DefaultSlug`, and `GET {prefix}/holders/{holder}/{slug}`. It opens nothing: a read that created what it did not find would guess a currency and a scale |
| closing a wallet, and putting it back | yes | `Close`, `Reopen`, `WalletClose`. The row and the ledger stay; one column leaves, and it requires a zero balance |
| typed errors from a rate source | yes | five sentinels in `rate.go`, testable with `errors.Is` against this package without importing whichever provider is wired in |
| a free line in a basket | yes | a price of zero writes the purchase row and moves nothing; only a negative price is refused |
| an empty balance told apart from an insufficient one | yes | `ErrBalanceEmpty`, wrapped beside `ErrInsufficientFunds` so an existing caller reads it as it always did |
| a slug derived from a name | yes | `Slugify`; an empty `OpenRequest.Slug` is derived from the name, and a name that derives to nothing is refused |
| locales beyond `en` and `pt-BR` | no, and that is the decision | a money screen's wording has to be checked by somebody who reads it; an application writes a third locale in its own catalogue under these keys, and its translator is asked first |

These are limits this package chose, and they are part of the contract rather
than gaps. A consumer that needs more asks here; a consumer that works around
one has written the second engine.

| Limit | Value | Why it is a number and not "none" |
| --- | --- | --- |
| `MaxCartLines` | 100 | one basket is one transaction, and every line takes a row lock |
| `MaxItemQuantity` | 10000 | a line is for a quantity somebody meant |
| `MaxPurchaseScan` | 2000 | one batch question reads this many rows; a wallet with more recent purchases is answered from what the scan reached |
| `MaxPurchaseQuestions` | 100 | one batch carries this many questions |
| `MaxDecimalPlaces` | 9 | past it the `int64` of minor units stops reaching a billion whole units |
| `MaxRateDenominator` | 1000000000 | the remainder is recorded over this times ten to the source's scale, and that product has to fit an `int64` |
| `MaxPageSize` | 200 | a page nobody bounded reads the whole table on the day it is large |
| `MaxMetaBytes` | 4096 | what an application attaches is carried, never queried |
| `MaxMetaKeys` | 32 | the same decision, counted |
| `maxCommitAttempts` | 4 | a conflict sent again forever is a request that never answers |

Three things the reference does that this package deliberately does not:

- **the fee vanishes from the ledger there.** `PrepareService` adds it to the
  withdrawal and `TransferService` writes two transactions, so the money leaves
  and is credited nowhere. `FeeSchedule` here requires a `WalletID` and credits
  it, so what leaves is what arrives plus the fee, exactly.
- **the exchange there ignores scale.** It applies the rate without
  `10^(to_dp − from_dp)`, so a conversion between two scales is wrong by that
  factor. `Rate.Convert` applies it.
- **the quote is never stored there.** There is no rate column anywhere in its
  source, and the swap package that supplies real rates persists nothing at all.
  `wallet_conversions` holds the fraction and the moment, so the row can be
  recomputed long after the provider that answered it is gone.

Verified against the clone of 2026-08-29, at the lines named above. The first
two are descriptions and not accusations: a platform that keeps its fee outside
its wallets is a defensible arrangement, and this package makes the other choice
because a ledger whose rows do not sum to its balances is one this package
freezes. The third has no reading that makes it a choice.

## Writing code

Everything in the source is in English: identifiers, doc comments, internal
comments, error messages, log messages, and the names and messages of tests.
`pkg.go.dev` publishes the doc comments and its readers are users of this
package.

Every exported symbol carries a doc comment, and the comment documents the
symbol and nothing else. Why a signature is what it is belongs there when it is
a fact about the code — *"the value is held because Go does not build a type
from a string"* stays. A date, an issue number, a version in progress or the
name of another repository does not.

Tests go under `tests/`, in a capitalized category directory declaring a
lowercase external package: `tests/Unit` holds `package unit_test`,
`tests/Feature` holds `package feature_test`. Both import the package by its
module path, which is what makes them see exactly what a caller sees. A test
that genuinely needs something unexported goes beside the code as
`*_internal_test.go`, and the suffix is how it says so.
