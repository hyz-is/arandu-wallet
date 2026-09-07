# Changelog

Everything worth knowing about a release of Arandu Wallet is recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
the versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

A published module version is immutable: Go serves it from the proxy forever, so
a release is corrected by another release and never by moving a tag.

Every heading below is a tag of this repository. Up to `v0.4.0` this file also
carried three sections describing releases of the package template this
repository was configured from -- numbered `0.2.0`, `0.3.1` and `0.4.0`, dated
before this repository existed, and colliding head-on with the tags of the same
name here. They are gone.

## [Unreleased]

## [0.7.0] - 2026-09-06

### Fixed

- A replay reauthorizes. Every method authorized its action on an empty
  candidate, asked the idempotency key, and only then loaded the wallet and
  authorized on the row -- so a caller who knew somebody else's key was handed
  that operation, its entries and, on a basket, the lines it bought, without any
  policy ever seeing the wallet. The key is unique per tenant, not per holder,
  so this reached everybody in one tenant. The lookup now runs after the wallet
  has been authorized, at all nine call sites, and the operation it finds has to
  have written an entry on that wallet. Both sides of a transfer are owners, and
  that is a choice the code states: the receipt describes a movement the wallet's
  own ledger already shows.
- The fallback after a lost race reauthorizes too. When two callers pick one
  key, the unique index refuses the second operation row and the loser looks up
  what the winner did -- a second place the key could stand in for permission. A
  loser who was not in the winner's operation is answered with `ErrNotFound`.
- A listener waits for the outermost commit. `notify` ran where the write
  returned, which is the commit only when this package opened the transaction.
  An application that had already opened one was told that money moved, and
  could then roll back -- leaving somebody told about a thing that did not
  happen. It goes through `AfterCommit` in Hesape `v0.27.0` now: registered at
  any depth, run once the outermost transaction has committed, discarded on
  rollback, and handed a context that reports no transaction. It is not durable
  delivery, and the doc comment says so.
- `Amount.Sub` answers every difference that fits. It was `a.Add(-b)`, and
  `-MinInt64` does not fit in an int64, so it refused every subtraction of
  `MinInt64` -- including `-1 - MinInt64 = MaxInt64` and `MinInt64 - MinInt64 =
  0`, which are both representable. Overflow is detected on the result now, the
  way `Add` does it, and the three that really overflow still answer
  `ErrAmountOverflow`.

### Changed

- The minimum Hesape version is now `v0.27.0`.

## [0.6.0] - 2026-09-06

### Added

- `(*WalletService).CanWithdraw`, which answers whether a wallet could pay out
  an amount without moving it. It is the one behaviour of the reference that had
  no expression here, and its absence was worse than its presence: a consumer
  needing the question reads the balance and compares it in Go, which is the
  read-then-check this package exists to avoid. Its doc comment says it is a
  photograph, and `TestCanWithdrawIsNotPermission` empties the wallet between the
  question and the withdrawal to hold that the guard is still what decides.
- `tests/Feature/parity_test.go`: twelve tests, one per behaviour of
  `bavix/laravel-wallet` and `bavix/laravel-wallet-swap`, exercised against this
  API. The parity table was written by reading both packages; this is the same
  claim made executable, so a capability that stops working fails a build
  instead of leaving a paragraph wrong.

### Changed

- `service.go` is seven files. It reached four thousand lines, which is not a
  property of the language and was never a decision: it is where every new
  capability was appended. They are all still `package wallet` -- the division
  is by subject and not by layer, there is no directory per Service or per DTO,
  and `go doc -all` is byte for byte what it was before.
- Every column width names the bound that validates it. The comment over those
  bounds already said they were "the widths the columns are created at", and
  every migration repeated the number as a literal beside it -- the MySQL work
  added two more, one of which disagreed: a key column created at 191 next to a
  validator refusing anything over 128. `TestEveryColumnWidthIsANamedBound`
  refuses a literal width now, and a column created at `12` instead of
  `maxCurrencyLen` fails it.
- `TestThePackageUsesTheModelFirstDataPath` asks the package rather than
  `service.go`. It named a file, which is a claim about where a method is
  written and not about what the package does.

