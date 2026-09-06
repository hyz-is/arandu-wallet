package wallet

import (
	"context"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
)

// EventKind names what happened.
//
// The set is closed and the values are constants, for the reason the actions
// are: a listener switches on one of these, and a kind assembled at run time is
// a branch nobody can find by reading the code.
type EventKind string

const (
	// WalletOpened is a wallet that now exists.
	WalletOpened EventKind = "wallet.opened"
	// WalletCreditChanged is a change to how far below zero a wallet may go.
	WalletCreditChanged EventKind = "wallet.credit_changed"
	// MoneyMoved is one movement that counted: a balance changed by exactly the
	// amount on the event, in the direction on it.
	MoneyMoved EventKind = "wallet.money_moved"
	// MoneyProposed is one movement that was recorded and did not count. The
	// balance on it is the balance the movement did not change, and the money
	// arrives when somebody confirms the operation.
	MoneyProposed EventKind = "wallet.money_proposed"
)

// Event is one thing that happened, told to whoever asked to be told.
//
// It carries what a listener needs to write a line somebody can read a year
// later: who did it, whose money it was, how much, and what it left behind. It
// does not carry the record, because a record handed to a listener is a record a
// listener can save -- and a write nobody authorized is exactly what this
// package exists to make impossible.
type Event struct {
	// Kind is what happened.
	Kind EventKind
	// At is when, in UTC.
	At time.Time
	// Tenant is the customer it happened in. It comes from the Grant, like
	// every other tenant here.
	Tenant string
	// ActorID is who did it, off the Grant the policy issued rather than off
	// the request: it is the subject the rules agreed to and not the one
	// somebody claimed to be.
	ActorID string

	// WalletID is the wallet it happened to, and Currency and DecimalPlaces are
	// how its money is counted -- so a listener can render an amount without a
	// second read.
	WalletID      string
	Currency      Currency
	DecimalPlaces int

	// OperationID is the request the movement was part of, and OperationKind
	// what that request was. Both are empty on an event about a wallet rather
	// than about money.
	OperationID   string
	OperationKind OperationKind

	// EntryID is the ledger row, and EntryKind its direction. Both are empty on
	// an event about a wallet.
	EntryID   string
	EntryKind EntryKind

	// Amount is how much moved, always positive: the direction is EntryKind's
	// to carry. On a wallet that was opened it is zero, and on a credit limit
	// that changed it is the new limit.
	Amount Amount
	// Balance is what the wallet held afterwards.
	Balance Amount

	// Meta is what the application attached to the movement this event is
	// about.
	Meta Meta
}

// Listener is something told what happened.
//
// It is called after the write has committed, in the goroutine that made it, and
// what it does is on the path of the request that caused it. A listener that
// talks to something slow makes the screen slow; one that has to do that hands
// the work to a queue and returns.
//
// It returns nothing, and that is the contract rather than an omission. The
// write is already durable by the time it is called, so there is no failure a
// listener could report that anything could still act on -- and an error that
// travelled back to the caller would report a write that succeeded as one that
// did not.
type Listener func(context.Context, Event)

// notify tells every listener what happened.
//
// It takes the Grant rather than a tenant string, so the tenant and the actor on
// an event are the ones the statements ran under and cannot drift from them.
//
// It is called after the transaction and never inside it. A listener told about
// money that was then rolled back has told somebody about a thing that did not
// happen, and there is no message that takes it back -- which is the whole
// reason the events of an operation are collected while it runs and handed over
// only once the database has kept them.
func (s *WalletService) notify(ctx context.Context, g security.Grant, events ...Event) {
	if len(s.listeners) == 0 || len(events) == 0 {
		return
	}
	at := time.Now().UTC()
	for _, event := range events {
		event.At = at
		event.Tenant = data.Tenant(g)
		event.ActorID = g.Subject().ID
		for _, listen := range s.listeners {
			listen(ctx, event)
		}
	}
}

// movedEvents is what an operation's movements are told as, once they have
// committed.
//
// One event per entry rather than one per operation: a transfer is two people's
// money and a basket is several, and a listener that had to take an operation
// apart to find out whose balance changed would be a listener writing the loop
// that is written here.
func movedEvents(op Operation, entries []Entry, wallets map[string]Wallet) []Event {
	events := make([]Event, 0, len(entries))
	for _, entry := range entries {
		kind := MoneyMoved
		if !entry.Settled {
			kind = MoneyProposed
		}
		held := wallets[entry.WalletID]
		events = append(events, Event{
			Kind:          kind,
			WalletID:      entry.WalletID,
			Currency:      held.Currency,
			DecimalPlaces: held.DecimalPlaces,
			OperationID:   op.ID,
			OperationKind: op.Kind,
			EntryID:       entry.ID,
			EntryKind:     entry.Kind,
			Amount:        entry.Amount,
			Balance:       entry.BalanceAfter,
			Meta:          entry.Meta,
		})
	}
	return events
}
