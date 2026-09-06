package wallet

import (
	"context"
	"fmt"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
)

// Pagination bounds for the listings. A request that asks for everything gets
// the maximum, never everything: an unbounded query is how one page load takes
// a production database down.
const (
	defaultLimit = 50
	maxLimit     = 200
)

// Bounds on the text a caller supplies. They are the widths the columns are
// created at, so a value that fits here fits there.
const (
	maxIdentifierLen     = 64
	maxNameLen           = 120
	maxIdempotencyKeyLen = 128
	maxReasonLen         = 255
	maxCurrencyLen       = 12
	maxDescriptionLen    = 255

	// The two that name a fixed vocabulary rather than something a caller
	// writes, and are wide enough for the longest word in it.
	maxKindLen     = 16
	maxRoundingLen = 16
)

// WalletService holds the rules of this package.
//
// It receives its collaborators through the constructor. There is no container
// and no resolution by reflection: what this service is made of is written at
// the one place that builds it, and reading that place is how somebody learns
// what the package touches.
//
// Everything a handler is allowed to do goes through here. The service is the
// only owner of the database handle, so the request layer cannot reach a Model
// before the policy has answered.
type WalletService struct {
	db        *data.DB
	policy    WalletPolicy
	rates     RateProvider
	fees      FeeProvider
	discounts DiscountProvider
	listeners []Listener
}

// NewWalletService wires the service over the application's database handle.
//
// Every provider may be nil, and nil is not a degraded mode. It is an
// application that moves money only between wallets counted the same way, that
// charges nothing to be paid, and that discounts nothing -- which is most of
// them. A transfer that would need a rate is refused rather than guessed at,
// and a payment with no schedule is a payment with no fee.
//
// They are parameters and not a struct of options, for the reason Config is a
// struct and not a map: what this service is made of is written at the one place
// that builds it, and reading that place is how somebody learns what the package
// reaches for.
//
// The listeners are last and variadic because there may be none, which is the
// ordinary case: an application that wants to be told what its money did says so
// by passing something, and one that does not passes nothing rather than a nil
// it has to remember the meaning of.
func NewWalletService(db *data.DB, rates RateProvider, fees FeeProvider, discounts DiscountProvider, listeners ...Listener) *WalletService {
	return &WalletService{
		db:        db,
		rates:     rates,
		fees:      fees,
		discounts: discounts,
		listeners: append([]Listener(nil), listeners...),
	}
}

// Compile-time proof that the requests honor the validation contract.
var (
	_ validation.Validatable = OpenRequest{}
	_ validation.Validatable = PayRequest{}
	_ validation.Validatable = RefundRequest{}
	_ validation.Validatable = CreditRequest{}
	_ validation.Validatable = DepositRequest{}
	_ validation.Validatable = WithdrawRequest{}
	_ validation.Validatable = TransferRequest{}
	_ validation.Validatable = ReverseRequest{}
	_ validation.Validatable = ConfirmRequest{}
)

// behind reads the operations a page of entries was written under, the rates
// those operations applied, and what they charged.
//
// Three statements for the whole page rather than three per entry: a page is up
// to two hundred rows, and a query per row is the read that makes a statement
// slow on exactly the accounts that have a history worth reading. Two of the
// three are skipped where the page has nothing of that kind on it.
//
// All are read through the Model with the Grant, so all are scoped to the
// tenant like everything else. A ledger that could be explained by another
// customer's operations would be a ledger that leaks one.
func (s *WalletService) behind(ctx context.Context, g security.Grant, entries []*Entry) (map[string]Operation, map[string]Conversion, map[string]Charge, error) {
	ids := make([]any, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry == nil || seen[entry.OperationID] {
			continue
		}
		seen[entry.OperationID] = true
		ids = append(ids, entry.OperationID)
	}
	if len(ids) == 0 {
		return map[string]Operation{}, map[string]Conversion{}, map[string]Charge{}, nil
	}

	rows, err := Operations(s.db).NewQuery().WhereIn("id", ids).Get(ctx, g)
	if err != nil {
		return nil, nil, nil, err
	}
	operations := make(map[string]Operation, len(rows))
	converting := make([]any, 0, len(rows))
	paying := make([]any, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		operations[row.ID] = *row
		switch row.Kind {
		case OperationExchange:
			converting = append(converting, row.ID)
			paying = append(paying, row.ID)
		case OperationTransfer:
			paying = append(paying, row.ID)
		}
	}

	conversions := make(map[string]Conversion, len(converting))
	if len(converting) > 0 {
		rates, err := Conversions(s.db).NewQuery().WhereIn("operation_id", converting).Get(ctx, g)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, rate := range rates {
			if rate != nil {
				conversions[rate.OperationID] = *rate
			}
		}
	}

	charges := make(map[string]Charge, len(paying))
	if len(paying) > 0 {
		charged, err := Charges(s.db).NewQuery().WhereIn("operation_id", paying).Get(ctx, g)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, row := range charged {
			if row != nil {
				charges[row.OperationID] = *row
			}
		}
	}
	return operations, conversions, charges, nil
}