- The parity table says what the three engines change and what they do not: one
  round trip on PostgreSQL and SQLite and two on MySQL, the isolation level
  handed to `BeginTx` rather than set inside the transaction, and the measured
  fact that the guard is exact on MySQL at both levels. Three rows described
  `v0.4.1` and were left behind by `v0.5.0`.
- The three differences from the reference are marked as verified against its
  clone of 2026-08-29 at the lines named, and two of them are marked as
  descriptions rather than defects: a platform that keeps its fee outside its
  wallets is a defensible arrangement, and this package makes the other choice
  for a reason of its own.

## [0.5.0] - 2026-09-06

### Added

- MySQL. `New` accepted PostgreSQL and SQLite and refused everything else,
  including the engine `hesape` supports and the framework's own rule treats as
  an adapter rather than a mode. It is the third engine now, and the tests that
  say so run against a real server: the guard on a balance, the guard with a
  credit limit, one idempotency key under concurrent callers, and a frozen
  wallet refused at the write. `ARANDU_TEST_MYSQL_DSN` names the server, and
  they skip where it does not.
- `quoterFor` and the statement composed through the connection's grammar.
  MySQL quotes identifiers with backticks and PostgreSQL with double quotes, so
  the one place in this package that writes SQL by hand asks `hesape` how to
  spell a column rather than deciding for itself.

### Changed

- A transaction is opened at the level it names rather than told afterwards.
  It was a `SET TRANSACTION ISOLATION LEVEL` as the first statement inside the
  transaction, which PostgreSQL takes and MySQL refuses -- a transaction's
  characteristics cannot be changed once it is in progress. The level goes to
  `BeginTx` now, through `TransactionAt` in Hesape `v0.26.0`, where each driver
  spells it the way its engine takes.
- The statement that moves a balance carries a `returning` clause only where
  the engine has one. MySQL has none, so there the row is read back inside the
  same transaction, which is safe for a reason worth naming: an update that
  matched a row holds an exclusive lock on it until the transaction ends, so
  the select that follows reads what this write left. The guard is untouched --
  it is still a predicate on the update, still evaluated at the instant of the
  write, and whether it matched is read from the count the engine reports and
  never from anything fetched afterwards.
- The minimum Hesape version is now `v0.26.0`, with Framework `v0.46.1`.

### Fixed

- The migrations apply on MySQL. Two of them could not, and neither had ever
  been run against it:
  `20260905_0008_add_wallet_metadata` and `20260906_0011_add_wallet_description`
  declared `meta` as unbounded text with a default, which MySQL refuses outright
  -- "BLOB, TEXT, GEOMETRY or JSON column can't have a default value" -- and
  `20260905_0009_create_wallet_purchases` built a five-column index over columns
  of the default width, which is 4080 bytes of utf8mb4 and past InnoDB's limit
  of 3072. The metadata columns are bounded at `MaxMetaBytes`, which is already
  the largest this package writes, and the identifier columns are declared at
  the width a UUID needs.

## [0.4.1] - 2026-09-06

### Added

- Three tests that hold the two release files against the code: an action
  declared in `policy.go` and a migration declared in `module.go` have to be
  named under a version heading rather than under `[Unreleased]`, and the two
  files have to describe the same set of versions. Removing the `## [0.4.0]`
  heading names `WalletDescribe`, `WalletClose` and the two migrations of that
  release, which is the defect this release corrects.

### Fixed

- `rates/frankfurter` requires the parent from the proxy instead of replacing
  it with the directory above. The `replace` was there because a submodule
  cannot require a version that does not exist yet; a consumer ignores it --
  Go applies a replace only from the main module -- but the `require` it stood
  in for is not ignored, so this repository's own gates were testing the
  submodule against the parent on disk rather than against what was published.
- This file and `UPGRADE.md` describe the releases of this package. Both
  carried the package template's own history, with the entity renamed into it,
  so `v0.4.0` shipped a changelog whose `[0.4.0]` section described a
  publishing migration of the template and filed everything this version
  actually added under `[Unreleased]`.

## [0.4.0] - 2026-09-06

### Added

