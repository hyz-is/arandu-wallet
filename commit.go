package wallet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/database"
	"github.com/arandu-io/hesape/database/query/grammars"
)

// converts reports that money moving between these two wallets has to go
// through a rate.
//
// A different currency, and also the same currency at a different scale. The
// second is a conversion too: the digits move, the division does not always
// come out whole, and what is left over has to be recorded for the same reason
// it does when the currency changes. Calling it a plain transfer would be
// calling a rounding a transfer.
func converts(source, target Wallet) bool {
	return source.Currency != target.Currency || source.DecimalPlaces != target.DecimalPlaces
}

// operation is what commit records before the money moves.
type operation struct {
	key     string
	kind    OperationKind
	settles string
	reason  string
	meta    Meta
	rate    *appliedRate
	charge  *appliedCharge
	// lines are the basket this operation paid for or gave back, and empty on
	// every operation that bought nothing. They are written inside the same
	// transaction as the movements, because a line whose money moved and whose
	// record is missing is a purchase nobody can find and nobody can refund.
	lines []purchaseLine
}

// purchaseLine is one line of a basket as it will be recorded, and there are
// none where an operation bought nothing.
//
// It carries every number the arithmetic used, which is everything the recorded
// row holds. It is one value travelling from the place that asked the seams to
// the place that writes the row, so there is exactly one price, one discount and
// one schedule per line and no second lookup between them.
type purchaseLine struct {
	position      int
	payer         string
	owner         string
	receiver      string
	productKey    string
	quantity      int
	currency      Currency
	decimalPlaces int
	price         Amount
	requested     Amount
	discount      Amount
	base          Amount
	schedule      FeeSchedule
	fee           Fee
	paid          Amount
	credited      Amount
	kind          PurchaseKind
	settles       string
	force         bool
	meta          Meta
	// movement is where this line's first movement sits in the operation's
	// movements, which is how the row learns the position it took in a ledger:
	// the sequence is decided by the statement that moved the balance, and only
	// that statement knows what the row said at the moment it ran.
	//
	// It is noMovement on a line that cost nothing, which produced none.
	movement int
}

// noMovement is what a line that moved no balance holds instead of a position
// in the operation's movements.
//
// A named value rather than a bare minus one, because it is compared in three
// places and a sentinel somebody has to recognise from its value is a sentinel
// somebody writes as zero by mistake.
const noMovement = -1

// appliedRate is the conversion an operation performed, and nil where it
// performed none.
//
// It carries the rate as it was quoted, the money that went in and the money
// that came out, which is everything the recorded row holds. It is one value
// travelling from the place that asked the provider to the place that writes
// the row, so there is exactly one rate in the operation and no second lookup
// between them.
type appliedRate struct {
	rate      Rate
	from      Money
	converted Converted
}

// appliedCharge is what an operation charged beyond the money it moved, and nil
// where it charged nothing.
//
// It carries every number the arithmetic used, which is everything the recorded
// row holds. It is one value travelling from the place that asked the providers
// to the place that writes the row, so there is exactly one discount and one
// schedule in the operation and no second lookup between them.
type appliedCharge struct {
	currency      Currency
	decimalPlaces int
	requested     Amount
	discount      Amount
	base          Amount
	schedule      FeeSchedule
	fee           Fee
}

// money reads an amount at the currency and scale this charge was counted in.
func (c appliedCharge) money(amount Amount) Money {
	return Money{Amount: amount, Currency: c.currency, DecimalPlaces: c.decimalPlaces}
}

// movement is one wallet's share of an operation.
type movement struct {
	wallet *Wallet
	kind   EntryKind
	amount Amount
	// pending records the movement without moving the balance. The entry it
	// writes takes its place in the wallet's history and counts for nothing
	// until a confirmation appends the settled entry beside it.
	pending bool
	// force lowers this movement's floor to the range of the column, and is
	// authorized where the request that asked for it is read. It is a field
	// rather than a second kind of movement, so there is one statement, one
	// guard and one place the floor is decided.
	force bool
	// adjust makes this movement the ledger row that closes a difference
	// between a wallet's entries and the balance column beside them.
	//
	// It moves no balance, and that is the whole of it: the column already
	// holds the number, and what was missing was an entry explaining it, so the
	// row is appended and the column is left exactly as it was. It is also the
	// one movement that writes while a wallet is frozen, because it is what
	// lifts the freeze.
	adjust bool
	// meta is what the application attached to this side of the operation.
	meta Meta
}

// withdrawalFloor is what the balance has to be at least for a withdrawal to
// happen, written as the expression its statement carries.
//
// Ordinarily it is the amount less the wallet's credit limit, which is the same
// thing as saying the balance may not end up further below zero than the limit
// allows. The column is named rather than read, so the limit that applies is
// the one the row holds at the moment of the write -- a limit fetched a moment
// earlier is a limit two concurrent withdrawals both spend.
//
// A forced movement lowers the floor to the smallest value the column can hold,
// and no further. The guard stays on the statement and stops being about the
// money, which is the only thing force is allowed to change: a statement with no
// guard at all would let the column wrap, and a balance that wrapped has changed
// sign.
//
// The amount goes into the SQL rather than into a placeholder because it stands
// beside a column on the right of a comparison. It is an int64 this package
// parsed, never text a caller wrote.
func withdrawalFloor(amount Amount, force bool, q quoter) string {
	if force {
		return strconv.FormatInt(math.MinInt64+int64(amount), 10)
	}
	return strconv.FormatInt(int64(amount), 10) + " - " + q("credit_limit")
}