// allowForce asks the policy about ignoring the limit, and only where the
// request asked for it.
//
// The question is about the wallet the money leaves, because that is the money
// the limit protects. A movement nobody asked to force asks nothing, so a
// subject who may never force is refused nothing they did not request.
func (s *WalletService) allowForce(ctx context.Context, actor security.Subject, force bool, record Wallet) error {
	if !force {
		return nil
	}
	_, err := security.Authorize(ctx, s.policy, actor, WalletForce, record)
	return err
}

// discount is what the payer is charged less on this payment, and zero where
// nothing answers.
//
// The provider is asked once, and what it answers is recorded: a discount that
// was decided and not written down is a receipt that says a smaller number than
// the request without saying why.
func (s *WalletService) discount(ctx context.Context, g security.Grant, source, target Wallet, requested Amount) (Amount, error) {
	if s.discounts == nil {
		return 0, nil
	}
	discount, err := s.discounts.Discount(ctx, g, source, target, source.Money(requested))
	if err != nil {
		return 0, err
	}
	if discount < 0 {
		return 0, fmt.Errorf("%w: got %s", ErrDiscountNegative, source.Money(discount))
	}
	return discount, nil
}

// charge is what this payment costs beyond the money it moves, and the wallet
// the fee is credited to.
//
// Both are nil where there is nothing to record: no discount and no schedule.
// A discount with no fee still produces a record and no third wallet, because
// the number the payer was charged less is a fact about the payment whether or
// not anybody charged for it.
//
// The provider is asked once and what it answers is applied once, which is the
// same arrangement the rate has and for the same reason: a schedule read twice
// could answer twice, and then the fee that was taken and the fee on the record
// would be two different stories about one payment.
//
// A fee never crosses a rate. The two wallets have to be counted the same way
// and so does the one collecting, because the fee is a share of the payment and
// is charged in the payment's money -- carrying it through a rate would round a
// number that is already the result of a rounding.
func (s *WalletService) charge(ctx context.Context, g security.Grant, source, target Wallet, requested, discount, base Amount) (*appliedCharge, *Wallet, error) {
	var schedule FeeSchedule
	if s.fees != nil {
		answered, err := s.fees.Fee(ctx, g, target, source.Money(base))
		if err != nil {
			return nil, nil, err
		}
		schedule = answered
	}

	if !schedule.Charges() {
		if discount == 0 {
			return nil, nil, nil
		}
		return &appliedCharge{
			currency:      source.Currency,
			decimalPlaces: source.DecimalPlaces,
			requested:     requested,
			discount:      discount,
			base:          base,
		}, nil, nil
	}

	if err := schedule.Validate(); err != nil {
		return nil, nil, err
	}
	if converts(source, target) {
		return nil, nil, fmt.Errorf("%w: %s and %s", ErrFeeCurrencyMismatch,
			source.Money(base), target.Money(0))
	}
	if schedule.WalletID == source.ID || schedule.WalletID == target.ID {
		return nil, nil, fmt.Errorf("%w: it names one of the two wallets the payment is between", ErrFeeWallet)
	}

	collector, err := Wallets(s.db).NewQuery().WhereKey(schedule.WalletID).First(ctx, g)
	if err != nil {
		return nil, nil, err
	}
	if collector == nil {
		return nil, nil, fmt.Errorf("%w: %s is not a wallet of this customer", ErrFeeWallet, schedule.WalletID)
	}
	if converts(source, *collector) {
		return nil, nil, fmt.Errorf("%w: the payment is %s and the fee would be credited in %s",
			ErrFeeCurrencyMismatch, source.Money(base), collector.Money(0))
	}

	fee, err := schedule.Fee(source.Money(base))
	if err != nil {
		return nil, nil, err
	}
	return &appliedCharge{
		currency:      source.Currency,
		decimalPlaces: source.DecimalPlaces,
		requested:     requested,
		discount:      discount,
		base:          base,
		schedule:      schedule,
		fee:           fee,
	}, collector, nil
}