- `rates/frankfurter`, a `wallet.RateProvider` against a public source, as a Go
  module of its own inside this repository. It reads the published decimal digit
  by digit into the exact fraction it spells -- 5.4321 is 54321/10000, never a
  float -- and wraps the five failure values above. A deadline is mandatory and
  is this package's rather than the client's; caching is the application's own
  `cache.Repository` or none. The parent's manifest is unchanged at
  `network = false`: an application that never crosses a currency never imports
  it. Its suite runs against a recorded answer, with one live check skipped
  unless `ARANDU_TEST_LIVE_RATES` asks for it.
- Sentences for what the new states and kinds are called, in both shipped
  locales: `wallet.kind.adjustment`, `wallet.state.open`, `wallet.state.frozen`,
  `wallet.state.closed`, `wallet.purchase.free`, `wallet.field.description`,
  `wallet.field.state`, and the three messages a stopped or empty wallet says.
- `ErrRatePairUnknown`, `ErrRateProviderUnavailable`, `ErrRateMomentUnsupported`,
  `ErrRateCacheFailed` and `ErrRateRequestRefused`: what a rate provider could
  not do, as five values a caller tests with `errors.Is` against this package
  rather than against whichever provider it is wired to. `RateProvider` says a
  provider should wrap the one that fits; an error wrapping none of them travels
  out unchanged rather than being guessed at. The service names the pair it was
  quoting and wraps what the provider said, so the value survives the journey.
- A line priced at zero is bought. It writes its purchase row, answers `Bought`
  like any other, moves no balance and appends no ledger entry -- an entry of
  zero would be a movement saying something happened when nothing did. No
  discount and no fee is asked for on one, because a share of nothing is nothing
  and a fee with a floor would charge the payer for something the shop gave
  away. `Purchase.Free` reads it off the row. A negative price is still refused.
- `ErrBalanceEmpty`. A withdrawal refused by a wallet holding nothing, with no
  credit limit to spend against, answers with it beside `ErrInsufficientFunds`:
  the two are different things for a caller to do, and the classification is
  read off the row the refusing statement already matched nothing on. Both are
  wrapped, so a caller testing `ErrInsufficientFunds` is answered as before.
- `(*WalletService).Close`, `(*WalletService).Reopen`, `CloseRequest`,
  `WalletClose`, `Wallet.Closed`, the `closed` column, `ErrWalletClosed`,
  `ErrWalletHoldsMoney`, `WalletWasClosed`, `WalletWasReopened`, and
  `PUT`/`DELETE {prefix}/{id}/closure`. The row and the ledger stay readable,
  which is the difference between closing and deleting; closing requires a zero
  balance, guarded by the statement that closes.
- `servable`, the one gate every balance statement carries, with its two reasons
  named: frozen means the ledger stopped explaining the balance and is lifted by
  `Rebuild`; closed means somebody took the wallet out of service and is lifted
  by `Reopen`. `Resource` answers with both.
- `(*WalletService).FindBySlug`, `DefaultSlug` and
  `GET {prefix}/holders/{holder}/{slug}`. The pair of holder and slug is what
  names a wallet and has been under a unique index since the table was created;
  what was missing was the read. It opens nothing -- a read that created what it
  did not find would open a wallet under a currency and a scale this package
  would have had to guess.
- `Slugify`, and an `OpenRequest.Slug` left empty is derived from the name. The
  fold keeps letters and digits, lowers the ASCII ones and turns every other run
  into one hyphen; it translates nothing, and a name that derives to nothing is
  refused rather than opened under a slug nobody chose.
- `description` and `meta` on `wallets`, `Wallet.Description`, `Wallet.Meta`,
  and both on `Resource`. A fact that is true of every movement -- the account
  a wallet settles to, the contract it belongs to -- is a fact about the wallet,
  and attaching it to each movement instead would write it into a table that
  only grows.
- `(*WalletService).Describe`, `DescribeRequest`, `WalletDescribe` and
  `PUT {prefix}/{id}/description`. Labels only: the slug is under a unique
  index and the currency and the scale decide what every amount already written
  means, so none of the three is reachable. It is allowed on a frozen wallet,
  because a freeze is a statement about the balance and not about what the
  wallet is called.
- `OpenRequest.Description` and `OpenRequest.Meta`, carried by the `store`
  handler.
