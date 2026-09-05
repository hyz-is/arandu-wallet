# Changelog

Everything worth knowing about a release of Arandu Wallet is recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
the versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

A published module version is immutable: Go serves it from the proxy forever, so
a release is corrected by another release and never by moving a tag.

## [Unreleased]

The versions below `0.1.0` are the history of the package template this
repository was configured from, carried over with every other file it holds;
they describe releases of the template and not of this package.

### Added

- `(*WalletService).Confirm`, `ConfirmRequest`, `OperationConfirmation` and
  `WalletConfirm`: a movement can be recorded without counting and made to count
  later. Confirming appends the settled entry beside the pending one under an
  operation that names the one it settles, so nothing already written changes
  and a statement reads as what was proposed and then what happened. There is
  one method and not the reference's pair of a safe and an unsafe one: this is
  the safe one, and a caller who wants the movement anyway asks with `Force` and
  is answered by the policy.
- `Pending` on `DepositRequest`, `WithdrawRequest` and `TransferRequest`, and a
  `pending` field on their bodies. False is what a client that never heard of it
  sends, and is what those requests have always done.
- `Entry.Settled`, whether a movement counted, and `Flag`, the type that spells
  a yes-or-no column and reads it back off any engine. `Entry.Signed` answers
  zero for a row that has not settled, so the sum of it over a whole ledger is
  the balance column exactly as it was before anything could be pending.
- `Receipt.Pending`, `Operation.Confirms`, and `confirms`, `pending` and
  `settled` on the responses.
- `ErrNotPending`, `ErrAlreadyConfirmed` and `ErrNotSettled`.
- `POST <prefix>/operations/{operation}/confirmations`.
- `20260905_0006_add_wallet_entry_settlement`. Running `aru migrate` is required
  before this version serves.
- `Wallet.CreditLimit`, how far below zero one wallet may go, as a positive
  number of minor units. It is a column and not a value an application answers
  for on each call, because the guard on a withdrawal reads it in the statement
  that moves the money: the balance is compared against the amount less this
  column, so the limit that decides is the one the row holds at that instant.
- `(*WalletService).SetCredit`, `CreditRequest` and `WalletCredit`, the one way
  a limit is set. It is refused to a holder: somebody who could raise their own
  limit could lend themselves money. Lowering one is guarded too -- a wallet
  already further below zero than the new limit answers
  `ErrCreditBelowBalance` and nothing is written.
- `WithdrawRequest.Force`, `TransferRequest.Force` and `WalletForce`, which is
  how a movement past the limit is asked for and answered. A field and a policy
  action rather than a second method beside each one: two entry points for a
  movement are two places every later rule has to be written into. Force lowers
  the guard's floor to the range of the column and never removes it, so a forced
  withdrawal still cannot wrap the balance into a positive number.
- `ErrCreditNegative` and `ErrCreditBelowBalance`.
- `PUT <prefix>/{id}/credit`, and a `force` field on the deposit, withdrawal and
  transfer bodies.
- `20260905_0005_add_wallet_credit_limit`. Running `aru migrate` is required
  before this version serves.
- A wallet answers with `credit_limit_minor` and `credit_limit`, because a
  balance that may be negative is not readable without the number that says how
  far.
- `OperationExchange`, the kind an operation is recorded under when it moved
  money between wallets that are not counted the same way. It is decided from
  the two wallets and never from the request, so a statement tells an exchange
  from a transfer without anybody having to remember which was which.
- `Conversion`, the record of a rate as it was applied: both currencies, both
  scales, both amounts, the exact fraction, the moment it was quoted, the
  rounding rule, and the part no minor unit could carry. `Conversions(db)` is
  its configured Model, and `wallet_conversions` its table, appended to and
  never rewritten, one row per operation under a unique index.
- `Rate`, an exchange rate as the exact fraction `Numerator/Denominator` with
  the pair it converts and the moment it was quoted, plus `Rate.Validate`,
  `Rate.Convert` and `Rate.String`.
- `Converted`, what a rate makes of an amount: the money that arrives and the
  remainder no minor unit could carry, with `Converted.Exact`.
- `Rounding` and `RoundDown`, the one rule this package rounds a conversion
  under. The exact value is truncated toward zero, so a conversion never credits
  more than the rate justifies; what is left is smaller than one minor unit and
  is written on the conversion as an exact fraction rather than dropped.
- `MaxRateDenominator`, the bound that lets a remainder be recorded in an
  `int64`.
- `ErrRateNotPositive`, `ErrRateDenominator`, `ErrRateNotQuoted`, `ErrRatePair`
  and `ErrConversionUnderflow`.
- `Receipt.Conversion`, the rate an operation applied, and nil where it applied
  none. A replayed receipt carries the rate the first call was quoted.
- `Statement.Operations` and `Statement.Conversions`, so a page of a ledger says
  what each movement was part of and at what rate.
- `NewConversionResource` and `ConversionResource`, the response form of a
  recorded rate.