// reversed reports whether an operation has already been undone.
func (s *WalletService) reversed(ctx context.Context, g security.Grant, operationID string) (bool, error) {
	return Operations(s.db).NewQuery().
		Where("reverses_id", "=", operationID).
		Where("kind", "=", string(OperationReversal)).
		Exists(ctx, g)
}

// mirror turns the settled entries of an operation into the movements that undo
// it, and asks the policy about every wallet they touch.
//
// The question is asked per wallet and not once for the operation, because a
// transfer's two entries are two people's money and a reversal moves both.
//
// Only the entries that settled are mirrored, and an operation that settled
// none has nothing to undo: what a pending operation wrote is a record of what
// was proposed, and appending its opposite would take out money that was never
// put in. That is one rule and not a second path -- you undo the operation that
// moved the money, which for a movement that waited is the confirmation and not
// the request.
func (s *WalletService) mirror(ctx context.Context, g security.Grant, actor security.Subject, operationID string) ([]movement, error) {
	written, err := Entries(s.db).NewQuery().
		Where("operation_id", "=", operationID).
		OrderBy("position").
		Get(ctx, g)
	if err != nil {
		return nil, err
	}
	if len(written) == 0 {
		return nil, ErrNotFound
	}

	rows := Wallets(s.db)
	movements := make([]movement, 0, len(written))
	for _, entry := range written {
		if entry == nil || !entry.Settled {
			continue
		}
		holder, err := rows.NewQuery().WhereKey(entry.WalletID).First(ctx, g)
		if err != nil {
			return nil, err
		}
		if holder == nil {
			return nil, ErrNotFound
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletReverse, *holder); err != nil {
			return nil, err
		}
		kind := EntryWithdraw
		if entry.Kind == EntryWithdraw {
			kind = EntryDeposit
		}
		movements = append(movements, movement{wallet: holder, kind: kind, amount: entry.Amount})
	}
	if len(movements) == 0 {
		return nil, ErrNotSettled
	}
	return movements, nil
}

// settle turns the pending entries of an operation into the movements that make
// them count, and asks the policy about every wallet they touch.
//
// Each movement is the pending one again, in the same direction and for the same
// amount: what is being confirmed is what was written down, so nothing here
// recomputes it. An exchange is not re-quoted either -- the rate belongs to the
// operation that quoted it and is on the record beside it, and asking again
// would settle at a number nobody was told.
//
// The amount is read off the entry rather than from the wallet, and the guard
// runs at this write. A pending withdrawal holds nothing, so the balance that
// decides is the balance now.
func (s *WalletService) settle(ctx context.Context, g security.Grant, actor security.Subject, in ConfirmRequest, operationID string) ([]movement, error) {
	written, err := Entries(s.db).NewQuery().
		Where("operation_id", "=", operationID).
		OrderBy("position").
		Get(ctx, g)
	if err != nil {
		return nil, err
	}
	if len(written) == 0 {
		return nil, ErrNotFound
	}

	rows := Wallets(s.db)
	movements := make([]movement, 0, len(written))
	for _, entry := range written {
		if entry == nil || entry.Settled {
			continue
		}
		holder, err := rows.NewQuery().WhereKey(entry.WalletID).First(ctx, g)
		if err != nil {
			return nil, err
		}
		if holder == nil {
			return nil, ErrNotFound
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletConfirm, *holder); err != nil {
			return nil, err
		}
		if err := s.allowForce(ctx, actor, in.Force, *holder); err != nil {
			return nil, err
		}
		movements = append(movements, movement{
			wallet: holder, kind: entry.Kind, amount: entry.Amount, force: in.Force,
			// The same facts as the movement being settled. It is the same
			// movement, now counting, so a receipt that lost what it was about
			// would be a receipt about a different payment.
			meta: entry.Meta,
		})
	}
	if len(movements) == 0 {
		return nil, ErrNotPending
	}
	return movements, nil
}

