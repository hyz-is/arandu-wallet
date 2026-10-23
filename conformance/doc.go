// Package conformance is the suite an application runs against its own wiring
// of the wallet, in one call from its tests/Conformance:
//
//	func TestWallet(t *testing.T) { conformance.Wallet(t, tests.WalletFixture) }
//
// The wallet keeps its promises inside its own repository, against the
// databases and the subjects its own suite builds. What it cannot see from
// there is the application that installed it: the engine and the pool the
// application opened, the migrations it ran, the session store it handed the
// module, and the subject its SubjectResolver answers for each account. Every
// one of those can break a promise the package's own suite holds -- an
// account resolved with the operator role it was never granted runs every
// wallet of its tenant, an Organization that is not carried as the tenant
// lets one customer read another's money, a store the module does not share
// with the guards signs nobody in. This kit is the package's promises
// checked over the application's own composition.
//
// # What it proves
//
//   - the ledger is append-only: a withdrawal, a transfer, a confirmation, a
//     void and a reversal append entries and change none already written;
//   - every wallet's balance is the sum of its settled entries, the newest
//     entry records it, and Reconcile finds nothing to freeze;
//   - one idempotency key is one operation: the same request again answers
//     the first receipt and moves nothing, concurrent copies move the money
//     once, and the key spent on another amount, wallet or kind is refused
//     with ErrOperationConflict;
//   - a hold reduces what may be spent and not the balance, and settles
//     exactly once: a captured hold is not captured or released again, a
//     released one is not captured, and concurrent captures take its money
//     once;
//   - a wallet of one Organization is unreachable with the subject of
//     another, through the Service and, when the application serves the
//     wallet's routes, through them -- the tenant is the Grant's, never the
//     request's;
//   - a Grant issued to read a wallet cannot write it: the database refuses
//     the update and the delete, and the balance does not move.
//
// # What it is handed
//
// [Fixture] is built by the application from its own composition, over one
// fresh database per subtest: the WalletService of the Module its bootstrap
// registers, the database it writes, and a function that creates an account
// where the application's SubjectResolver reads it and answers the subject
// that resolver answers. When the application registers the wallet's routes it
// also hands the server framework/test Serve booted; the kit signs accounts in
// with framework/test As, so the session is the application's and the roles
// are the resolver's, never a subject the test wrote.
//
// The kit opens no database of its own. The engine is the application's,
// which is what lets the same kit run on SQLite, PostgreSQL and MySQL.
package conformance
