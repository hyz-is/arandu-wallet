# Upgrade Guide

Every heading below is a tag of this repository. Up to `v0.4.0` this file also
carried two sections describing releases of the package template this repository
was configured from -- `v0.4.0` and `v0.2.0`, with the entity renamed into them,
describing a publishing migration and a Repository removal that both happened
before `v0.1.0` of this package. They are gone, and what this package actually
changed at each of its own versions is below.

## v0.9.4

No symbol is removed or changed, and no route, migration, action, policy
decision, tenant rule or amount changes. The published views do not change, so
nothing is republished.

### `Config.CSRF` is deprecated

The screens take the CSRF token `middleware.CSRFProtect` put on the request,
so the issuer passed in `Config` is not read. Remove the line:

```go
wallet.New(wallet.Config{
	Tenant: cfg.Auth.Tenant,
	CSRF:   csrf, // remove
}, db, sessions)
```

Keeping it compiles and changes nothing; `staticcheck` reports it as SA1019.

### The routes have to sit behind `CSRFProtect`

The application skeleton mounts it for every route, so a project made from it
has nothing to do. Outside it the screens draw an empty token, and nothing
checks the writes they send either: mount the module's routes behind
`CSRFProtect`.

### The layout links

The brand, sign-in, sign-out and register links are read from the routes named
`home`, `auth.login`, `auth.logout` and `auth.register`. An application that
registered those under other names sees the links empty on these screens, as
it does on its own screens built with `view.New`.

## v0.9.3

No symbol, route, migration, action, policy decision, tenant rule or amount
changes. What changes is what an application compiles against: updating to
this release selects Framework v0.55.1, Hesape v0.52.0 and Kyse v0.33.0.

Those releases can stop an application that booted before from booting, and
the wallet cannot make that change for it. Read their upgrade guides from the
versions the application required before. The ones that reach a running
deployment:

- Framework v0.55.0 makes `Configuration.Session` a `bootstrap.Session` with
  `Secure` and `Lifetime`, refuses `SESSION_TTL`, and stops the boot on a
  `SESSION_*` variable the session store does not read. An application that
  built its store from `SESSION_TTL` builds it with `fw.Session.Lifetime` and
  writes `SESSION_LIFETIME` in minutes.
- Framework v0.54.0 stops the boot on a boolean setting that does not read as
  one, such as `SESSION_SECURE_COOKIE=sometimes`.
- Hesape v0.52.0 removes the names it deprecated in v0.50.1 and v0.50.2.

The views this package publishes are unchanged, so there is nothing to
republish.

`rates/frankfurter` requires the same Framework and Hesape, and this package
at v0.9.2 or later.

## v0.9.2

Nothing to change in an application: this release touches the README and the
skills, not Go code. `aru skills:sync` now offers `wallet-package` to a project
that requires this version. An application wired from the earlier README or
skill did not compile or did not boot, because of the `CSRF` line; one that
runs already passes the issuer `Build` makes.

## v0.9.1

### Republish the views

No symbol is removed, and no route, migration, action, policy decision, tenant
rule or amount changes. What changes is the markup this package publishes.

The views it published up to `v0.9.0` do not compile with `aru` v0.57.0 or
later. Ten addresses wrote the prefix and an identifier as two interpolations,
`{{ .Prefix }}/{{ .Wallet.ID }}/deposits`, and the view compiler refuses a value
written into an address behind text it cannot read:

```
resources/views/modules/wallet/operations.kyse.go:53: this value is written into "action" before the scheme and the host of the address are fixed
```

The page data now carries each address whole, and the views write it once.
Publish them again and rebuild:

```sh
aru vendor:publish --tag=view
aru vendor:publish --tag=view --apply
aru view:build
```

The first command only previews. A view that was never edited is reported as
`update` and replaced; one you changed outside its custom markers is reported as
a `conflict` and left alone. Either publish over it with `--force`, which keeps
only what you wrote between `arandu:begin custom` and `arandu:end custom`, or
make the same change by hand, one line per address:

| in your view | write instead |
|---|---|
| `{{ .Prefix }}/{{ row.ID }}` | `{{ row.URL }}` |
| `{{ .Prefix }}/{{ row.ID }}/entries` | `{{ row.StatementURL }}` |
| `{{ .Prefix }}?holder_id={{ .Holder }}&amp;cursor={{ .Next }}` | `{{ .NextURL }}` |
| `{{ .Prefix }}/{{ .Wallet.ID }}` | `{{ .Wallet.URL }}` |
| `{{ .Prefix }}/{{ .Wallet.ID }}/entries` | `{{ .Wallet.StatementURL }}` |
| `{{ .Prefix }}/{{ .Wallet.ID }}/entries?cursor={{ .Next }}` | `{{ .NextURL }}` |
| `{{ .Prefix }}/{{ .Wallet.ID }}/deposits` | `{{ .DepositURL }}` |
| `{{ .Prefix }}/{{ .Wallet.ID }}/withdrawals` | `{{ .WithdrawalURL }}` |
| `{{ .Prefix }}/{{ .Wallet.ID }}/transfers` | `{{ .TransferURL }}` |
| `{{ .Prefix }}/{{ .Wallet.ID }}/credit` | `{{ .CreditURL }}` |
| `{{ .Prefix }}/purchases/refunds` | `{{ .RefundURL }}` |

`{{ .Prefix }}` written alone is the address of the listing and compiles as it
is. A project that builds with an `aru` older than v0.57.0 compiles the old
views and the new ones alike, so it can take this release and republish when it
moves the CLI.

The links point where they did. For the identifiers this package generates the
rendered pages are byte for byte the same; the one difference is a listing
narrowed to a holder whose name carries `&`, `#` or a space, whose next-page
link now escapes it instead of breaking.

## v0.9.0

### Each entity is a concrete type over the non-generic model

Hesape `v0.47.0` removes the generic model layer, and this release moves to it
with Framework `v0.50.2`. The six entities embed the non-generic `model.Model`,
each table is declared once beside its entity with `model.NewTable`, and the
query that starts from it is generated beside it by `aru model:build`, in
`WalletQuery.go`, `OperationQuery.go`, `EntryQuery.go`, `ConversionQuery.go`,
`ChargeQuery.go` and `PurchaseQuery.go`. No route, migration, action, policy
decision, tenant rule, amount, idempotency key or transaction changed.

**The constructors return the generated queries.** `Wallets`, `Operations`,
`Entries`, `Conversions`, `Charges` and `Purchases` take a `model.DB` -- a
`*data.DB` passes unchanged -- and return `*WalletQuery`, `*OperationQuery`,
`*EntryQuery`, `*ConversionQuery`, `*ChargeQuery` and `*PurchaseQuery` instead
of `*model.Model[T]`. A chain that started from them keeps its text, minus the
calls that no longer exist:

| before | now |
|---|---|
| `wallet.Wallets(db).NewQuery().Where(…)` | `wallet.Wallets(db).Where(…)` |
| `wallet.Wallets(db).NewInstance(nil, false)` and `.Entity` | `wallet.Wallets(db).New()`, which returns `*Wallet` |
| `Get` → `model.Collection[wallet.Wallet]` | `Get` → `wallet.WalletCollection` (`[]*Wallet`) |
| `func(q *model.Builder[wallet.Wallet])` in a grouped `Where` | `func(q *wallet.WalletQuery)` |

`First`, `Find` and the other row terminals still return `*Wallet`, and still
take the Grant. Every `WalletService` method keeps its signature: `List` still
returns `[]*Wallet`, `Bought` and `PurchasesOf` still return `[]*Purchase`.

**The entities no longer carry the model's configuration.** `Wallet`,
`Operation`, `Entry`, `Conversion`, `Charge` and `Purchase` embed `model.Model`,
so the fields and methods `model.Model[T]` promoted onto them are gone: the
configuration fields (`PrimaryKey`, `KeyType`, `Incrementing`, `Timestamps`,
`TenantColumn`, `Table` and the rest) live in the table, which this package
keeps unexported, and `Exists` and `WasRecentlyCreated` are methods,
`row.Exists()`. A copied row refuses every write with `model.ErrUnwired`, so
keep the pointers the queries return.

**Upgrade the floor.** The module requires Hesape `v0.48.0` and Framework
`v0.50.2`, and `arandu.mod.toml` declares `framework = ">= 0.50"`. An
application that pins a Hesape below `v0.47.0` cannot compile this release:
every generic model type it would need is gone from Hesape itself. The
published views are unchanged and need no republish.

<details>
<summary>Every incompatible symbol <code>apidiff</code> reports against v0.8.1</summary>

Most of these are the methods and fields `model.Model[T]` promoted onto the six
entities, which left with the generic type. The five renamed types are the
section below.