// confirmed reports whether an operation has already been made to count.
func (s *WalletService) confirmed(ctx context.Context, g security.Grant, operationID string) (bool, error) {
	return Operations(s.db).NewQuery().
		Where("reverses_id", "=", operationID).
		Where("kind", "=", string(OperationConfirmation)).
		Exists(ctx, g)
}

// convert is how much arrives in the target wallet, and the rate that decided
// it.
//
// The same number and no rate when both wallets count the same thing at the
// same scale. Otherwise the provider is asked once, and the rate it answers
// with is the value everything downstream uses: the multiplication here, the
// row commit writes, and the receipt the caller reads. There is one call and
// one variable, which is the whole of "the rate does not change in the middle
// of the operation" -- a second lookup could answer differently, and then the
// money that moved and the rate on the record would be two different stories
// about one payment.
//
// The provider is not asked to convert, only to quote. What the rate does to
// an amount is this package's arithmetic, under this package's one rounding
// rule, so two conversions of the same amount at the same rate are the same
// number wherever the rate came from.
func (s *WalletService) convert(ctx context.Context, g security.Grant, source, target Wallet, debited Amount) (Amount, *appliedRate, error) {
	if !converts(source, target) {
		return debited, nil, nil
	}
	if s.rates == nil {
		return 0, nil, ErrCurrencyMismatch
	}

	rate, err := s.rates.Rate(ctx, g, source.Currency, target.Currency)
	if err != nil {
		// Named, so that a failure reaching this package's caller says which
		// pair was being quoted. What the provider wrapped travels with it, so
		// the five values above are still what errors.Is answers to.
		return 0, nil, fmt.Errorf("wallet: quoting %s into %s: %w", source.Currency, target.Currency, err)
	}

	from := source.Money(debited)
	converted, err := rate.Convert(from, target.Currency, target.DecimalPlaces)
	if err != nil {
		return 0, nil, err
	}
	return converted.Money.Amount, &appliedRate{rate: rate, from: from, converted: converted}, nil
}

// MaxPurchaseScan is how many purchase rows one batch question reads.
//
// A bound rather than none, for the reason a page has one: an unbounded read is
// how one call takes a production database down on the day a customer has a
// long history. It is stated rather than hidden because it is a real limit --
// a question about a wallet with more recent purchases than this, from the
// wallets named beside it, is answered from what the scan reached.
const MaxPurchaseScan = 2000

// MaxPurchaseQuestions is how many questions one batch may carry.
const MaxPurchaseQuestions = 100

// basket is a priced cart: the lines as they will be recorded and the movements
// that pay for them, in step.
type basket struct {
	lines     []purchaseLine
	movements []movement
}

