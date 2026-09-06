package wallet

import (
	"context"
	"errors"
	"fmt"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/database/model"
)

// PayRequest is what paying for a basket takes.
//
// There is no pending mode here, and its absence is a decision. A movement is
// recorded without counting so that somebody can say later whether it happened;
// a basket that has not been paid for is a basket, and what an application
// wants held is the delivery, which is a transfer whose two sides settle apart.
// A second half-paid state, with lines that are on the record and money that is
// not, would be a second answer to what "has this been bought" means.
type PayRequest struct {
	// IdempotencyKey is the caller's name for this request. Sending the same
	// key twice pays once.
	IdempotencyKey string
	// PayerWalletID is the wallet the money leaves. Every line of the basket
	// is paid from it, and every price is read at its scale.
	PayerWalletID string
	// Cart is what is being bought.
	Cart Cart
	// Force asks for the payment even where the balance and the credit limit do
	// not cover it, and is answered by WalletForce on the wallet paying.
	Force bool
}

// RefundRequest is what giving back some lines of a purchase takes.
type RefundRequest struct {
	// IdempotencyKey is the caller's name for this request. It is the refund's
	// own key, and never the key of the purchase being given back.
	IdempotencyKey string
	// PurchaseIDs are the lines to give back. They may come from one basket or
	// from several: what is undone is a line, and which request it was part of
	// changes nothing about the money.
	PurchaseIDs []string
	// Reason is what the refund is recorded as. It is required, for the reason a
	// reversal's is: money that moved for no recorded reason is money nobody can
	// account for later.
	Reason string
	// Force asks for the movement even where the wallet giving the money back
	// does not cover it, and is answered by WalletForce.
	Force bool
	// Meta is what the application attaches to the refund.
	Meta Meta
}

// Pay buys a basket with one wallet's money.
//
// Every line is one movement out of the payer and one into the wallet that
// sells it, plus a third into whoever collects the fee, and all of them are one
// operation and one transaction. There is no state in which half a basket was
// paid for: a line the application refuses, a price that does not fit or a
// balance that runs out on the fourth of six leaves nothing written at all.
//
// The prices, the discounts and the fees are the application's, through the
// seams it already supplies. What this package owns is the arithmetic and the
// record: what was asked for, what was taken off, what the fee was computed
// from and what actually left and arrived are all on the line's own row, so a
// receipt that says a different number from the catalogue says why.
//
// A basket crosses no rate. Every wallet it names has to be counted the way the
// payer's is, because a basket that converted line by line would round once per
// line and the total would not be the total of anything -- an application
// selling in another currency prices the line in the payer's money, which is
// what Product.Price is asked for.
//
// A line bought for somebody else is a gift: the money still leaves the payer
// and still arrives at the seller, and the record says the beneficiary bought
// it. That is the whole of what a gift changes, and it is what "has this person
// already got one" reads afterwards.
func (s *WalletService) Pay(ctx context.Context, actor security.Subject, in PayRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletPay, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationPurchase); err != nil || found {
		return receipt, err
	}

	payer, err := Wallets(s.db).NewQuery().WhereKey(in.PayerWalletID).First(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if payer == nil {
		return Receipt{}, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletPay, *payer); err != nil {
		return Receipt{}, err
	}
	if err := s.allowForce(ctx, actor, in.Force, *payer); err != nil {
		return Receipt{}, err
	}

	priced, err := s.priceBasket(ctx, g, *payer, in.Cart)
	if err != nil {
		return Receipt{}, err
	}

	return s.commit(ctx, g, operation{
		key:   in.IdempotencyKey,
		kind:  OperationPurchase,
		meta:  in.Cart.Meta(),
		lines: priced.lines,
	}, priced.movements)
}