- `20260906_0011_add_wallet_description` and `20260906_0012_add_wallet_closure`.
  Running `aru migrate` is required before this version serves.

### Changed

- The statement that moves a balance reports the row it left, so nothing reads
  it back. `moveStatement` composes it -- the one place in the package that
  writes the balance column -- and every branch of it carries the same
  predicate: the wallet, the tenant, the two reasons a wallet is out of service,
  and the condition on the balance. A basket of forty lines saves a hundred and
  twenty round trips inside one transaction, with the row locks already held.
  `TestEveryBalanceStatementCarriesItsOwnGuard` now asks that function for the
  statement and reads it, and `TestOnlyOneStatementInThePackageWritesABalance`
  holds that there is no second site.

### Fixed

- `(*WalletService).PurchasesOf` pages on the pair of the sequence and the
  identifier rather than on the sequence alone. The sequence is not unique in
  that table -- two lines of one basket take theirs from the ledgers of two
  different wallets, and a free line records zero -- so a page anchored on it
  alone skipped every row sharing the last one's.

## [0.3.0] - 2026-09-06

### Added

- `ErrConcurrencyConflict`, and a movement that is sent again when the engine
  refuses it as a conflict with another transaction. Serialization failure and
  deadlock are classified by SQLSTATE, read through an interface a driver
  satisfies rather than by importing one, and retried with a widening random
  pause. Sending it again is safe because the operation and the movements arrive
  at the transaction as values -- no rate, fee, discount or product is asked
  twice -- and because an attempt that did commit is answered by its own
  idempotency key rather than repeated. A conflict that survives four attempts
  is `ErrConcurrencyConflict`, which says that nothing was written and the same
  request can be sent again.
- `(*WalletService).Rebuild`, `RebuildRequest`, `OperationAdjustment`,
  `MoneyAdjusted`, `ErrWalletNotFrozen`, `ErrLedgerBalanced` and `ErrLedgerMoved`.
  A wallet whose ledger stopped explaining its balance is closed by appending the
  settled entry the ledger was missing: the balance column is not touched, so
  the repair is a row somebody can read rather than a value somebody changed.
- The `frozen` column on `wallets`, `Wallet.Frozen`, `Reconciliation.Frozen` and
  `ErrWalletFrozen`. `(*WalletService).Reconcile` now freezes a wallet whose
  ledger and balance disagree, and every balance statement names the column in
  its own predicate, so a wallet frozen between a read and a write is refused at
  the write.
- `WalletReconcile`, the action `Reconcile` and `Rebuild` ask about. It is the
  operator's and not the holder's: what these two write is a wallet that no
  longer moves, or a ledger row no request produced.
- `ErrUnsupportedDialect`. `New` refuses an engine this package's suite has never
  run against; PostgreSQL and SQLite are what it covers, and nothing here claims
  MySQL.
- `20260906_0010_add_wallet_freeze`. Running `aru migrate` is required before
  this version serves.

### Changed

- Every transaction this package opens names its own isolation level -- read
  committed, as the first statement -- instead of taking the engine's default.
  The guard on a balance is a predicate on an update, and what that predicate is
  evaluated against while another transaction changes the same row is the level's
  answer; a default is a setting an operator can change for a whole cluster. A
  transaction the application had already opened is joined and left at the level
  it chose.
- `(*WalletService).Reconcile` asks about `WalletReconcile` rather than
  `WalletHistory`, and sums the ledger only up to the position the wallet held
  when the read began, so what it compares against the balance is exactly the set
  of rows that produced it.

## [0.2.1] - 2026-09-05

### Fixed

- The published module carries its view sources. They were kept at
  `resources/views/vendor/wallet/`, and `go mod` drops every path with a segment
  named `vendor` when it packs a module, so the files were in the repository and
  absent from the archive the proxy serves: a project that imported this package
  failed to build with `pattern resources/views: no matching files found`, and
  every gate that compiles this repository was green. The archive keeps them
  under `resources/publish/` and the publication carries where they come from
  and where they go, so the files still land at `resources/views/vendor/wallet/`
  under the same view names.

## [0.2.0] - 2026-09-05

### Added