// priceBasket asks the application what the basket costs and turns it into
// movements.
//
// Every wallet the basket names is read in two statements rather than one per
// line: the sellers and the beneficiaries together, and then the wallets that
// collect fees, which are not known until the schedules have been answered. A
// basket of forty lines therefore costs the same reads as a basket of two, and
// the number of statements does not depend on what somebody put in it.
//
// Nothing here writes. The application is asked about stock before any money is
// judged and about price before any is moved, so a line it refuses is a refusal
// with an empty ledger behind it.
func (s *WalletService) priceBasket(ctx context.Context, g security.Grant, payer Wallet, cart Cart) (basket, error) {
	items := cart.Items()
	named := make([]any, 0, 2*len(items))
	seen := make(map[string]bool, 2*len(items))
	for _, item := range items {
		for _, id := range []string{item.receiver(), item.BeneficiaryWalletID} {
			if id == "" || id == payer.ID || seen[id] {
				continue
			}
			seen[id] = true
			named = append(named, id)
		}
	}

	wallets, err := s.walletsByID(ctx, g, named)
	if err != nil {
		return basket{}, err
	}
	wallets[payer.ID] = payer

	// What the fee is a share of is decided first, for every line, because the
	// wallets that collect those fees are read together afterwards.
	priced := make([]purchaseLine, 0, len(items))
	collectors := make([]any, 0, len(items))
	wanted := make(map[string]bool, len(items))
	for position, item := range items {
		line, err := s.priceLine(ctx, g, payer, wallets, position, item)
		if err != nil {
			return basket{}, err
		}
		priced = append(priced, line)
		if id := line.schedule.WalletID; id != "" && !wanted[id] {
			wanted[id] = true
			collectors = append(collectors, id)
		}
	}

	collecting, err := s.walletsByID(ctx, g, collectors)
	if err != nil {
		return basket{}, err
	}

	out := basket{lines: make([]purchaseLine, 0, len(priced)), movements: make([]movement, 0, 3*len(priced))}
	for _, line := range priced {
		var collector *Wallet
		if line.schedule.Charges() {
			held, known := collecting[line.schedule.WalletID]
			if !known {
				return basket{}, fmt.Errorf("%w: %s is not a wallet of this customer", ErrFeeWallet, line.schedule.WalletID)
			}
			if converts(payer, held) {
				return basket{}, fmt.Errorf("%w: the line is %s and the fee would be credited in %s",
					ErrFeeCurrencyMismatch, payer.Money(line.base), held.Money(0))
			}
			collector = &held
		}

		receiver := wallets[line.receiver]
		// A line that costs nothing writes its row and moves no balance. There
		// is nothing to guard and nothing to record in a ledger: an entry of
		// zero would be a movement saying something happened when nothing did,
		// and it would take a position in a statement that reads as money.
		line.movement = noMovement
		if line.paid > 0 || line.credited > 0 {
			line.movement = len(out.movements)
			out.movements = append(out.movements, movement{
				wallet: &receiver, kind: EntryDeposit, amount: line.credited, meta: line.meta,
			})
			out.movements = append(out.movements, movement{
				wallet: &payer, kind: EntryWithdraw, amount: line.paid, force: line.force, meta: line.meta,
			})
		}
		if collector != nil {
			out.movements = append(out.movements, movement{
				wallet: collector, kind: EntryDeposit, amount: line.fee.Amount,
			})
		}
		out.lines = append(out.lines, line)
	}
	return out, nil
}