- `20260905_0004_create_wallet_conversions`. Running `aru migrate` is required
  before an exchange can settle.

### Changed

- `Operation.ReversesID` is now `Operation.SettlesID`, and holds the operation a
  row reverses or the one it confirms. The column keeps the name it was created
  under, so nothing moves and no migration renames it.
- `(*WalletService).Reverse` refuses an operation that never moved anything,
  with `ErrNotSettled`. What is undone is the operation that moved the money,
  which for a movement that waited is its confirmation.
- `ErrInsufficientFunds` now means the balance and the credit limit together
  are not enough, and its message says so. A wallet with no limit reads exactly
  as it did.
- `(*WalletService).Withdraw` and `(*WalletService).Transfer` authorize
  `WalletForce` when, and only when, the request asked for it.
- `RateProvider` answers with a rate instead of a converted amount:
  `ConvertTo(ctx, g, Money, Currency, int) (Money, error)` became
  `Rate(ctx, g, from, to Currency) (Rate, error)`. Applying the rate, rounding
  it and recording it belong to this package now, so every exchange in an
  application is rounded the same way and leaves the same row behind whatever
  the provider is. A provider that returned the amount left no rate to record.
- `NewEntryResource` takes the kind of the operation the entry was written
  under: `NewEntryResource(Entry, int)` became
  `NewEntryResource(Entry, int, OperationKind)`.
- `NewEntryCollection` takes the statement instead of the entries and a scale:
  `NewEntryCollection([]*Entry, int, string)` became
  `NewEntryCollection(Statement, string)`.
- `NewReceiptResource` takes a scale per wallet instead of one for the whole
  receipt: `NewReceiptResource(Receipt, int)` became
  `NewReceiptResource(Receipt, map[string]int)`. An exchange writes two entries
  counted differently, and one scale for both moves the decimal point on one of
  them.
- `(*WalletService).Transfer` reads the two wallets before it looks up the
  idempotency key, because the kind a replay has to match is the kind the two
  wallets produce.
- `ErrCurrencyMismatch` now covers the same currency at a different scale, and
  its message says so.
- An entry answers with `operation_kind`, a receipt with `conversion`, and a
  page of a ledger with `conversions` beside its items.

## [0.4.0] - 2026-09-05

### Added

- `(*Module).Publishes` declares one `foundation.Publication`, tagged as a view.
  The contract belongs to the framework, so whatever writes the files reads
  every module through one interface instead of one this package defined for
  itself.

### Changed

- The minimum Framework version is now `v0.46.0`, with Hesape `v0.25.0`.
- `(*Module).Publishes` returns `[]foundation.Publication` instead of `io/fs.FS`.
- `PublishCommand` is now `aru vendor:publish --apply`.
- `(*Module).Boot` names the package whose import links the views, alongside the
  view and the command.

### Removed

- `Publishable`, the contract this package declared for itself.
  `foundation.Publishable` is the one it answers now.
- `Publishes`, the package-level function. There was a second form because a
  command with no database handle could not hold a `Module`; there is no such
  command any more.
- `publish`, the command of this module. `aru vendor:publish` reads the modules
  an application registered and writes what each one declares, which is a
  question only the application can answer.

## [0.3.1] - 2026-09-03

### Added

- `Publishable`, the optional contract a module answers to hand files to the
  application, and `Publishes()` on `Module`.
- `PublishedPaths`, `ViewNames` and `ViewPackages`, the three spellings of one
  view derived from the archive rather than written down separately.
- `PublishCommand`, the one spelling of the command that copies the views.
- `publish`, a command of this module: `go run <module>/publish@latest` writes
  the views under `resources/views/vendor/<module>/`, refuses to replace a file
  the project already has without `--force`, and prints the imports that link
  them.
- `(*Module).Boot` refuses to serve when a view this package renders was never
  published, naming the view and the command instead of answering the first
  request that reaches it with a 500.

## [0.2.0] - 2026-08-29

### Added

- `Wallets(db)` exposes the configured, tenant-scoped Model used by the
  Service after authorization.

### Changed

- The minimum Framework version is now `v0.41.0`, with Hesape `v0.19.1`.
- `NewWalletService` now accepts `*data.DB` instead of
  `*WalletRepository`.
- `(*WalletService).Create` now returns `(*Wallet, error)`.
- `(*WalletService).Find` now returns `(*Wallet, error)`.
- `(*WalletService).List` now returns `([]*Wallet, error)`.
- `Wallet`: old is comparable; new is not because it embeds
  `model.Model[Wallet]`. Compare stable fields such as `ID` instead.

### Removed

- `WalletRepository` and `NewWalletRepository`.
- `(*WalletRepository).Create`, `(*WalletRepository).Delete`,
  `(*WalletRepository).Find`, `(*WalletRepository).List`, and
  `(*WalletRepository).Update`. Add a Repository only for specialized
  queries, reports, projections, read models, exports, or external storage.