// maxCommitAttempts is how many times one movement is sent again after the
// engine refused it as a conflict with another transaction.
//
// Four, and the number is a bound rather than a preference. A request retried
// without one is a request that never answers, and the caller holding a
// connection open for it is the one whose own timeout ends up deciding. Three
// further attempts clear the contention that two transactions crossing produce;
// what they do not clear is a wallet every request in the system is queueing
// behind, and that is a shape to report rather than to wait out.
const maxCommitAttempts = 4

// commitBackoff is how long to wait before the attempt after this one.
//
// It doubles, and it carries a random half. Two transactions that conflicted
// did so because they ran together, and two that retry after the same fixed
// pause run together again -- so the pause is a range rather than a number, and
// the two come back at different moments. It is measured in milliseconds
// because the losing transaction's row locks are already released: what is
// being waited out is the winner finishing, not a timeout.
func commitBackoff(attempt int) time.Duration {
	step := int64(1<<attempt) * int64(time.Millisecond)
	return time.Duration(step/2 + rand.Int64N(step/2))
}

// conflictCodes are the answers that mean the engine rolled this transaction
// back over another one, rather than anything about the request.
//
// Serialization failure and deadlock, by SQLSTATE, and not the whole of class
// 40. The class also holds an integrity constraint violation, which would
// repeat on every attempt, and a completion of unknown outcome, which is not
// something to decide by class -- so the two that mean "nothing was written,
// send it again" are named and the rest are not.
var conflictCodes = map[string]bool{
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
}

// conflicted reports that the engine refused this transaction as a conflict
// with another one.
//
// The answer is read through an interface a driver satisfies rather than by
// importing a driver: a driver imported here would be a driver in every build
// that installs this package, and the SQLSTATE is the one thing the engines
// spell the same way. A driver that reports no state answers no, which is the
// safe direction -- an error nobody classified is reported to the caller
// instead of being sent again.
//
// SQLite has no SQLSTATE and reaches none of these. Its writers are serialized
// by the database itself, so what contention produces there is a busy file that
// the driver's own handle waits on, and a transaction that reaches this package
// with an error has failed for a reason retrying will not change.
func conflicted(err error) bool {
	var stated interface{ SQLState() string }
	if !errors.As(err, &stated) {
		return false
	}
	return conflictCodes[stated.SQLState()]
}