// priceLine is one line of a basket, priced and checked but not yet paid for.
func (s *WalletService) priceLine(ctx context.Context, g security.Grant, payer Wallet, wallets map[string]Wallet, position int, item CartItem) (purchaseLine, error) {
	receiver, known := wallets[item.receiver()]
	if !known {
		return purchaseLine{}, ErrNotFound
	}
	if receiver.ID == payer.ID {
		return purchaseLine{}, ErrPaysItself
	}
	if converts(payer, receiver) {
		return purchaseLine{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch,
			payer.Money(0), receiver.Money(0))
	}

	// Whose purchase it is. The money is the payer's either way; what this
	// decides is who the record says bought the thing, which is what the
	// application asks about when it wants to know whether to sell it again.
	owner := payer
	if item.BeneficiaryWalletID != "" {
		held, present := wallets[item.BeneficiaryWalletID]
		if !present {
			return purchaseLine{}, ErrNotFound
		}
		owner = held
	}

	// The stock, before any money is judged. An application that answers no
	// here answers before a single balance has been touched.
	if limited, keeps := item.Product.(LimitedProduct); keeps {
		if err := limited.CanBuy(ctx, g, owner, item.quantity()); err != nil {
			return purchaseLine{}, fmt.Errorf("%w: %s: %w", ErrProductStock, item.Product.ProductKey(), err)
		}
	}

	price, err := s.priceOf(ctx, g, payer, owner, item)
	if err != nil {
		return purchaseLine{}, err
	}
	requested, err := price.Times(item.quantity())
	if err != nil {
		return purchaseLine{}, err
	}

	// What the payer is charged less comes off first, because everything after
	// it is a share of what is being paid rather than of what was asked for.
	// A line that costs nothing is not asked about: a share of nothing is
	// nothing, and a provider answering anything else would be discounting a
	// price that was never charged.
	discount := Amount(0)
	if requested > 0 {
		answered, err := s.discount(ctx, g, payer, receiver, requested)
		if err != nil {
			return purchaseLine{}, err
		}
		discount = answered
	}
	base, err := requested.Sub(discount)
	if err != nil {
		return purchaseLine{}, err
	}
	if base < 0 {
		return purchaseLine{}, ErrAmountNotPositive
	}

	line := purchaseLine{
		position:      position,
		payer:         payer.ID,
		owner:         owner.ID,
		receiver:      receiver.ID,
		productKey:    item.Product.ProductKey(),
		quantity:      item.quantity(),
		currency:      payer.Currency,
		decimalPlaces: payer.DecimalPlaces,
		price:         price,
		requested:     requested,
		discount:      discount,
		base:          base,
		paid:          base,
		credited:      base,
		kind:          PurchasePaid,
		meta:          item.Meta,
	}
	if owner.ID != payer.ID {
		line.kind = PurchaseGift
	}

	// And no fee on a line that moves nothing. A fee with a floor would charge
	// the payer for something the shop gave away, which is a line that is not
	// free however it was priced.
	if s.fees == nil || base == 0 {
		return line, nil
	}
	schedule, err := s.fees.Fee(ctx, g, receiver, payer.Money(base))
	if err != nil {
		return purchaseLine{}, err
	}
	if !schedule.Charges() {
		return line, nil
	}
	if err := schedule.Validate(); err != nil {
		return purchaseLine{}, err
	}
	if schedule.WalletID == payer.ID || schedule.WalletID == receiver.ID {
		return purchaseLine{}, fmt.Errorf("%w: it names one of the two wallets the line is between", ErrFeeWallet)
	}
	fee, err := schedule.Fee(payer.Money(base))
	if err != nil {
		return purchaseLine{}, err
	}
	line.schedule = schedule
	line.fee = fee

	// Who pays the fee is the only thing the schedule changes about the line:
	// on top of it, or out of what arrives. Either way what leaves is what
	// arrives plus the fee, exactly.
	if schedule.Deductible {
		credited, err := base.Sub(fee.Amount)
		if err != nil {
			return purchaseLine{}, err
		}
		if credited <= 0 {
			return purchaseLine{}, fmt.Errorf("%w: %s of %s", ErrFeeExceedsAmount,
				payer.Money(fee.Amount), payer.Money(base))
		}
		line.credited = credited
		return line, nil
	}
	paid, err := base.Add(fee.Amount)
	if err != nil {
		return purchaseLine{}, err
	}
	line.paid = paid
	return line, nil
}