```text
Charge.ConnectionName
Charge.CreatedAtColumn
Charge.DeletedAtColumn
Charge.Entity
Charge.Exists
Charge.Grammar
Charge.Incrementing
Charge.KeyType
Charge.NamedScopes
Charge.PerPage
Charge.PrimaryKey
Charge.Processor
Charge.RelationResolvers
Charge.SoftDeletes
Charge.Table
Charge.TenantColumn
Charge.Timestamps
Charge.UpdatedAtColumn
Charge.WasRecentlyCreated
Charges
Conversion.ConnectionName
Conversion.CreatedAtColumn
Conversion.DeletedAtColumn
Conversion.Entity
Conversion.Exists
Conversion.Grammar
Conversion.Incrementing
Conversion.KeyType
Conversion.NamedScopes
Conversion.PerPage
Conversion.PrimaryKey
Conversion.Processor
Conversion.RelationResolvers
Conversion.SoftDeletes
Conversion.Table
Conversion.TenantColumn
Conversion.Timestamps
Conversion.UpdatedAtColumn
Conversion.WasRecentlyCreated
Conversions
Entries
Entry.ConnectionName
Entry.CreatedAtColumn
Entry.DeletedAtColumn
Entry.Entity
Entry.Exists
Entry.Grammar
Entry.Incrementing
Entry.KeyType
Entry.NamedScopes
Entry.PerPage
Entry.PrimaryKey
Entry.Processor
Entry.RelationResolvers
Entry.SoftDeletes
Entry.Table
Entry.TenantColumn
Entry.Timestamps
Entry.UpdatedAtColumn
Entry.WasRecentlyCreated
EntryCollection
NewEntryCollection
NewPurchaseCollection
Operation.ConnectionName
Operation.CreatedAtColumn
Operation.DeletedAtColumn
Operation.Entity
Operation.Exists
Operation.Grammar
Operation.Incrementing
Operation.KeyType
Operation.NamedScopes
Operation.PerPage
Operation.PrimaryKey
Operation.Processor
Operation.RelationResolvers
Operation.SoftDeletes
Operation.Table
Operation.TenantColumn
Operation.Timestamps
Operation.UpdatedAtColumn
Operation.WasRecentlyCreated
Operations
Purchase.ConnectionName
Purchase.CreatedAtColumn
Purchase.DeletedAtColumn
Purchase.Entity
Purchase.Exists
Purchase.Grammar
Purchase.Incrementing
Purchase.KeyType
Purchase.NamedScopes
Purchase.PerPage
Purchase.PrimaryKey
Purchase.Processor
Purchase.RelationResolvers
Purchase.SoftDeletes
Purchase.Table
Purchase.TenantColumn
Purchase.Timestamps
Purchase.UpdatedAtColumn
Purchase.WasRecentlyCreated
PurchaseCollection
PurchaseQuery
Purchases
Wallet.ConnectionName
Wallet.CreatedAtColumn
Wallet.DeletedAtColumn
Wallet.Entity
Wallet.Exists
Wallet.Grammar
Wallet.Incrementing
Wallet.KeyType
Wallet.NamedScopes
Wallet.PerPage
Wallet.PrimaryKey
Wallet.Processor
Wallet.RelationResolvers
Wallet.SoftDeletes
Wallet.Table
Wallet.TenantColumn
Wallet.Timestamps
Wallet.UpdatedAtColumn
Wallet.WasRecentlyCreated
Wallets
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).AddGlobalScope, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).All, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Append, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).AttributesToArray, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).CallNamedScope, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Create, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Destroy, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).DiscardChanges, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Except, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Find, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).FindMany, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).FindOrFail, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).FindOrNew, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).First, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).FirstOrCreate, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).FirstOrNew, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ForceCreate, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ForceDeleteQuietly, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ForceDeleted, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ForceDeleting, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ForceDestroy, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).FreshTimestamp, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetAppends, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetConnectionName, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetCreatedAtColumn, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetDeletedAtColumn, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetForeignKey, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetGlobalScopes, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetHidden, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetIncrementing, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetKeyName, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetKeyType, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetMorphClass, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetPerPage, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetPrevious, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetQualifiedCreatedAtColumn, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetQualifiedDeletedAtColumn, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetQualifiedKeyName, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetQualifiedUpdatedAtColumn, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetQueueableConnection, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetQueueableID, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetQueueableRelations, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetRawOriginal, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetRelation, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetRelations, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetRouteKey, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetRouteKeyName, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetTable, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetTouchedRelations, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetUpdatedAtColumn, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).GetVisible, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).HasAppended, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).HasGlobalScope, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).HasNamedScope, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Is
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).IsForceDeleting, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).IsIgnoringTouch, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).IsNot, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).IsRelation, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).IsSoftDeletable, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).LoadAggregate, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).LoadMorph, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).LoadMorphAggregate, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).LoadMorphAvg, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).LoadMorphCount, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).LoadMorphMax, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).LoadMorphMin, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).LoadMorphSum, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).MakeHidden
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).MakeVisible
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewBaseQueryBuilder, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewCollection, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewFromBuilder, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewInstance, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewModelQuery, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewQuery, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewQueryForRestoration, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewQueryWithoutRelationships, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewQueryWithoutScope, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewQueryWithoutScopes, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).NewTypedBuilder, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).On, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).OnWriteConnection, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Only, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).OnlyTrashed, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).OriginalIsEquivalent, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).PushQuietly, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).QualifyColumn, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).QualifyColumns, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Query, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Ref, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).RegisterGlobalScopes, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).RegisterModelEvent, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ReplicateQuietly, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ResolveRouteBinding, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ResolveRouteBindingQuery, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ResolveSoftDeletableRouteBinding, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).RestoreQuietly, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Restored, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Restoring, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetAppends, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetConnection, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetHidden, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetIncrementing, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetKeyName, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetKeyType, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetPerPage, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetRelation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetRelations, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetTable, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetTouchedRelations, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SetVisible, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SoftDeleted, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SyncChanges, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SyncOriginal
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SyncOriginalAttribute, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).SyncOriginalAttributes, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).ToPrettyJSON, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Touches, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).UnsetAttribute, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).UnsetRelation, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).UnsetRelations, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).UpdateOrCreate, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).UpdateOrFail, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).UpdateQuietly, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).UpdateTimestamps, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).UsesTimestamps, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).Where, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).WhereKey, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).With, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).WithTrashed, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).WithoutRelations, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Charge]).WithoutTimestamps, method set of *Charge
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).AddGlobalScope, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).All, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Append, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).AttributesToArray, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).CallNamedScope, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Create, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Destroy, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).DiscardChanges, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Except, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Find, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).FindMany, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).FindOrFail, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).FindOrNew, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).First, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).FirstOrCreate, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).FirstOrNew, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ForceCreate, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ForceDeleteQuietly, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ForceDeleted, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ForceDeleting, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ForceDestroy, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).FreshTimestamp, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetAppends, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetConnectionName, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetCreatedAtColumn, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetDeletedAtColumn, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetForeignKey, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetGlobalScopes, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetHidden, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetIncrementing, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetKeyName, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetKeyType, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetMorphClass, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetPerPage, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetPrevious, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetQualifiedCreatedAtColumn, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetQualifiedDeletedAtColumn, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetQualifiedKeyName, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetQualifiedUpdatedAtColumn, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetQueueableConnection, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetQueueableID, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetQueueableRelations, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetRawOriginal, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetRelation, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetRelations, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetRouteKey, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetRouteKeyName, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetTable, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetTouchedRelations, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetUpdatedAtColumn, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).GetVisible, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).HasAppended, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).HasGlobalScope, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).HasNamedScope, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Is
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).IsForceDeleting, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).IsIgnoringTouch, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).IsNot, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).IsRelation, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).IsSoftDeletable, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).LoadAggregate, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).LoadMorph, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).LoadMorphAggregate, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).LoadMorphAvg, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).LoadMorphCount, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).LoadMorphMax, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).LoadMorphMin, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).LoadMorphSum, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).MakeHidden
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).MakeVisible
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewBaseQueryBuilder, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewCollection, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewFromBuilder, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewInstance, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewModelQuery, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewQuery, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewQueryForRestoration, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewQueryWithoutRelationships, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewQueryWithoutScope, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewQueryWithoutScopes, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).NewTypedBuilder, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).On, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).OnWriteConnection, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Only, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).OnlyTrashed, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).OriginalIsEquivalent, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).PushQuietly, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).QualifyColumn, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).QualifyColumns, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Query, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Ref, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).RegisterGlobalScopes, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).RegisterModelEvent, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ReplicateQuietly, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ResolveRouteBinding, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ResolveRouteBindingQuery, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ResolveSoftDeletableRouteBinding, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).RestoreQuietly, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Restored, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Restoring, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetAppends, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetConnection, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetHidden, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetIncrementing, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetKeyName, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetKeyType, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetPerPage, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetRelation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetRelations, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetTable, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetTouchedRelations, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SetVisible, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SoftDeleted, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SyncChanges, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SyncOriginal
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SyncOriginalAttribute, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).SyncOriginalAttributes, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).ToPrettyJSON, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Touches, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).UnsetAttribute, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).UnsetRelation, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).UnsetRelations, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).UpdateOrCreate, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).UpdateOrFail, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).UpdateQuietly, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).UpdateTimestamps, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).UsesTimestamps, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).Where, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).WhereKey, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).With, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).WithTrashed, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).WithoutRelations, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Conversion]).WithoutTimestamps, method set of *Conversion
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).AddGlobalScope, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).All, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Append, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).AttributesToArray, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).CallNamedScope, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Create, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Destroy, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).DiscardChanges, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Except, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Find, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).FindMany, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).FindOrFail, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).FindOrNew, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).First, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).FirstOrCreate, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).FirstOrNew, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ForceCreate, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ForceDeleteQuietly, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ForceDeleted, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ForceDeleting, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ForceDestroy, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).FreshTimestamp, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetAppends, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetConnectionName, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetCreatedAtColumn, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetDeletedAtColumn, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetForeignKey, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetGlobalScopes, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetHidden, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetIncrementing, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetKeyName, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetKeyType, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetMorphClass, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetPerPage, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetPrevious, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetQualifiedCreatedAtColumn, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetQualifiedDeletedAtColumn, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetQualifiedKeyName, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetQualifiedUpdatedAtColumn, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetQueueableConnection, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetQueueableID, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetQueueableRelations, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetRawOriginal, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetRelation, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetRelations, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetRouteKey, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetRouteKeyName, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetTable, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetTouchedRelations, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetUpdatedAtColumn, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).GetVisible, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).HasAppended, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).HasGlobalScope, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).HasNamedScope, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Is
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).IsForceDeleting, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).IsIgnoringTouch, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).IsNot, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).IsRelation, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).IsSoftDeletable, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).LoadAggregate, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).LoadMorph, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).LoadMorphAggregate, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).LoadMorphAvg, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).LoadMorphCount, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).LoadMorphMax, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).LoadMorphMin, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).LoadMorphSum, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).MakeHidden
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).MakeVisible
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewBaseQueryBuilder, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewCollection, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewFromBuilder, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewInstance, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewModelQuery, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewQuery, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewQueryForRestoration, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewQueryWithoutRelationships, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewQueryWithoutScope, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewQueryWithoutScopes, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).NewTypedBuilder, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).On, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).OnWriteConnection, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Only, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).OnlyTrashed, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).OriginalIsEquivalent, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).PushQuietly, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).QualifyColumn, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).QualifyColumns, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Query, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Ref, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).RegisterGlobalScopes, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).RegisterModelEvent, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ReplicateQuietly, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ResolveRouteBinding, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ResolveRouteBindingQuery, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ResolveSoftDeletableRouteBinding, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).RestoreQuietly, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Restored, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Restoring, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetAppends, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetConnection, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetHidden, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetIncrementing, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetKeyName, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetKeyType, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetPerPage, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetRelation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetRelations, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetTable, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetTouchedRelations, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SetVisible, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SoftDeleted, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SyncChanges, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SyncOriginal
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SyncOriginalAttribute, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).SyncOriginalAttributes, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).ToPrettyJSON, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Touches, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).UnsetAttribute, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).UnsetRelation, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).UnsetRelations, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).UpdateOrCreate, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).UpdateOrFail, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).UpdateQuietly, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).UpdateTimestamps, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).UsesTimestamps, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).Where, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).WhereKey, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).With, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).WithTrashed, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).WithoutRelations, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Entry]).WithoutTimestamps, method set of *Entry
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).AddGlobalScope, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).All, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Append, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).AttributesToArray, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).CallNamedScope, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Create, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Destroy, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).DiscardChanges, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Except, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Find, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).FindMany, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).FindOrFail, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).FindOrNew, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).First, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).FirstOrCreate, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).FirstOrNew, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ForceCreate, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ForceDeleteQuietly, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ForceDeleted, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ForceDeleting, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ForceDestroy, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).FreshTimestamp, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetAppends, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetConnectionName, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetCreatedAtColumn, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetDeletedAtColumn, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetForeignKey, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetGlobalScopes, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetHidden, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetIncrementing, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetKeyName, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetKeyType, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetMorphClass, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetPerPage, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetPrevious, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetQualifiedCreatedAtColumn, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetQualifiedDeletedAtColumn, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetQualifiedKeyName, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetQualifiedUpdatedAtColumn, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetQueueableConnection, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetQueueableID, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetQueueableRelations, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetRawOriginal, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetRelation, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetRelations, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetRouteKey, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetRouteKeyName, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetTable, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetTouchedRelations, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetUpdatedAtColumn, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).GetVisible, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).HasAppended, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).HasGlobalScope, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).HasNamedScope, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Is
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).IsForceDeleting, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).IsIgnoringTouch, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).IsNot, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).IsRelation, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).IsSoftDeletable, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).LoadAggregate, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).LoadMorph, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).LoadMorphAggregate, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).LoadMorphAvg, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).LoadMorphCount, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).LoadMorphMax, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).LoadMorphMin, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).LoadMorphSum, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).MakeHidden
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).MakeVisible
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewBaseQueryBuilder, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewCollection, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewFromBuilder, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewInstance, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewModelQuery, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewQuery, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewQueryForRestoration, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewQueryWithoutRelationships, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewQueryWithoutScope, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewQueryWithoutScopes, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).NewTypedBuilder, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).On, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).OnWriteConnection, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Only, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).OnlyTrashed, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).OriginalIsEquivalent, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).PushQuietly, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).QualifyColumn, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).QualifyColumns, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Query, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Ref, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).RegisterGlobalScopes, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).RegisterModelEvent, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ReplicateQuietly, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ResolveRouteBinding, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ResolveRouteBindingQuery, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ResolveSoftDeletableRouteBinding, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).RestoreQuietly, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Restored, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Restoring, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetAppends, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetConnection, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetHidden, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetIncrementing, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetKeyName, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetKeyType, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetPerPage, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetRelation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetRelations, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetTable, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetTouchedRelations, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SetVisible, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SoftDeleted, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SyncChanges, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SyncOriginal
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SyncOriginalAttribute, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).SyncOriginalAttributes, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).ToPrettyJSON, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Touches, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).UnsetAttribute, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).UnsetRelation, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).UnsetRelations, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).UpdateOrCreate, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).UpdateOrFail, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).UpdateQuietly, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).UpdateTimestamps, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).UsesTimestamps, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).Where, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).WhereKey, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).With, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).WithTrashed, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).WithoutRelations, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Operation]).WithoutTimestamps, method set of *Operation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).AddGlobalScope, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).All, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Append, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).AttributesToArray, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).CallNamedScope, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Create, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Destroy, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).DiscardChanges, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Except, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Find, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).FindMany, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).FindOrFail, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).FindOrNew, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).First, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).FirstOrCreate, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).FirstOrNew, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ForceCreate, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ForceDeleteQuietly, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ForceDeleted, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ForceDeleting, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ForceDestroy, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).FreshTimestamp, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetAppends, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetConnectionName, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetCreatedAtColumn, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetDeletedAtColumn, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetForeignKey, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetGlobalScopes, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetHidden, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetIncrementing, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetKeyName, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetKeyType, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetMorphClass, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetPerPage, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetPrevious, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetQualifiedCreatedAtColumn, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetQualifiedDeletedAtColumn, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetQualifiedKeyName, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetQualifiedUpdatedAtColumn, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetQueueableConnection, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetQueueableID, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetQueueableRelations, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetRawOriginal, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetRelation, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetRelations, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetRouteKey, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetRouteKeyName, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetTable, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetTouchedRelations, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetUpdatedAtColumn, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).GetVisible, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).HasAppended, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).HasGlobalScope, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).HasNamedScope, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Is
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).IsForceDeleting, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).IsIgnoringTouch, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).IsNot, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).IsRelation, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).IsSoftDeletable, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).LoadAggregate, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).LoadMorph, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).LoadMorphAggregate, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).LoadMorphAvg, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).LoadMorphCount, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).LoadMorphMax, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).LoadMorphMin, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).LoadMorphSum, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).MakeHidden
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).MakeVisible
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewBaseQueryBuilder, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewCollection, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewFromBuilder, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewInstance, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewModelQuery, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewQuery, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewQueryForRestoration, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewQueryWithoutRelationships, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewQueryWithoutScope, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewQueryWithoutScopes, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).NewTypedBuilder, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).On, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).OnWriteConnection, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Only, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).OnlyTrashed, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).OriginalIsEquivalent, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).PushQuietly, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).QualifyColumn, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).QualifyColumns, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Query, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Ref, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).RegisterGlobalScopes, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).RegisterModelEvent, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ReplicateQuietly, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ResolveRouteBinding, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ResolveRouteBindingQuery, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ResolveSoftDeletableRouteBinding, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).RestoreQuietly, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Restored, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Restoring, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetAppends, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetConnection, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetHidden, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetIncrementing, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetKeyName, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetKeyType, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetPerPage, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetRelation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetRelations, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetTable, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetTouchedRelations, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SetVisible, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SoftDeleted, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SyncChanges, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SyncOriginal
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SyncOriginalAttribute, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).SyncOriginalAttributes, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).ToPrettyJSON, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Touches, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).UnsetAttribute, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).UnsetRelation, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).UnsetRelations, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).UpdateOrCreate, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).UpdateOrFail, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).UpdateQuietly, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).UpdateTimestamps, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).UsesTimestamps, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).Where, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).WhereKey, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).With, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).WithTrashed, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).WithoutRelations, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Purchase]).WithoutTimestamps, method set of *Purchase
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).AddGlobalScope, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).All, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Append, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).AttributesToArray, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).CallNamedScope, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Create, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Destroy, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).DiscardChanges, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Except, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Find, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).FindMany, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).FindOrFail, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).FindOrNew, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).First, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).FirstOrCreate, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).FirstOrNew, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ForceCreate, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ForceDeleteQuietly, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ForceDeleted, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ForceDeleting, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ForceDestroy, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).FreshTimestamp, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetAppends, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetConnectionName, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetCreatedAtColumn, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetDeletedAtColumn, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetForeignKey, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetGlobalScopes, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetHidden, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetIncrementing, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetKeyName, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetKeyType, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetMorphClass, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetPerPage, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetPrevious, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetQualifiedCreatedAtColumn, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetQualifiedDeletedAtColumn, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetQualifiedKeyName, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetQualifiedUpdatedAtColumn, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetQueueableConnection, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetQueueableID, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetQueueableRelations, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetRawOriginal, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetRelation, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetRelations, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetRouteKey, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetRouteKeyName, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetTable, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetTouchedRelations, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetUpdatedAtColumn, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).GetVisible, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).HasAppended, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).HasGlobalScope, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).HasNamedScope, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Is
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).IsForceDeleting, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).IsIgnoringTouch, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).IsNot, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).IsRelation, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).IsSoftDeletable, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).LoadAggregate, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).LoadMorph, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).LoadMorphAggregate, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).LoadMorphAvg, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).LoadMorphCount, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).LoadMorphMax, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).LoadMorphMin, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).LoadMorphSum, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).MakeHidden
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).MakeVisible
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewBaseQueryBuilder, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewCollection, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewFromBuilder, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewInstance, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewModelQuery, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewQuery, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewQueryForRestoration, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewQueryWithoutRelationships, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewQueryWithoutScope, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewQueryWithoutScopes, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).NewTypedBuilder, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).On, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).OnWriteConnection, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Only, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).OnlyTrashed, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).OriginalIsEquivalent, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).PushQuietly, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).QualifyColumn, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).QualifyColumns, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Query, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Ref, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).RegisterGlobalScopes, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).RegisterModelEvent, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ReplicateQuietly, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ResolveRouteBinding, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ResolveRouteBindingQuery, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ResolveSoftDeletableRouteBinding, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).RestoreQuietly, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Restored, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Restoring, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetAppends, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetConnection, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetHidden, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetIncrementing, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetKeyName, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetKeyType, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetPerPage, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetRelation
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetRelations, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetTable, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetTouchedRelations, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SetVisible, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SoftDeleted, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SyncChanges, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SyncOriginal
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SyncOriginalAttribute, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).SyncOriginalAttributes, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).ToPrettyJSON, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Touches, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).UnsetAttribute, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).UnsetRelation, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).UnsetRelations, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).UpdateOrCreate, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).UpdateOrFail, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).UpdateQuietly, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).UpdateTimestamps, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).UsesTimestamps, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).Where, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).WhereKey, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).With, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).WithTrashed, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).WithoutRelations, method set of *Wallet
github.com/arandu-io/hesape/database/model.(*Model[github.com/hyz-is/arandu-wallet.Wallet]).WithoutTimestamps, method set of *Wallet
```

