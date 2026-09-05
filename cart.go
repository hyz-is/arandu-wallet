package wallet

import (
	"context"
	"errors"
	"fmt"

	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
)

// MaxCartLines is how many lines one basket may carry.
//
// A bound rather than none, because a basket is paid in one transaction and
// every line of it takes a row lock on a wallet: a basket nobody bounded is a
// transaction that holds a shop's wallet for as long as somebody's script felt
// like adding to it.
const MaxCartLines = 100

// MaxItemQuantity is how many of one product a line may carry.
const MaxItemQuantity = 10_000

// The refusals about a basket. Each is a different thing to fix: an empty
// basket is the caller's, a product that ran out is the application's stock,
// and a basket that would need a rate is the wallets it names.
var (
	// ErrCartEmpty is returned when a basket has nothing in it. A payment of
	// nothing is an operation with no entries, which is a record that says
	// something happened when nothing did.
	ErrCartEmpty = errors.New("wallet: the basket is empty, and paying for nothing is not a movement")

	// ErrCartTooLarge is returned when a basket carries more lines than
	// MaxCartLines.
	ErrCartTooLarge = fmt.Errorf("wallet: the basket carries more than %d lines", MaxCartLines)

	// ErrItemQuantity is returned when a line asks for a quantity that is not a
	// positive number within MaxItemQuantity.
	ErrItemQuantity = fmt.Errorf("wallet: a line is for between 1 and %d of a product", MaxItemQuantity)

	// ErrProductWallet is returned when a line does not say where its money
	// arrives, or names a wallet that is not this customer's.
	ErrProductWallet = errors.New("wallet: the line names no wallet for its money to arrive in")

	// ErrProductStock is returned when the application says a product cannot be
	// bought in this quantity by this buyer. It is the application's answer,
	// asked before any money moves.
	ErrProductStock = errors.New("wallet: the application refused the quantity asked for")

	// ErrPaysItself is returned when a line would move money out of a wallet and
	// back into it. It is a pair of entries that cancel, which is a statement
	// that says something happened when nothing did.
	ErrPaysItself = errors.New("wallet: a line cannot be paid to the wallet paying for it")
)

// Product is what an application sells.
//
// It is an interface this package declares and never implements, for the reason
// RateProvider is one: what a product is, what it costs and how many of it are
// left are the application's questions, and a package that answered any of them
// would be a package deciding somebody's catalogue. What this package owns is
// the money -- it debits, it credits, and it records who bought what from whom.
//
// The three answers are all it needs. A key that names the product on the
// record, a wallet for the money to arrive in, and a price for this buyer.
type Product interface {
	// ProductKey names this product on the record, and it is the application's
	// own identifier rather than anything derived from a Go type.
	//
	// A type name changes when its package is renamed, moved or vendored, and
	// none of those changes touch the rows already stored: every purchase would
	// go on naming a product nothing answers to, and nothing would say so.
	ProductKey() string

	// ReceiverWalletID is the wallet the money for this product arrives in.
	ReceiverWalletID() string

	// Price is what one of this product costs this buyer, in the buyer's minor
	// units.
	//
	// The buyer is passed because a price can be about who is buying -- a
	// wholesale rate, a member's price, a currency the catalogue is kept in.
	// The Grant is passed for the reason every seam here takes one: a price can
	// be a tenant's own, and a provider that cannot tell whose price it is asked
	// for is a provider that answers with somebody else's.
	Price(ctx context.Context, g security.Grant, buyer Wallet) (Amount, error)
}

// LimitedProduct is a product the application keeps a stock of.
//
// It is asked before any money moves, so a basket with one line the application
// refuses leaves nothing written: the refusal happens where the basket is read
// and not halfway through paying for it.
//
// It is a second interface rather than a method on the first, so a catalogue
// with nothing to run out of implements nothing extra -- and a product that does
// keep stock is recognised by what it answers rather than by a flag somebody
// remembered to set.
type LimitedProduct interface {
	Product

	// CanBuy reports why this buyer may not take this many, and nil when they
	// may.
	//
	// It reports the reason rather than a yes or no, because "out of stock",
	// "one per customer" and "not sold in your country" are three different
	// things to tell somebody, and this package has no way to tell them apart
	// from a false.
	CanBuy(ctx context.Context, g security.Grant, buyer Wallet, quantity int) error
}