// pause waits, and gives up where the caller has.
//
// A retry that ignored the context would go on holding a request whose client
// has gone, which is the failure the deadline exists to prevent.
func pause(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// supported reports whether this package has been run against the engine a
// handle speaks.
//
// PostgreSQL, MySQL and SQLite, and the list is what the suite covers rather
// than what the SQL would compile on. Every statement here is written through the Model
// and would run on more engines than these two. What does not travel with it is
// the reading of the guard: what an update sees of a row another transaction is
// changing is the engine's answer, nothing that compiles checks it, and an
// engine no test has interleaved two withdrawals on is an engine whose answer
// nobody here has read.
func supported(dialect data.Dialect) error {
	switch dialect {
	case data.DialectPostgres, data.DialectSQLite, data.DialectMySQL:
		return nil
	}
	return fmt.Errorf("%w: got %q", ErrUnsupportedDialect, dialect)
}

// commit records the operation and applies its movements, all or nothing, and
// sends it again where the engine refused it as a conflict.
//
// The operation row goes in first, and that ordering is the idempotency: the
// unique index on the tenant and the key is what refuses a second request under
// one name, so two identical calls arriving together cannot both reach the
// balance -- one of them fails at the index and rolls back with nothing
// written. The loser then finds the winner's operation and answers with it,
// which is why a failure here is looked up before it is reported.
//
// # Why sending it again is safe
//
// Everything one attempt writes is inside one transaction, so a failed attempt
// left nothing behind. What the next attempt is given is the same values: the
// operation and the movements arrive as data, and nothing in here asks a rate,
// a fee, a discount or a product again -- there is nowhere it could, because
// none of those seams is reachable from this call. So a second attempt cannot
// price the request differently from the first.
//
// And where an attempt did commit and the failure was in hearing so, the key is
// what settles it: the second attempt's operation row collides with the first
// one's on the unique index, and the lookup answers with what the first one
// did. That is the same path a second caller under a shared key takes, and it
// is checked before the error is classified, so an outcome nobody could observe
// is answered with the outcome rather than retried into a second movement.
//
// A conflict that survives the attempts is ErrConcurrencyConflict, which says
// exactly that: nothing was written, and the same request under the same key is
// still safe to send.
func (s *WalletService) commit(ctx context.Context, g security.Grant, op operation, movements []movement) (Receipt, error) {
	for attempt := 1; ; attempt++ {
		receipt, err := s.attempt(ctx, g, op, movements)
		if err == nil {
			// After the transaction, and never inside it. Everything above
			// either committed or left nothing behind, so what is told here is
			// what happened -- a listener told about a movement that was then
			// rolled back has told somebody about a thing that did not happen.
			s.notify(ctx, g, movedEvents(receipt.Operation, receipt.Entries, touched(movements))...)
			return receipt, nil
		}

		// A key already spent answers with what it did, whatever this attempt
		// failed on.
		//
		// The wallet named here is the one this call is moving money out of,
		// and the caller authorized it before reaching this function. Without
		// it, two callers who picked the same key would be answered by
		// whichever wrote first -- and losing that race is exactly the path
		// this branch is: the unique index refused the second operation row, so
		// the loser looks up what the winner did. A loser who is not in the
		// winner's operation is answered with ErrNotFound rather than with
		// somebody else's money.
		if replayed, found, lookupErr := s.replay(ctx, g, op.key, op.kind, settledFor(movements)); lookupErr == nil && found {
			return replayed, nil
		}
		if !conflicted(err) {
			return Receipt{}, err
		}
		if attempt >= maxCommitAttempts {
			return Receipt{}, fmt.Errorf("%w: %d attempts: %w", ErrConcurrencyConflict, attempt, err)
		}
		if waited := pause(ctx, commitBackoff(attempt)); waited != nil {
			return Receipt{}, waited
		}
	}
}

// attempt records the operation and applies its movements once.
func (s *WalletService) attempt(ctx context.Context, g security.Grant, op operation, movements []movement) (Receipt, error) {
	id, err := data.NewID()
	if err != nil {
		return Receipt{}, err
	}
	instance, err := Operations(s.db).NewInstance(nil, false)
	if err != nil {
		return Receipt{}, err
	}
	record := instance.Entity
	record.ID = id
	record.TenantID = data.Tenant(g)
	record.IdempotencyKey = op.key
	record.Kind = op.kind
	record.Reason = op.reason
	record.Meta = op.meta
	// An operation settles exactly one thing: the operation it reverses, the
	// operation it confirms, or itself. With the kind beside it in a unique
	// index, that one column carries the whole rule that an operation is undone
	// at most once and confirmed at most once, and a row that settles nothing
	// collides with nothing -- including with the reversal or the confirmation
	// that names it, which differ from it in kind.
	record.SettlesID = op.settles
	if record.SettlesID == "" {
		record.SettlesID = id
	}

	var entries []Entry
	var conversion *Conversion
	var charge *Charge
	var purchases []Purchase
	err = database.TransactionAt(ctx, s.db, sql.LevelReadCommitted, func(ctx context.Context) error {
		if _, err := record.Save(ctx, g); err != nil {
			return err
		}
		// The rate goes in with the operation and before the money moves, so
		// there is no state in which an exchange settled and what it was worth
		// is missing. It is the same transaction as the entries: a conversion
		// nobody can read is a movement nobody can explain, and the two either
		// both exist or neither does.
		if op.rate != nil {
			quoted, err := s.recordConversion(ctx, g, id, *op.rate)
			if err != nil {
				return err
			}
			conversion = quoted
		}
		// And the charge with it, for the same reason: a receipt that says a
		// different number from the request has to say why in the same
		// transaction that moved the money, or there is a state in which the
		// payment settled and what it cost is missing.
		if op.charge != nil {
			taken, err := s.recordCharge(ctx, g, id, *op.charge)
			if err != nil {
				return err
			}
			charge = taken
		}
		written, err := s.apply(ctx, g, id, movements)
		if err != nil {
			return err
		}
		entries = written
		// And the lines with them, in the same transaction and after the
		// movements: the position a line takes in a ledger is decided by the
		// statement that moved the balance, and there is no state in which
		// money moved for a basket and what it bought is missing.
		bought, err := s.recordPurchases(ctx, g, id, op.lines, written)
		if err != nil {
			return err
		}
		purchases = bought
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}

	return Receipt{
		Operation:  *record,
		Entries:    entries,
		Conversion: conversion,
		Charge:     charge,
		Purchases:  purchases,
	}, nil
}

// touched is the wallets an operation moved, by identifier.
//
// They are read off the movements rather than the database, because the
// movements already hold them: an event says what currency and scale an amount
// is in, and a read to find that out again would be a statement per wallet after
// the transaction that changed it.
func touched(movements []movement) map[string]Wallet {
	out := make(map[string]Wallet, len(movements))
	for _, m := range movements {
		if m.wallet != nil {
			out[m.wallet.ID] = *m.wallet
		}
	}
	return out
}

// recordPurchases writes the lines of a basket, inside the caller's
// transaction.
//
// Every number comes off the value the caller already holds. Nothing here asks
// a seam again, and there is nowhere it could: the price, the discount and the
// schedule arrived as data, and a second lookup would be a second answer in one
// basket -- which is a receipt that says one thing and a ledger that did
// another.
//
// The rows go in one statement, for the reason the entries do: a basket of
// forty lines that wrote forty inserts would pay forty round trips for rows
// that were all decided before the first of them was sent.
func (s *WalletService) recordPurchases(ctx context.Context, g security.Grant, operationID string, lines []purchaseLine, entries []Entry) ([]Purchase, error) {
	if len(lines) == 0 {
		return nil, nil
	}

	written := make([]Purchase, 0, len(lines))
	rows := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		id, err := data.NewID()
		if err != nil {
			return nil, err
		}
		// The sequence is read off the movement this line produced, which is
		// the position it took in a ledger. A line that cost nothing produced
		// none and records a sequence of zero -- which is why the page of a
		// wallet's purchases is anchored on the pair of the sequence and the
		// identifier, and not on the sequence alone.
		sequence := int64(0)
		if line.movement != noMovement {
			if line.movement < 0 || line.movement >= len(entries) {
				return nil, fmt.Errorf("wallet: the line at position %d names a movement that is not there", line.position)
			}
			sequence = entries[line.movement].Sequence
		}

		row := Purchase{
			ID:                   id,
			TenantID:             data.Tenant(g),
			OperationID:          operationID,
			Position:             line.position,
			PayerWalletID:        line.payer,
			OwnerWalletID:        line.owner,
			ReceiverWalletID:     line.receiver,
			ProductKey:           line.productKey,
			Quantity:             line.quantity,
			Currency:             line.currency,
			DecimalPlaces:        line.decimalPlaces,
			PricePerItem:         line.price,
			RequestedAmount:      line.requested,
			Discount:             line.discount,
			BaseAmount:           line.base,
			FeeNumerator:         line.schedule.Numerator,
			FeeDenominator:       line.schedule.Denominator,
			FeeMinimum:           line.schedule.Minimum,
			FeeMaximum:           line.schedule.Maximum,
			FeeDeductible:        Flag(line.schedule.Deductible),
			FeeAmount:            line.fee.Amount,
			FeeWalletID:          line.schedule.WalletID,
			Rounding:             RoundDown,
			RemainderNumerator:   line.fee.RemainderNumerator,
			RemainderDenominator: line.fee.RemainderDenominator,
			PaidAmount:           line.paid,
			CreditedAmount:       line.credited,
			Kind:                 line.kind,
			SettlesID:            line.settles,
			Sequence:             sequence,
			CreatedAt:            time.Now().UTC(),
		}
		// A line settles exactly one thing: the line a refund gives back, or
		// itself. With the kind beside it in a unique index, that one column
		// carries the whole rule that a line is refunded at most once.
		if row.SettlesID == "" {
			row.SettlesID = id
		}
		written = append(written, row)
		rows = append(rows, purchaseRow(row))
	}

	if _, err := Purchases(s.db).NewQuery().Insert(ctx, g, rows...); err != nil {
		return nil, err
	}
	return written, nil
}

// purchaseRow is one purchase as the insert writes it.
//
// The columns are named here and nowhere else, so the batch that writes forty
// of them and the value a receipt answers with are the same fields: a column
// added to the entity and forgotten here would be a row that stored a default
// while the receipt reported what was charged.
func purchaseRow(row Purchase) map[string]any {
	return map[string]any{
		"id":                    row.ID,
		"operation_id":          row.OperationID,
		"position":              row.Position,
		"payer_wallet_id":       row.PayerWalletID,
		"owner_wallet_id":       row.OwnerWalletID,
		"receiver_wallet_id":    row.ReceiverWalletID,
		"product_key":           row.ProductKey,
		"quantity":              row.Quantity,
		"currency":              string(row.Currency),
		"decimal_places":        row.DecimalPlaces,
		"price_per_item":        int64(row.PricePerItem),
		"requested_amount":      int64(row.RequestedAmount),
		"discount":              int64(row.Discount),
		"base_amount":           int64(row.BaseAmount),
		"fee_numerator":         row.FeeNumerator,
		"fee_denominator":       row.FeeDenominator,
		"fee_minimum":           int64(row.FeeMinimum),
		"fee_maximum":           int64(row.FeeMaximum),
		"fee_deductible":        row.FeeDeductible,
		"fee_amount":            int64(row.FeeAmount),
		"fee_wallet_id":         row.FeeWalletID,
		"rounding":              string(row.Rounding),
		"remainder_numerator":   row.RemainderNumerator,
		"remainder_denominator": row.RemainderDenominator,
		"paid_amount":           int64(row.PaidAmount),
		"credited_amount":       int64(row.CreditedAmount),
		"kind":                  string(row.Kind),
		"settles_id":            row.SettlesID,
		"sequence":              row.Sequence,
		"created_at":            row.CreatedAt,
	}
}

// recordConversion writes the rate an operation converted at, inside the
// caller's transaction.
//
// Every number comes off the value the caller already holds. Nothing here asks
// the provider again, and there is nowhere it could: the rate arrived as data,
// and a second lookup would be a second rate in one operation -- which is a
// receipt that says one thing and a ledger that did another.
func (s *WalletService) recordConversion(ctx context.Context, g security.Grant, operationID string, applied appliedRate) (*Conversion, error) {
	id, err := data.NewID()
	if err != nil {
		return nil, err
	}
	instance, err := Conversions(s.db).NewInstance(nil, false)
	if err != nil {
		return nil, err
	}

	written := instance.Entity
	written.ID = id
	written.TenantID = data.Tenant(g)
	written.OperationID = operationID
	written.FromCurrency = applied.from.Currency
	written.FromDecimalPlaces = applied.from.DecimalPlaces
	written.FromAmount = applied.from.Amount
	written.ToCurrency = applied.converted.Money.Currency
	written.ToDecimalPlaces = applied.converted.Money.DecimalPlaces
	written.ToAmount = applied.converted.Money.Amount
	written.RateNumerator = applied.rate.Numerator
	written.RateDenominator = applied.rate.Denominator
	written.QuotedAt = applied.rate.QuotedAt.UTC()
	written.Rounding = RoundDown
	written.RemainderNumerator = applied.converted.RemainderNumerator
	written.RemainderDenominator = applied.converted.RemainderDenominator
	written.CreatedAt = time.Now().UTC()
	if _, err := written.Save(ctx, g); err != nil {
		return nil, err
	}
	return written, nil
}

// recordCharge writes what an operation charged, inside the caller's
// transaction.
//
// Every number comes off the value the caller already holds. Nothing here asks
// a provider again, and there is nowhere it could: the schedule and the discount
// arrived as data, and a second lookup would be a second answer in one
// operation -- which is a receipt that says one thing and a ledger that did
// another.
func (s *WalletService) recordCharge(ctx context.Context, g security.Grant, operationID string, applied appliedCharge) (*Charge, error) {
	id, err := data.NewID()
	if err != nil {
		return nil, err
	}
	instance, err := Charges(s.db).NewInstance(nil, false)
	if err != nil {
		return nil, err
	}

	written := instance.Entity
	written.ID = id
	written.TenantID = data.Tenant(g)
	written.OperationID = operationID
	written.Currency = applied.currency
	written.DecimalPlaces = applied.decimalPlaces
	written.RequestedAmount = applied.requested
	written.Discount = applied.discount
	written.BaseAmount = applied.base
	written.FeeNumerator = applied.schedule.Numerator
	written.FeeDenominator = applied.schedule.Denominator
	written.FeeMinimum = applied.schedule.Minimum
	written.FeeMaximum = applied.schedule.Maximum
	written.FeeDeductible = Flag(applied.schedule.Deductible)
	written.FeeAmount = applied.fee.Amount
	written.FeeWalletID = applied.schedule.WalletID
	written.Rounding = RoundDown
	written.RemainderNumerator = applied.fee.RemainderNumerator
	written.RemainderDenominator = applied.fee.RemainderDenominator
	written.CreatedAt = time.Now().UTC()
	if _, err := written.Save(ctx, g); err != nil {
		return nil, err
	}
	return written, nil
}

// apply moves every balance and appends every entry, inside the caller's
// transaction.
//
// The wallets are touched in the order of their identifiers, and never in the
// order the request named them. Two transfers in opposite directions between
// the same pair would otherwise take their two row locks in opposite orders,
// which is a deadlock -- one that only appears under load, on the engines that
// take row locks, and is reported as a driver error nobody can place.
//
// The balance is moved by a statement carrying its own guard, and the guard is
// the whole of the concurrency safety here. Nothing reads a balance, decides,
// and writes it back: the update says what has to be true of the row it is
// updating, and the database answers how many rows that was. Zero means the
// condition was false at the moment of the write, which is the only moment that
// counts.
//
// The rows are appended in one statement rather than one each. A balance has to
// move a wallet at a time, because each carries its own guard and each answers
// separately; the ledger does not, and a basket of twenty lines that wrote
// twenty inserts would pay twenty round trips for rows that were all decided
// before the first of them was sent.
func (s *WalletService) apply(ctx context.Context, g security.Grant, operationID string, movements []movement) ([]Entry, error) {
	order := make([]int, len(movements))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return movements[order[a]].wallet.ID < movements[order[b]].wallet.ID
	})

	entries := make([]Entry, len(movements))
	rows := make([]map[string]any, 0, len(movements))
	for _, index := range order {
		entry, err := s.move(ctx, g, operationID, index, movements[index])
		if err != nil {
			return nil, err
		}
		entries[index] = entry
		rows = append(rows, entryRow(entry))
	}
	if len(rows) == 0 {
		return entries, nil
	}
	if _, err := Entries(s.db).NewQuery().Insert(ctx, g, rows...); err != nil {
		return nil, err
	}
	return entries, nil
}