- `Cart`, `CartItem`, `Product` and `LimitedProduct`: a basket of lines the
  application prices, paid for in one operation and one transaction. What is for
  sale is the application's, through an interface this package declares and never
  implements -- a key for the record, a wallet for the money and a price for this
  buyer, with a stock a catalogue answers separately and is asked about before a
  single balance is touched.
- `(*WalletService).Pay`, `PayRequest`, `OperationPurchase` and `WalletPay`. A
  line bought for somebody else is a gift: the money still leaves the payer and
  arrives at the seller, and the record says the beneficiary bought it.
- `(*WalletService).Refund`, `RefundRequest`, `OperationRefund` and
  `WalletRefund`. A basket is undone line by line and never whole, so a basket
  half of which was already given back cannot be given back twice; reversing a
  purchase answers `ErrPurchaseOperation`.
- `Purchase`, `PurchaseKind`, `PurchaseQuery`, `Purchases(db)`,
  `PurchaseResource` and `PurchaseCollection`, and the `wallet_purchases` table:
  the record of who bought what from whom and the receipt of one line at once.
  Every number the arithmetic used is a column, which is what lets a refund move
  back exactly what moved by reading one row. Appended to and never rewritten.
- `(*WalletService).Bought` and `WalletPurchases`: one statement answers a whole
  page of "has this already been bought", refunds excluded, bounded by
  `MaxPurchaseScan`. `(*WalletService).PurchasesOf` is the page of one wallet's
  own lines.
- `Meta`, what the application attaches to a movement, on `Operation` and on
  `Entry` and on every request. Names to text, because a JSON number read back in
  Go is a float and a float is what this package keeps away from money.
- `Leg` and `TransferRequest.Withdrawal`/`TransferRequest.Deposit`: the two sides
  of a payment carry their own metadata and their own settlement, so money held
  until delivery and delivery on credit are both expressible.
- `Config.CSRF`, the issuer of the token every form on these screens carries.
  It is required: every screen this module draws moves money, and a form with no
  token is a form the application refuses -- which is better found at boot than
  from a button that does nothing.
- `Listener`, `Event`, `EventKind` and `Config.Listeners`: whoever asked is told
  what the money did, after the write has committed and never inside it. A
  movement the database threw away is never announced.
- `Commands`, `Deps` and `CommandPrefix`: `wallet:wallets`, `wallet:statement`,
  `wallet:purchases` and `wallet:audit`. Every one of them reads, and the last
  reports what a ledger adds up to without ever repairing it.
- `(*WalletService).Reconcile` and `Reconciliation`: what a ledger sums to beside
  what the balance column says, so an application can ask from a health check
  what an operator asks from a terminal.
- A catalogue of sentences in `en` and `pt-BR`, embedded and never published,
  with `Labels`, `Lines`, `Locales`, `TranslationGroup`, `FallbackLocale` and
  `Config.Translator`.
- Three screens -- `ViewIndex`, `ViewStatement` and `ViewOperations` -- with
  `IndexPageData`, `StatementPageData`, `OperationsPageData`, `WalletRow`,
  `EntryRow`, `ConversionRow`, `ChargeRow`, `PurchaseRow` and `FormState`. Each
  route answers JSON to a client that asks for it and a page to a browser.
- `Amount.Times`, `Receipt.Purchases`, `MaxCartLines`, `MaxItemQuantity`,
  `MaxMetaBytes`, `MaxMetaKeys`, `MaxPurchaseScan`, `MaxPurchaseQuestions`,
  `ErrCartEmpty`, `ErrCartTooLarge`, `ErrItemQuantity`, `ErrProductWallet`,
  `ErrProductStock`, `ErrPaysItself`, `ErrAlreadyRefunded`, `ErrNotRefundable`,
  `ErrPurchaseOperation`, `ErrMetaTooLarge`, `ErrMetaTooManyKeys` and
  `ErrMetaUnreadable`.
- `(*Module).Service`, the use cases the module holds, for the handler an
  application writes beside the routes. Paying for a basket has no route here,
  because a basket names products and a product is the application's type.
- `GET <prefix>/{id}/purchases` and `POST <prefix>/purchases/refunds`.
- `20260905_0008_add_wallet_metadata` and
  `20260905_0009_create_wallet_purchases`. Running `aru migrate` is required
  before this version serves.