// CartItem is one line of a basket.
type CartItem struct {
	// Product is what is being bought.
	Product Product

	// Quantity is how many of it. Zero is read as one, which is what a line
	// somebody wrote without a number means.
	Quantity int

	// PricePerItem overrides what the product answers, as a decimal at the
	// payer's own scale.
	//
	// Empty is the ordinary line, which asks the product. It is text rather than
	// an amount because that is how every amount arrives at this package's
	// border: a caller writing "10.50" does not have to know the scale, and one
	// writing 1050 does.
	PricePerItem string

	// ReceiverWalletID overrides where the money arrives. Empty is the
	// product's own wallet, which is the ordinary line.
	ReceiverWalletID string

	// BeneficiaryWalletID is whose purchase this is, when it is not the payer's.
	//
	// Empty is the ordinary line: the payer buys for themselves. Anything else
	// is a gift -- the money still leaves the payer and still arrives at the
	// receiver, and what changes is who the record says bought it, which is what
	// "has this person already got one" is asked about afterwards.
	BeneficiaryWalletID string

	// Meta is what the application attaches to this line.
	Meta Meta
}

// quantity is how many of the product this line is for, reading zero as one.
func (i CartItem) quantity() int {
	if i.Quantity == 0 {
		return 1
	}
	return i.Quantity
}

// receiver is the wallet this line's money arrives in.
func (i CartItem) receiver() string {
	if i.ReceiverWalletID != "" {
		return i.ReceiverWalletID
	}
	if i.Product == nil {
		return ""
	}
	return i.Product.ReceiverWalletID()
}

// Cart is what is being bought.
//
// It is immutable, and every method that adds to it answers with a new basket.
// A basket that could be changed after it was priced is a basket where the
// receipt and the payment are about different things -- and the value handed to
// Pay would be one the caller could still be holding a reference to.
type Cart struct {
	items []CartItem
	meta  Meta
}

// NewCart is a basket of these lines.
func NewCart(items ...CartItem) Cart {
	return Cart{items: append([]CartItem(nil), items...)}
}

// With is this basket and these lines, as a new basket.
func (c Cart) With(items ...CartItem) Cart {
	out := Cart{items: make([]CartItem, 0, len(c.items)+len(items)), meta: c.meta}
	out.items = append(out.items, c.items...)
	out.items = append(out.items, items...)
	return out
}

// WithMeta is this basket carrying these facts, as a new basket.
func (c Cart) WithMeta(meta Meta) Cart {
	return Cart{items: append([]CartItem(nil), c.items...), meta: meta}
}

// Items are the lines, in the order they were added.
func (c Cart) Items() []CartItem { return append([]CartItem(nil), c.items...) }

// Meta is what the application attached to the basket as a whole.
func (c Cart) Meta() Meta { return c.meta }

// Lines is how many lines the basket has.
func (c Cart) Lines() int { return len(c.items) }

// Quantity is how many items the basket holds, counting the quantity of each
// line.
func (c Cart) Quantity() int {
	total := 0
	for _, item := range c.items {
		total += item.quantity()
	}
	return total
}

// Validate reports what the basket cannot be paid as.
//
// It reads the lines and never the wallets: whether a wallet exists, whose it
// is and what it is counted in are questions for the service, after the policy
// has answered.
func (c Cart) Validate() validation.Errors {
	e := validation.Errors{}
	if len(c.items) == 0 {
		e.Add("cart", ErrCartEmpty.Error())
		return e
	}
	if len(c.items) > MaxCartLines {
		e.Add("cart", ErrCartTooLarge.Error())
	}
	checkMeta(e, "cart_meta", c.meta)

	for i, item := range c.items {
		field := fmt.Sprintf("cart.%d", i)
		if item.Product == nil {
			e.Add(field, "names no product")
			continue
		}
		if item.Product.ProductKey() == "" {
			e.Add(field, "the product has no key, so nothing could say afterwards what was bought")
		}
		validation.MaxLen(e, field, item.Product.ProductKey(), maxIdentifierLen)
		if item.quantity() < 1 || item.quantity() > MaxItemQuantity {
			e.Add(field, ErrItemQuantity.Error())
		}
		if item.receiver() == "" {
			e.Add(field, ErrProductWallet.Error())
		}
		validation.MaxLen(e, field, item.receiver(), maxIdentifierLen)
		validation.MaxLen(e, field, item.BeneficiaryWalletID, maxIdentifierLen)
		checkMeta(e, field, item.Meta)
	}
	return e
}

// Compile-time proof that the basket honors the validation contract.
var _ validation.Validatable = Cart{}