// entryRow is one ledger row as the insert writes it.
//
// The columns are named here and nowhere else, so the batch that writes twenty
// of them and the value a receipt answers with are the same fields: a column
// added to the entity and forgotten here would be a row that stored a default
// while the receipt reported what the caller asked for.
func entryRow(entry Entry) map[string]any {
	return map[string]any{
		"id":            entry.ID,
		"operation_id":  entry.OperationID,
		"wallet_id":     entry.WalletID,
		"kind":          string(entry.Kind),
		"sequence":      entry.Sequence,
		"position":      entry.Position,
		"amount":        int64(entry.Amount),
		"balance_after": int64(entry.BalanceAfter),
		"settled":       entry.Settled,
		"meta":          entry.Meta,
		"created_at":    entry.CreatedAt,
	}
}

// moveStatement is the one statement in this package that writes a balance, and
// the only place the guard on one is composed.
//
// It is written out rather than built through the Model, and the reason is the
// clause at the end of it. The row a movement produces has to record the
// balance the wallet was left at and the position the entry takes, and both are
// decided by this write -- so either the statement reports them or something
// reads the row again afterwards. Reading again is a second round trip per
// movement with the row lock already held, which on a basket of forty lines is
// a hundred and twenty extra of them inside one transaction, on the wallets two
// other payments are queueing for. The returning clause makes it one statement.
//
// # The guard is not optional, and cannot be
//
// Every branch below composes the same predicate: the wallet, the tenant, the
// two reasons a wallet may be out of service, and -- where money moves -- the
// condition on the balance itself. There is no path through this function that
// emits an update without them, and TestEveryBalanceStatementCarriesItsOwnGuard
// asks this function for each of them and reads what comes back.
//
// That is what the reference gets wrong at exactly this point. It batches every
// wallet of a basket into one update with a CASE over the identifiers and no
// per-row condition at all, which is a balance nothing stopped from going
// negative; the round trips it saves are the ones saved here by the clause at
// the end instead.
//
// The amounts go into the SQL rather than into placeholders, because they stand
// beside a column on the right of an assignment or a comparison. They are
// int64s this package parsed, never text a caller wrote.
//
// The tenant is a placeholder and comes from the Grant, like every other tenant
// here. It is in the predicate rather than left to the Model's scope because
// this statement does not go through the Model: a row of another customer has
// to be unreachable by this write for the same reason it is unreachable by
// every other one.
func moveStatement(m movement, tenant string, dialect data.Dialect) (string, []any) {
	q := quoterFor(dialect)
	var set, guard string
	switch {
	case m.adjust:
		// The one statement that writes while a wallet is frozen, because it is
		// what lifts the freeze. It moves no balance: the column already holds
		// the number, and what was missing is the row explaining it.
		//
		// What it guards on is that nothing has moved since the difference was
		// measured. The balance and the position are both named, so a movement
		// that got in between leaves this matching no row, and the whole
		// transaction rolls back rather than writing an adjustment computed
		// from numbers that have changed.
		set = q("last_sequence") + " = " + q("last_sequence") + " + 1, " + q("frozen") + " = 0"
		guard = fmt.Sprintf("%s = 1 and %s = 0 and %s = %d and %s = %d",
			q("frozen"), q("closed"), q("balance"), int64(m.wallet.Balance),
			q("last_sequence"), m.wallet.LastSequence)
	case m.pending:
		// A movement that has not settled moves no balance, so its statement
		// touches none. It still takes a position in the ledger, because the
		// row it writes is part of that wallet's history and a history is read
		// in order -- and the position has to be one nothing else can be
		// holding, which is why it comes from the statement rather than from a
		// number read here.
		set = q("last_sequence") + " = " + q("last_sequence") + " + 1"
		guard = servableGuard(q)
	case m.kind == EntryWithdraw:
		// The balance has to still be enough at the moment of the write, and
		// what "enough" is comes off the same row in the same statement: the
		// amount less the wallet's own credit limit, named rather than read,
		// because a limit fetched a moment earlier is a limit two concurrent
		// withdrawals both spend.
		set = fmt.Sprintf("%s = %s - %d, %s = %s + 1",
			q("balance"), q("balance"), int64(m.amount), q("last_sequence"), q("last_sequence"))
		guard = servableGuard(q) + " and " + q("balance") + " >= " + withdrawalFloor(m.amount, m.force, q)
	case m.kind == EntryDeposit:
		// And it has to still have room, or the column wraps into a negative
		// balance that no rule in this package would ever have allowed.
		set = fmt.Sprintf("%s = %s + %d, %s = %s + 1",
			q("balance"), q("balance"), int64(m.amount), q("last_sequence"), q("last_sequence"))
		guard = servableGuard(q) + fmt.Sprintf(" and %s <= %d", q("balance"), int64(m.amount.Ceiling()))
	default:
		return "", nil
	}

	statement := "update " + q(walletsTable) + " set " + set +
		" where " + q("id") + " = ? and " + q("tenant_id") + " = ? and " + guard
	if reportsTheRowItWrote(dialect) {
		statement += " returning " + movedColumns(q)
	}
	return statement, []any{m.wallet.ID, tenant}
}