</details>

### Three types are renamed, because the generated queries take their names

`aru model:build` declares `<Entity>Query` and `<Entity>Collection` beside every
entity, so `Purchase` gets `PurchaseQuery` and `PurchaseCollection`, and `Entry`
gets `EntryCollection`. The hand-written types under those names move:

| before | now |
|---|---|
| `PurchaseQuery` | `PurchaseQuestion` |
| `EntryCollection` | `EntryResourceCollection` |
| `NewEntryCollection` | `NewEntryResourceCollection` |
| `PurchaseCollection` | `PurchaseResourceCollection` |
| `NewPurchaseCollection` | `NewPurchaseResourceCollection` |

Fields, methods and the JSON they answer with are unchanged; only the names move.

```go
// Before
service.Bought(ctx, actor, []wallet.PurchaseQuery{{OwnerWalletID: id, ProductKey: "sku-1"}})

// After
service.Bought(ctx, actor, []wallet.PurchaseQuestion{{OwnerWalletID: id, ProductKey: "sku-1"}})
```

### Published views move out of `vendor/`

The views this package publishes land in `resources/views/modules/wallet/` and
compile to `storage/framework/views/modules/wallet`. It used to be `vendor/` in
both, and that address could not work: the go command refuses to import a
package whose path carries a `vendor` element —