// priceOf is what one of a product costs, read at the payer's scale.
//
// A price written on the line wins over the one the product answers, because a
// caller that says what something costs has already decided; asking the product
// as well would be asking a question whose answer is thrown away.
//
// Zero is a price. A trial, a bundled item, a gift the shop is giving away is a
// line that is bought and costs nothing, and refusing it would leave an
// application with two ways to record what somebody has: through this package
// when there was money and through a table of its own when there was not. What
// is refused is a negative price, which is a payment pointing the wrong way.
func (s *WalletService) priceOf(ctx context.Context, g security.Grant, payer, owner Wallet, item CartItem) (Amount, error) {
	if item.PricePerItem != "" {
		price, err := ParseAmount(item.PricePerItem, payer.DecimalPlaces)
		if err != nil {
			return 0, err
		}
		if price < 0 {
			return 0, ErrAmountNotPositive
		}
		return price, nil
	}
	price, err := item.Product.Price(ctx, g, owner)
	if err != nil {
		return 0, err
	}
	if price < 0 {
		return 0, fmt.Errorf("%w: %s costs %s", ErrAmountNotPositive,
			item.Product.ProductKey(), payer.Money(price))
	}
	return price, nil
}

// walletsByID reads a set of wallets in one statement, keyed by identifier.
//
// One statement and not one per identifier: a basket names as many wallets as
// it has lines, and a read per line is what makes a long basket slow on exactly
// the customers who buy the most. It is read through the Model with the Grant,
// so what comes back is this customer's and a wallet that is missing from the
// answer is a wallet that does not exist here.
func (s *WalletService) walletsByID(ctx context.Context, g security.Grant, ids []any) (map[string]Wallet, error) {
	out := make(map[string]Wallet, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := Wallets(s.db).NewQuery().WhereIn("id", ids).Get(ctx, g)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row != nil {
			out[row.ID] = *row
		}
	}
	return out, nil
}

// reverseLines turns purchased lines into the movements that give them back,
// and asks the policy about every wallet they touch.
//
// The question is asked per wallet and not once for the request, because the
// lines of one refund are several people's money and every one of them is
// moved.
func (s *WalletService) reverseLines(ctx context.Context, g security.Grant, actor security.Subject, in RefundRequest, lines []Purchase) (basket, error) {
	named := make([]any, 0, 3*len(lines))
	seen := make(map[string]bool, 3*len(lines))
	for _, line := range lines {
		for _, id := range []string{line.PayerWalletID, line.ReceiverWalletID, line.FeeWalletID} {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			named = append(named, id)
		}
	}
	wallets, err := s.walletsByID(ctx, g, named)
	if err != nil {
		return basket{}, err
	}
	for id := range seen {
		held, known := wallets[id]
		if !known {
			return basket{}, ErrNotFound
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletRefund, held); err != nil {
			return basket{}, err
		}
		if err := s.allowForce(ctx, actor, in.Force, held); err != nil {
			return basket{}, err
		}
	}

	out := basket{lines: make([]purchaseLine, 0, len(lines)), movements: make([]movement, 0, 3*len(lines))}
	for position, line := range lines {
		payer := wallets[line.PayerWalletID]
		receiver := wallets[line.ReceiverWalletID]

		given := purchaseLine{
			position:      position,
			payer:         line.PayerWalletID,
			owner:         line.OwnerWalletID,
			receiver:      line.ReceiverWalletID,
			productKey:    line.ProductKey,
			quantity:      line.Quantity,
			currency:      line.Currency,
			decimalPlaces: line.DecimalPlaces,
			price:         line.PricePerItem,
			requested:     line.RequestedAmount,
			discount:      line.Discount,
			base:          line.BaseAmount,
			schedule:      line.Schedule(),
			fee:           Fee{Amount: line.FeeAmount, RemainderNumerator: line.RemainderNumerator, RemainderDenominator: line.RemainderDenominator},
			paid:          line.PaidAmount,
			credited:      line.CreditedAmount,
			kind:          PurchaseRefund,
			settles:       line.ID,
			force:         in.Force,
			meta:          in.Meta,
		}
		given.movement = noMovement
		if line.PaidAmount > 0 || line.CreditedAmount > 0 {
			given.movement = len(out.movements)
			out.movements = append(out.movements, movement{
				wallet: &receiver, kind: EntryWithdraw, amount: line.CreditedAmount, force: in.Force, meta: in.Meta,
			})
			out.movements = append(out.movements, movement{
				wallet: &payer, kind: EntryDeposit, amount: line.PaidAmount, meta: in.Meta,
			})
		}
		if line.FeeAmount > 0 && line.FeeWalletID != "" {
			collector := wallets[line.FeeWalletID]
			out.movements = append(out.movements, movement{
				wallet: &collector, kind: EntryWithdraw, amount: line.FeeAmount, force: in.Force,
			})
		}
		out.lines = append(out.lines, given)
	}
	return out, nil
}