- `FeeSchedule`, what a wallet charges to be paid: an exact fraction of the
  payment with a floor, a ceiling, who pays it and the wallet it is credited to.
  `FeeSchedule.Fee` applies it under the exchange's arithmetic -- integers
  throughout, truncated toward zero -- and `Fee` carries the share that no minor
  unit could take, over the schedule's denominator, rather than dropping it.
- `FeeProvider` and `Config.Fees`, the seam that prices a payment, and
  `DiscountProvider` and `Config.Discounts`, the seam that answers what one
  payer is charged less. Both are asked once per payment and what they answer is
  written down, so a replay is charged what the first call charged.
- `Charge`, the record of what a payment cost: the money it was counted in, what
  was asked for, what was taken off, what the fee was computed from, the exact
  fraction, both bounds, who paid it, where it went, the rounding rule and what
  the share could not divide. `Charges(db)` is its configured Model, and
  `wallet_charges` its table, appended to and never rewritten, one row per
  operation under a unique index.
- `Receipt.Charge`, `Statement.Charges`, `NewChargeResource`, `ChargeResource`,
  `charge` on a receipt and `charges` beside a page of a ledger, so a movement
  smaller than the request says why beside itself.
- `ErrFeeShare`, `ErrFeeBounds`, `ErrFeeWallet`, `ErrFeeCurrencyMismatch`,
  `ErrFeeExceedsAmount` and `ErrDiscountNegative`.
- `20260905_0007_create_wallet_charges`. Running `aru migrate` is required
  before a fee or a discount can settle.
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

- `NewWalletService` takes the fee and discount seams beside the rate one:
  `NewWalletService(db *data.DB, rates RateProvider)` became
  `NewWalletService(db *data.DB, rates RateProvider, fees FeeProvider, discounts DiscountProvider)`.
  Every one of the three may be nil, and nil is the ordinary application.
- `(*WalletService).Transfer` asks the discount seam and then the fee seam, in
  that order: the fee is a share of what is being paid rather than of what was
  asked for. What leaves the payer is what arrives at the receiver plus what
  arrives at the wallet collecting the fee, exactly, whichever of the two paid
  it.
- A fee never crosses a rate. Where the two wallets are not counted the same
  way, or the wallet collecting the fee is not counted like them, the payment is
  refused with `ErrFeeCurrencyMismatch` rather than converted -- carrying a fee
  through a rate would round a number that is already the result of a rounding.
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

## [0.1.0] - 2026-09-05

### Added

- The package: `Wallet`, `Operation`, `Entry`, `WalletService` and `Module`,
  with `Open`, `Find`, `List`, `History`, `Deposit`, `Withdraw`, `Transfer`
  and `Reverse`, and the routes that reach them.
- `Money` and `Amount`: an amount is an integer of minor units and a scale, so
  nothing here is a float. What a wallet is counted in is a column.
- `RateProvider` and `Config.Rates`, the seam that converts between two wallets
  counted differently: `ConvertTo` answers the amount that arrives. The rate
  behind it is not recorded at this version -- `wallet_conversions` and the
  arithmetic this package owns arrive in `0.2.0` -- so an exchange settled here
  leaves no row saying at what rate.
- An append-only ledger. A balance is a column, and the ledger is what
  explains it; nothing rewrites a row that was written.
- The guard that moves a balance: the decision happens inside the statement
  that writes, as a predicate on the update, rather than in a read before it.
  A read-then-check loses money under a database that interleaves writers, and
  the suite runs against one to hold that.
- `WalletPolicy`, and the actions it answers about: `WalletView`, `WalletList`,
  `WalletCreate`, `WalletHistory`, `WalletDeposit`, `WalletWithdraw`,
  `WalletTransfer` and `WalletReverse`, plus the `OperatorRole` a person holds
  to act on money that is not their own. Money is split finer than read and
  write: somebody who may see a balance is not thereby somebody who may spend
  it.
- Idempotency: an operation carries a key, and a request that arrives twice is
  answered by the receipt of the first rather than moving the money again.
- `20260905_0001_create_wallets`, `20260905_0002_create_wallet_operations` and
  `20260905_0003_create_wallet_entries`.