// servableGuard is the gate on money as a statement spells it, and it has
// exactly two reasons.
//
// Every statement that moves a balance carries it, so a wallet that stopped
// being servable between the read that loaded it and the write that would move
// it is refused at the write. That is the same arrangement the credit limit
// has, for the same reason: an answer read a moment earlier is an answer two
// concurrent movements both saw.
//
// # The two reasons, and why they are two columns
//
// Frozen means this package no longer knows what the wallet holds: its ledger
// stopped adding up to its balance. Closed means somebody took the wallet out
// of service. They are kept apart because they are lifted by different things
// and answered to different people -- a freeze is lifted by Rebuild, which
// appends the row that explains the balance, and a closure is lifted by Reopen,
// which decides. A single "unavailable" column would make those two one, and
// the first person to look at a stopped wallet would not be able to tell
// whether it needs an investigation or a decision.
//
// # A third reason does not belong here
//
// This is the shape that invites one: a gate with two entries reads like a list
// somebody may add to. It is not. Everything this package refuses about money
// is refused for a reason it can name in the row -- not enough balance, past
// the credit limit, past the range of the column -- and each of those is a
// predicate about the movement rather than a state of the wallet. A third
// column here would mean a third state a wallet can be stuck in, with a third
// way out that somebody has to write, and a stopped wallet would need three
// questions asked before anybody could say what is wrong with it. What looks
// like a third reason is nearly always the application's own: an application
// that wants to stop a wallet for a reason of its own writes that rule in its
// own policy, where it can also say who may lift it.
func servableGuard(q quoter) string {
	return q("frozen") + " = 0 and " + q("closed") + " = 0"
}