// refunded reports whether any of these lines has already been given back.
func (s *WalletService) refunded(ctx context.Context, g security.Grant, lines []Purchase) (bool, error) {
	ids := make([]any, 0, len(lines))
	for _, line := range lines {
		ids = append(ids, line.ID)
	}
	if len(ids) == 0 {
		return false, nil
	}
	return Purchases(s.db).NewQuery().
		WhereIn("settles_id", ids).
		Where("kind", "=", string(PurchaseRefund)).
		Exists(ctx, g)
}

// collect adds a value to a set of query arguments, once.
func collect(into *[]any, seen map[string]bool, key, value string) {
	if seen[key] {
		return
	}
	seen[key] = true
	*into = append(*into, value)
}

// Balanced reports that the ledger and the balance column agree.
func (r Reconciliation) Balanced() bool {
	if r.Settled != r.Wallet.Balance {
		return false
	}
	return r.Entries == 0 || r.LastBalanceAfter == r.Wallet.Balance
}

// Difference is what the balance column holds beyond what the ledger explains,
// and zero where the two agree.
func (r Reconciliation) Difference() Amount { return r.Wallet.Balance - r.Settled }

// walk sums one wallet's ledger up to the position the wallet held when it was
// read.
//
// The bound is the whole of why the answer means anything. Without it the sum
// would include rows written while the pages were being read and would be
// compared against a balance from before them, so a busy wallet would report a
// difference that is only the reading.
func (s *WalletService) walk(ctx context.Context, g security.Grant, holder Wallet) (Reconciliation, error) {
	out := Reconciliation{Wallet: holder, Frozen: bool(holder.Frozen)}
	entries := Entries(s.db)
	after := int64(0)
	expected := int64(1)
	for {
		page, err := entries.NewQuery().
			Where("wallet_id", "=", holder.ID).
			Where("sequence", ">", after).
			Where("sequence", "<=", holder.LastSequence).
			OrderBy("sequence").
			Limit(maxLimit).
			Get(ctx, g)
		if err != nil {
			return Reconciliation{}, err
		}
		if len(page) == 0 {
			return out, nil
		}
		for _, entry := range page {
			if entry == nil {
				continue
			}
			if entry.Sequence != expected {
				out.Gaps++
			}
			expected = entry.Sequence + 1

			if entry.Settled {
				sum, err := out.Settled.Add(entry.Signed())
				if err != nil {
					return Reconciliation{}, err
				}
				out.Settled = sum
			} else {
				direction := entry.Amount
				if entry.Kind == EntryWithdraw {
					direction = -entry.Amount
				}
				sum, err := out.Proposed.Add(direction)
				if err != nil {
					return Reconciliation{}, err
				}
				out.Proposed = sum
			}

			out.Entries++
			out.LastBalanceAfter = entry.BalanceAfter
			out.LastSequence = entry.Sequence
			after = entry.Sequence
		}
		if len(page) < maxLimit {
			return out, nil
		}
	}
}

// freeze stops a wallet being served.
//
// One column and one statement. It is not guarded on the balance the scan saw,
// and that is deliberate: a movement arriving after the scan carries the
// difference forward unchanged, because it adds the same amount to the column
// and to the ledger. So the conclusion stays true of the wallet as it is now,
// and refusing to freeze because it moved would leave a wallet unserved by
// nothing while the difference is still there.
func (s *WalletService) freeze(ctx context.Context, g security.Grant, walletID string) error {
	affected, err := Wallets(s.db).NewQuery().
		WhereKey(walletID).
		Update(ctx, g, map[string]any{"frozen": int64(1)})
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Compile-time proof that the request honors the validation contract.
var _ validation.Validatable = RebuildRequest{}