// Refund gives back some of the lines of a purchase.
//
// It moves back exactly what moved, on each side, read off the line's own row:
// what left the payer goes back to the payer, what reached the seller leaves the
// seller, and a fee that was taken leaves whoever collected it. Nothing is
// recomputed -- a schedule that answers differently today would otherwise make
// a refund of last month's purchase a different number from the purchase.
//
// Nothing already written changes. The line that was bought stays on the record
// exactly as it was bought, and a second row appears beside it naming the one it
// settles -- which is why a basket half of which was given back cannot be given
// back whole, and why a statement afterwards reads as what was bought and then
// what came back.
//
// A line is given back once. The second attempt answers ErrAlreadyRefunded, and
// the refusal is a unique index rather than a check, so two refunds arriving
// together cannot both be the one that succeeds.
func (s *WalletService) Refund(ctx context.Context, actor security.Subject, in RefundRequest) (Receipt, error) {
	if errs := in.Validate(); errs.Any() {
		return Receipt{}, errs
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletRefund, Wallet{})
	if err != nil {
		return Receipt{}, err
	}

	if receipt, found, err := s.replay(ctx, g, in.IdempotencyKey, OperationRefund); err != nil || found {
		return receipt, err
	}

	ids := make([]any, 0, len(in.PurchaseIDs))
	for _, id := range in.PurchaseIDs {
		ids = append(ids, id)
	}
	rows, err := Purchases(s.db).NewQuery().WhereIn("id", ids).OrderBy("position").Get(ctx, g)
	if err != nil {
		return Receipt{}, err
	}
	if len(rows) != len(in.PurchaseIDs) {
		return Receipt{}, ErrNotFound
	}

	lines := make([]Purchase, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			return Receipt{}, ErrNotFound
		}
		if row.Kind == PurchaseRefund {
			return Receipt{}, ErrNotRefundable
		}
		lines = append(lines, *row)
	}
	if given, err := s.refunded(ctx, g, lines); err != nil {
		return Receipt{}, err
	} else if given {
		return Receipt{}, ErrAlreadyRefunded
	}

	priced, err := s.reverseLines(ctx, g, actor, in, lines)
	if err != nil {
		return Receipt{}, err
	}

	receipt, err := s.commit(ctx, g, operation{
		key:    in.IdempotencyKey,
		kind:   OperationRefund,
		reason: in.Reason,
		meta:   in.Meta,
		lines:  priced.lines,
	}, priced.movements)
	if err != nil && !errors.Is(err, ErrInsufficientFunds) {
		// The unique index on the line being settled is what refuses a second
		// refund, and this turns its answer into ours. Read after the failure,
		// never instead of it: a check that ran before is a check two concurrent
		// refunds both passed.
		if given, lookupErr := s.refunded(ctx, g, lines); lookupErr == nil && given {
			return Receipt{}, ErrAlreadyRefunded
		}
	}
	return receipt, err
}