// quoter spells an identifier the way one engine takes it.
//
// It is the grammar of the connection, not a rule written here: PostgreSQL and
// SQLite take double quotes, MySQL takes backticks unless an operator turned on
// ANSI_QUOTES, and choosing between them in this package would be a second
// place that decides how a column is named. quoterFor asks hesape.
type quoter func(column string) string

// quoterFor is the identifier grammar of a dialect.
//
// The default is the one the rest of this package is written against, and it is
// reached only by a dialect New already refused, so it is a spelling rather than
// a decision.
func quoterFor(dialect data.Dialect) quoter {
	var wrap func(any) string
	switch dialect {
	case data.DialectMySQL:
		wrap = grammars.NewMySQLGrammar().Wrap
	case data.DialectPostgres:
		wrap = grammars.NewPostgresGrammar().Wrap
	default:
		wrap = grammars.NewSQLiteGrammar().Wrap
	}
	return func(column string) string { return wrap(column) }
}

// reportsTheRowItWrote reports whether an engine can answer an update with the
// row it left.
//
// PostgreSQL and SQLite have a returning clause; MySQL has none, and no version
// of it is going to grow one. Where it is absent the caller reads the row back
// inside the same transaction, which is safe for a reason that is specific and
// worth stating: an update that matched a row holds an exclusive lock on it
// until the transaction ends, so the select that follows cannot see anybody
// else's change. What it costs is the round trip the clause exists to save.
func reportsTheRowItWrote(dialect data.Dialect) bool {
	return dialect != data.DialectMySQL
}

// moved is what a balance statement answers with: the row as it left it.
//
// It is the whole reason the statement carries a returning clause. Every field
// here was decided by that write, at that instant, with the row lock already
// held -- so what an entry records is what happened rather than what a second
// read found afterwards.
type moved struct {
	balance     Amount
	creditLimit Amount
	sequence    int64
	frozen      Flag
	closed      Flag
}