```
bootstrap/app.go:98:2: use of vendored package not allowed
```

— and a published view is compiled into a Go package the application has to
import for its `init()` to register anything. So the last step of the install,
the import `(*Module).Boot` asks for, did not build.

The archive was already under `resources/publish`, which is what keeps the files
in the module zip: a file under a directory named `vendor` is dropped from it at
any depth. That fixed the source side and left the destination carrying the
word, and the destination is the address the application looks the views up at.

The value of every view name constant moved with it. The constant names are
unchanged, so code that renders through them keeps compiling, and each one now
answers `modules.wallet.…` where it answered `vendor.wallet.…`:

- `ViewIndex`
- `ViewStatement`
- `ViewOperations`

A string written out by hand instead of through the constant stops matching, and
what that produces is a 500 saying no view is registered under the old name.

**A project that already published the old tree** publishes again and removes
the old one by hand:

```sh
aru vendor:publish --tag=view --apply
aru view:build
rm -rf resources/views/vendor/wallet storage/framework/views/vendor/wallet
```

then deletes the old lines from `vendor-publish.lock` and changes the import in
`bootstrap/app.go` from `storage/framework/views/vendor/wallet` to
`storage/framework/views/modules/wallet`.

Framework `v0.46.4` and Hesape `v0.37.0` refuse a publication that carries the
reserved name, so this cannot come back quietly.