// Bought answers, for each question, the line that already bought it -- and nil
// where nothing did.
//
// One statement for the whole set rather than one per question. The rows that
// could answer any of them are read together and matched in memory, so a shop
// checking forty products against one customer makes one read and not forty --
// which is the difference between a page that loads and a page that times out on
// the customers who buy the most.
//
// A line that was given back does not answer. The refund is a row of its own
// naming the line it settles, so what is asked here is "bought and not given
// back", which is what a shop deciding whether to sell something again means.
//
// It is bounded by MaxPurchaseScan, and that bound is real: a question about a
// wallet with more recent purchases than that, among the wallets named beside
// it, is answered from what the scan reached.
func (s *WalletService) Bought(ctx context.Context, actor security.Subject, questions []PurchaseQuery) ([]*Purchase, error) {
	if len(questions) == 0 {
		return nil, nil
	}
	if len(questions) > MaxPurchaseQuestions {
		return nil, fmt.Errorf("wallet: %d questions were asked at once, and the most is %d",
			len(questions), MaxPurchaseQuestions)
	}

	g, err := security.Authorize(ctx, s.policy, actor, WalletPurchases, Wallet{})
	if err != nil {
		return nil, err
	}

	owners := make([]any, 0, len(questions))
	receivers := make([]any, 0, len(questions))
	products := make([]any, 0, len(questions))
	seen := map[string]bool{}
	for _, question := range questions {
		if question.OwnerWalletID == "" || question.ReceiverWalletID == "" || question.ProductKey == "" {
			return nil, fmt.Errorf("wallet: a question needs an owner, a receiver and a product")
		}
		collect(&owners, seen, "o:"+question.OwnerWalletID, question.OwnerWalletID)
		collect(&receivers, seen, "r:"+question.ReceiverWalletID, question.ReceiverWalletID)
		collect(&products, seen, "p:"+question.ProductKey, question.ProductKey)
	}

	// Every wallet whose purchases would be read is asked about, because a read
	// is a decision like any other: a page of what somebody bought is a page of
	// what they spend their money on.
	held, err := s.walletsByID(ctx, g, owners)
	if err != nil {
		return nil, err
	}
	for _, id := range owners {
		record, known := held[id.(string)]
		if !known {
			return nil, ErrNotFound
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletPurchases, record); err != nil {
			return nil, err
		}
	}

	rows, err := Purchases(s.db).NewQuery().
		WhereIn("owner_wallet_id", owners).
		WhereIn("receiver_wallet_id", receivers).
		WhereIn("product_key", products).
		OrderByDesc("sequence").
		OrderByDesc("id").
		Limit(MaxPurchaseScan).
		Get(ctx, g)
	if err != nil {
		return nil, err
	}

	// The refunds first, because a line that was given back answers nothing and
	// the row that says so can be anywhere in the page.
	given := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row != nil && row.Kind == PurchaseRefund {
			given[row.SettlesID] = true
		}
	}

	answers := make([]*Purchase, len(questions))
	for i, question := range questions {
		for _, row := range rows {
			if row == nil || given[row.ID] {
				continue
			}
			if row.OwnerWalletID != question.OwnerWalletID ||
				row.ReceiverWalletID != question.ReceiverWalletID ||
				row.ProductKey != question.ProductKey {
				continue
			}
			if row.Kind == PurchasePaid || (question.IncludeGifts && row.Kind == PurchaseGift) {
				answers[i] = row
				break
			}
		}
	}
	return answers, nil
}

// PurchasesOf returns a page of what one wallet bought, newest first.
//
// It is a read, and it asks the policy the same two questions a read of the
// wallet itself asks: whether this subject reads purchases, and whether they
// read this wallet's. What somebody buys is at least as private as what they
// hold, so a path to it that skipped the second question would be the widest
// read in the package.
func (s *WalletService) PurchasesOf(ctx context.Context, actor security.Subject, walletID string, page data.Query) ([]*Purchase, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletPurchases, Wallet{})
	if err != nil {
		return nil, err
	}

	owner, err := Wallets(s.db).NewQuery().WhereKey(walletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletPurchases, *owner); err != nil {
		return nil, err
	}

	// The page is anchored on the pair of the sequence and the identifier, which
	// is the pair it is ordered by. The sequence alone is not unique here: a
	// line that cost nothing produced no movement and records zero, and two
	// lines of one basket take their sequences from the ledgers of two
	// different wallets. Anchoring on it alone would skip every row that shares
	// the sequence of the last one on a page.
	rows := Purchases(s.db)
	query := rows.NewQuery().Where("owner_wallet_id", "=", owner.ID)
	if page.Cursor != "" {
		anchor, err := rows.NewQuery().WhereKey(page.Cursor).Value(ctx, g, "sequence")
		if err != nil {
			return nil, err
		}
		if anchor == nil {
			return nil, nil
		}
		query = query.Where(func(before *model.Builder[Purchase]) {
			before.Where("sequence", "<", anchor).
				OrWhere(func(equal *model.Builder[Purchase]) {
					equal.Where("sequence", "=", anchor).Where("id", "<", page.Cursor)
				})
		})
	}
	return query.OrderByDesc("sequence").OrderByDesc("id").Limit(boundedLimit(page.Limit)).Get(ctx, g)
}