// move applies one movement: the guarded balance update, which reports what it
// left behind.
//
// It does not write the ledger row. What comes back is the entry the batch in
// apply appends, so that the statement which moves a balance stays one per
// wallet and the rows it produced go in one per operation.
func (s *WalletService) move(ctx context.Context, g security.Grant, operationID string, position int, m movement) (Entry, error) {
	statement, bindings := moveStatement(m, data.Tenant(g), s.db.Dialect())
	if statement == "" {
		return Entry{}, fmt.Errorf("wallet: %q is not a direction money moves in", m.kind)
	}

	after, matched, err := s.moving(ctx, statement, bindings)
	if err != nil {
		return Entry{}, err
	}
	if !matched {
		return Entry{}, s.whyNothingMoved(ctx, g, m)
	}

	id, err := data.NewID()
	if err != nil {
		return Entry{}, err
	}
	instance, err := Entries(s.db).NewInstance(nil, false)
	if err != nil {
		return Entry{}, err
	}
	written := instance.Entity
	written.ID = id
	written.TenantID = data.Tenant(g)
	written.OperationID = operationID
	written.WalletID = m.wallet.ID
	written.Kind = m.kind
	written.Sequence = after.sequence
	written.Position = position
	written.Amount = m.amount
	written.BalanceAfter = after.balance
	written.Settled = Flag(!m.pending)
	written.Meta = m.meta
	written.CreatedAt = time.Now().UTC()
	return *written, nil
}

// moving runs one balance statement and reads the row it returned.
//
// It reports whether the statement matched anything, which is the only thing
// the caller decides on: a guard that was false at the instant of the write
// matches nothing, and that is an answer rather than a failure.
func (s *WalletService) moving(ctx context.Context, statement string, bindings []any) (moved, bool, error) {
	if !reportsTheRowItWrote(s.db.Dialect()) {
		return s.movingInTwo(ctx, statement, bindings)
	}

	rows, err := s.db.QueryContext(ctx, statement, bindings...)
	if err != nil {
		return moved{}, false, err
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return moved{}, false, rows.Err()
	}
	var after moved
	if err := rows.Scan(&after.balance, &after.creditLimit, &after.sequence, &after.frozen, &after.closed); err != nil {
		return moved{}, false, err
	}
	return after, true, rows.Err()
}