### The statement renders through the Kyse DataTable

No API, route, ledger or migration changes. Republish the wallet views to adopt the native Kyse DataTable on the statement screen:

    aru vendor:publish --tag=view
    aru vendor:publish --tag=view --apply
    aru view:build

## v0.8.1

No package API, route or migration changes. Update the module normally to select Framework v0.47.1, Hesape v0.41.1 and Kyse v0.29.1. Existing authorization and tenant policies are unchanged. Application-owned published views are not overwritten by this dependency update.

## v0.7.0

Three defects, and one of them is a disclosure. Take this release before serving
money.

### A replay is answered to the holder, and to nobody else

An idempotency key is a name the caller chose, in a column that is unique per
tenant rather than per holder. Until this version, a caller who knew somebody
else's key was answered with that operation, its entries and the lines of its
basket, and no policy had seen the wallet: the lookup ran before the wallet was
loaded.

Nothing in an application changes. What changes is who is answered:

- the holder replaying their own key is answered exactly as before, with the
  same operation and the same quote;
- either side of a transfer may replay it, because the receipt describes a
  movement their own ledger already shows;
- anybody else is answered `ErrNotFound`, whichever wallet they name.

An application that used one key across several wallets on purpose will now see
`ErrNotFound` on the second wallet instead of a receipt for the first. That was
never a replay -- it was a deposit that silently did not happen -- so the fix is
to give each wallet its own key.

### A listener is called after the outermost transaction commits

`notify` ran where the write returned, which is the commit only when this
package opened the transaction. An application that wrapped a deposit in its own
transaction was told the money moved, and could then roll back.

```go
// Before: the listener heard about this, and then it did not happen.
data.Transaction(ctx, db, func(ctx context.Context) error {
	if _, err := service.Deposit(ctx, actor, in); err != nil {
		return err
	}
	return errors.New("something else failed")
})
```

Now the listener is called once the outermost transaction has committed, and not
at all if it rolls back. The context it receives reports no transaction, so a
listener that writes must open its own.

**A listener that relied on running inside the transaction has to change.** One
that wrote a row expecting it to be rolled back with the movement is now writing
outside the transaction, and its write survives.

**This is not durable delivery.** A process that dies between the commit and the
listener loses the event. What is removed is the announcement of a write that
was rolled back; what is not added is a guarantee that the announcement arrives.
An application that needs one writes the event into the same transaction as the
row and reads it out afterwards -- an outbox -- and this is not that.

### `Amount.Sub` stops refusing results that fit

`-1 - MinInt64` is `MaxInt64` and `MinInt64 - MinInt64` is zero. Both were
answered `ErrAmountOverflow`. They are answered with the difference now.
`0 - MinInt64`, `MaxInt64 - (-1)` and `MinInt64 - 1` still overflow.

Code that treated the old refusal as a signal was reading a defect as a rule.

### Upgrade the floor

```sh
go get github.com/arandu-io/hesape@v0.27.0
```

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