// movingInTwo is moving where the engine cannot answer an update with the row.
//
// MySQL has no returning clause, so the same question takes two statements: the
// update decides, and a select reads what it left.
//
// # Why the second statement reads what the first wrote, and not something else
//
// The two run inside one transaction, and an update that matched a row holds an
// exclusive lock on it until that transaction ends. Nobody else can change the
// row between them, and nobody else can read the half-written state either. The
// select is therefore reading the row as this update left it, which is the same
// guarantee the returning clause gives in one statement -- at the cost of the
// round trip it exists to save.
//
// The guard is untouched by any of this. It is still a predicate on the update,
// still evaluated at the instant of the write, and a false one still matches no
// row -- which is read here from the count the engine reports rather than from
// anything fetched afterwards. There is no read-then-check on this path: the
// only thing read afterwards is what the write already decided.
//
// A statement that matched nothing is not followed by a select, because there is
// no lock and nothing to say. The caller turns that into a sentence by asking
// the wallet, exactly as it does on the engines with the clause.
func (s *WalletService) movingInTwo(ctx context.Context, statement string, bindings []any) (moved, bool, error) {
	result, err := s.db.ExecContext(ctx, statement, bindings...)
	if err != nil {
		return moved{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return moved{}, false, fmt.Errorf("wallet: reading how many rows the balance statement matched: %w", err)
	}
	if affected == 0 {
		return moved{}, false, nil
	}

	q := quoterFor(s.db.Dialect())
	// The identifier and the tenant, in the order moveStatement binds them.
	var after moved
	err = s.db.QueryRowContext(ctx,
		"select "+movedColumns(q)+" from "+q(walletsTable)+
			" where "+q("id")+" = ? and "+q("tenant_id")+" = ?",
		bindings...,
	).Scan(&after.balance, &after.creditLimit, &after.sequence, &after.frozen, &after.closed)
	if err != nil {
		return moved{}, false, err
	}
	return after, true, nil
}

// movedColumns is what a balance statement answers with, in the order moved is
// scanned in.
//
// One spelling for the two paths: the returning clause names them, and so does
// the select that stands in for it where there is no clause. Two lists would be
// two orders to keep in step, and the failure of that is a balance read into the
// credit limit.
func movedColumns(q quoter) string {
	return q("balance") + ", " + q("credit_limit") + ", " +
		q("last_sequence") + ", " + q("frozen") + ", " + q("closed")
}

// whyNothingMoved says which condition of a balance statement was false.
//
// After the statement and never instead of it. The write is what decided; this
// only turns "no row" into a sentence, and it reads the row through the Model
// so the answer is scoped and authorized exactly as every other read is.
func (s *WalletService) whyNothingMoved(ctx context.Context, g security.Grant, m movement) error {
	after, err := Wallets(s.db).NewQuery().WhereKey(m.wallet.ID).First(ctx, g)
	if err != nil {
		return err
	}
	if after == nil {
		return ErrNotFound
	}
	switch {
	case m.adjust:
		// The row is still there and it did not match, so either the wallet
		// moved since the difference was measured or somebody lifted the
		// freeze. Either way the number this adjustment carries is about a
		// state the wallet has left.
		return ErrLedgerMoved
	case bool(after.Frozen):
		return ErrWalletFrozen
	case bool(after.Closed):
		return ErrWalletClosed
	case m.pending:
		// Nothing about the money can have refused this one, so the row is
		// gone: another statement in this transaction would have read it.
		return ErrNotFound
	case m.kind == EntryWithdraw:
		// Telling "there was nothing" from "there was not enough" costs no
		// statement here either. Both are returned, so a caller that only asks
		// whether the money was there is answered exactly as before.
		if after.Balance == 0 && after.CreditLimit == 0 {
			return fmt.Errorf("%w: %w", ErrInsufficientFunds, ErrBalanceEmpty)
		}
		return ErrInsufficientFunds
	}
	return ErrAmountOverflow
}

// replay answers with what this idempotency key already did, if anything.
//
// A key that names an operation of another kind is a conflict rather than a
// replay: one key cannot be the name of two different requests, and answering
// the deposit's receipt to a withdrawal would be answering a question nobody
// asked.
//
// A replayed exchange answers with the rate the first call was quoted, read
// back off the row rather than asked for again. That is what makes idempotency
// mean the same thing for a conversion as for anything else: the same key twice
// converts once, at one rate, and the second answer is the first answer.
// settledFor is the wallet a settlement of an existing operation belongs to.
//
// The first movement, because mirror and settle build them from the entries of
// the operation being undone or confirmed, in the order that operation wrote
// them -- so the first is the wallet that operation started at. Every wallet in
// the list has already been authorized by the time this is asked; what it names
// is which one the replay is about.
func settledFor(movements []movement) string {
	for _, m := range movements {
		if m.wallet != nil {
			return m.wallet.ID
		}
	}
	return ""
}

// movedFor reports whether an operation wrote an entry on the given wallet.
//
// It is the whole of the ownership check on a replay, and it is deliberately
// this and not more. An idempotency key is a name the caller chose, stored in a
// column that is unique per tenant; two people in one tenant can pick the same
// string, and one of them can guess the other's. So a lookup by key alone
// answers with whoever wrote first, and the caller who lost the race is handed
// somebody else's operation, entries and purchases.
//
// The wallet compared against is the one the calling method authorized before
// asking -- the wallet in the request, or the one the operation being settled
// belongs to. That ordering is what makes this enough: the policy has already
// said this subject may act on that wallet, and this says the key names an
// operation that touched it.
//
// # Both sides of a transfer are owners here
//
// A transfer writes an entry on the payer and one on the payee, so either of
// them replaying under that key is answered. That is a choice rather than an
// oversight: the receipt describes a movement that this wallet's own ledger
// already shows, and refusing it would mean the payee cannot ask what a payment
// they received consisted of. What it does not do is answer somebody who was
// not in the operation at all.
//
// An empty owner never matches, which is what keeps a caller that has no wallet
// to name from being answered by accident.
func movedFor(owner string, entries []Entry) bool {
	if owner == "" {
		return false
	}
	for _, entry := range entries {
		if entry.WalletID == owner {
			return true
		}
	}
	return false
}

func (s *WalletService) replay(ctx context.Context, g security.Grant, key string, kind OperationKind, owner string) (Receipt, bool, error) {
	record, err := Operations(s.db).NewQuery().Where("idempotency_key", "=", key).First(ctx, g)
	if err != nil {
		return Receipt{}, false, err
	}
	if record == nil {
		return Receipt{}, false, nil
	}
	if record.Kind != kind {
		return Receipt{}, false, ErrOperationConflict
	}

	written, err := Entries(s.db).NewQuery().
		Where("operation_id", "=", record.ID).
		OrderBy("position").
		Get(ctx, g)
	if err != nil {
		return Receipt{}, false, err
	}
	entries := make([]Entry, 0, len(written))
	for _, entry := range written {
		if entry != nil {
			entries = append(entries, *entry)
		}
	}

	// The key is not the permission. Everything below this line is somebody's
	// money, and what says it is this caller's is the wallet the caller was
	// authorized for a moment ago -- not the fact that they know a string.
	if !movedFor(owner, entries) {
		return Receipt{}, false, ErrNotFound
	}

	// The key is not the permission. Everything below this line is somebody's
	// money, and what says it is this caller's is the wallet the caller was
	// authorized for a moment ago -- not the fact that they know a string.

	// Only an exchange has one, so only an exchange is asked for one. A read
	// on every replay would be a statement per deposit that answers nothing.
	var conversion *Conversion
	if record.Kind == OperationExchange {
		conversion, err = Conversions(s.db).NewQuery().
			Where("operation_id", "=", record.ID).
			First(ctx, g)
		if err != nil {
			return Receipt{}, false, err
		}
	}

	// And only a payment between two wallets can have been charged, so only one
	// is asked. A replayed payment answers with what the first call charged,
	// read back off the row rather than decided again: the same key twice pays
	// once, at one price.
	var charge *Charge
	if record.Kind == OperationTransfer || record.Kind == OperationExchange {
		charge, err = Charges(s.db).NewQuery().
			Where("operation_id", "=", record.ID).
			First(ctx, g)
		if err != nil {
			return Receipt{}, false, err
		}
	}
	// And only a basket has lines, so only a basket is asked for them. A
	// replayed purchase answers with what the first call bought, read back off
	// the rows rather than priced again: the same key twice pays once, at one
	// price.
	var purchases []Purchase
	if record.Kind == OperationPurchase || record.Kind == OperationRefund {
		lines, err := Purchases(s.db).NewQuery().
			Where("operation_id", "=", record.ID).
			OrderBy("position").
			Get(ctx, g)
		if err != nil {
			return Receipt{}, false, err
		}
		for _, line := range lines {
			if line != nil {
				purchases = append(purchases, *line)
			}
		}
	}
	return Receipt{
		Operation:  *record,
		Entries:    entries,
		Conversion: conversion,
		Charge:     charge,
		Purchases:  purchases,
		Replayed:   true,
	}, true, nil
}
