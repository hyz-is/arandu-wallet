//go:build example

// Command example wires this package the way an application does, and then
// exercises it.
//
// Run it:
//
//	go run -tags example ./example
//
// It is behind a build tag so that installing this package never compiles it:
// what a program does is not a capability whoever ran `go get` agreed to, and a
// library that carries a main package carries one anyway.
//
// It uses SQLite in a temporary directory and leaves nothing behind, so it needs
// no configuration and no server. What it does not do is serve the screens:
// those are published into a project and compiled there, which takes three
// commands and an application to run them in. The wiring below is the same
// either way -- this stops one step short of Routes, and the README carries the
// rest.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	hdatabase "github.com/arandu-io/hesape/database"

	wallet "github.com/hyz-is/arandu-wallet"

	// The driver. An application picks its own, and this package never does.
	_ "modernc.org/sqlite"
)

// tenant is the customer this example writes as. An application reads its own
// from configuration, and never from a request.
const tenant = "acme"

// quotedAt is when the pretend rate provider says its rate was obtained.
//
// A fixed moment rather than a clock read, because a recorded rate is a fact
// about a moment and this example prints the row it wrote: a clock read twice
// would print two.
var quotedAt = time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)

// Rates is the rate provider this pretend application supplies.
//
// The package ships none, on purpose: a rate comes from somewhere outside the
// process, and one that declares it makes no outbound call has nowhere to get
// it. What a provider answers is the rate and never the converted amount --
// applying it, rounding it and writing it down belong to the package, so every
// exchange in the application is rounded the same way.
type Rates struct{}

// Rate answers five of the target for one of the source, whatever the pair.
func (Rates) Rate(_ context.Context, _ security.Grant, from, to wallet.Currency) (wallet.Rate, error) {
	return wallet.Rate{From: from, To: to, Numerator: 5, Denominator: 1, QuotedAt: quotedAt}, nil
}

// Fees is what this pretend application charges to be paid.
//
// A tenth of the payment, into the house wallet. What a merchant charges is the
// application's business, which is why this is a seam and not a setting: the
// schedule is asked for once per payment and written down as it was answered,
// so a provider that answers differently a moment later does not change what a
// receipt already said.
type Fees struct{ house string }

// Fee answers the schedule for a receiver counted the way the house wallet is,
// and nothing for anybody else.
//
// The zero schedule is no fee, and it is the ordinary answer. It is what this
// provider has to give for a payment into another currency: a fee is a share of
// the payment and is charged in the payment's money, so carrying it through a
// rate would round a number that is already the result of a rounding -- the
// package refuses that, and a provider that answered anyway would be refusing
// the payment on the application's behalf.
func (f Fees) Fee(_ context.Context, _ security.Grant, receiver wallet.Wallet, _ wallet.Money) (wallet.FeeSchedule, error) {
	if receiver.Currency != "BRL" {
		return wallet.FeeSchedule{}, nil
	}
	return wallet.FeeSchedule{Numerator: 1, Denominator: 10, WalletID: f.house}, nil
}

// Discounts is what this pretend application takes off a payment.
type Discounts struct{}

// Discount answers one unit off every payment, which is enough to see it on a
// receipt.
func (Discounts) Discount(_ context.Context, _ security.Grant, _, _ wallet.Wallet, _ wallet.Money) (wallet.Amount, error) {
	return 100, nil
}

// Book is a product this pretend application sells.
//
// The package declares the interface and never implements it: what a product
// is, what it costs and how many are left are the application's questions. What
// it has to answer is a key for the record, a wallet for the money, and a price
// for this buyer.
type Book struct {
	key    string
	wallet string
	price  wallet.Amount
	stock  int
}

// ProductKey names it on the record.
func (b *Book) ProductKey() string { return b.key }

// ReceiverWalletID is where the money for it arrives.
func (b *Book) ReceiverWalletID() string { return b.wallet }

// Price is what one costs this buyer, in the buyer's minor units.
func (b *Book) Price(_ context.Context, _ security.Grant, _ wallet.Wallet) (wallet.Amount, error) {
	return b.price, nil
}

// CanBuy is the stock, and it is asked before any money moves.
func (b *Book) CanBuy(_ context.Context, _ security.Grant, _ wallet.Wallet, quantity int) error {
	if quantity > b.stock {
		return fmt.Errorf("only %d left", b.stock)
	}
	return nil
}

// Compile-time proof that the catalogue answers the seams the package declares.
var (
	_ wallet.RateProvider     = Rates{}
	_ wallet.FeeProvider      = Fees{}
	_ wallet.DiscountProvider = Discounts{}
	_ wallet.LimitedProduct   = (*Book)(nil)
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "example:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "arandu-wallet-example")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	pool, err := sql.Open("sqlite", filepath.Join(dir, "wallet.db"))
	if err != nil {
		return err
	}
	defer func() { _ = pool.Close() }()

	// ---- the wiring, which is what an application copies -------------------
	//
	// Every collaborator is a parameter. There is no container, no provider and
	// no discovery: what this module touches is written here and read here. An
	// application reads the wallet its fees are collected into from its own
	// configuration; this example opens one below, so the schedule is built
	// after the wallets and the service is built with it.
	key := []byte("0123456789abcdef0123456789abcdef")
	sessions := security.NewSessionStore(key, time.Hour, false, security.NewMemoryBackend())
	handle := data.Wrap(pool, data.DialectSQLite)

	module, err := wallet.New(wallet.Config{
		Tenant:    tenant,
		CSRF:      security.NewCSRF(key, time.Hour),
		Rates:     Rates{},
		Discounts: Discounts{},
		Listeners: []wallet.Listener{announce},
	}, handle, sessions)
	if err != nil {
		return err
	}

	// In an application this is `aru migrate`, run as a pipeline step. Never at
	// boot: with N replicas, N migrations race.
	connection := hdatabase.NewConnection(pool, "", "", map[string]any{
		"driver": string(hdatabase.DialectSQLite),
		"name":   "example",
	})
	for _, migration := range module.Migrations() {
		if err := migration.Up(ctx, hdatabase.ForMigrations(connection)); err != nil {
			return fmt.Errorf("applying %s: %w", migration.GetName(), err)
		}
	}

	// Who is acting. In an application this comes off the session; here it is
	// written out, carrying the role the shipped policy reads.
	staff := security.Subject{
		ID: "staff-1", Tenant: tenant, Verified: true,
		Roles: []string{wallet.OperatorRole},
	}
	customer := security.Subject{ID: "user-1", Tenant: tenant, Verified: true}

	// The service the module built, which is the one an application's routes
	// call. It charges nothing yet, because the wallet a fee is collected into
	// does not exist until the next few lines have run.
	opening := wallet.NewWalletService(handle, Rates{}, nil, nil)

	say("open", "a wallet is a balance in one currency, for one holder")
	buyer := must(opening.Open(ctx, staff, wallet.OpenRequest{
		HolderID: "user-1", Slug: "main", Name: "Ana", Currency: "BRL", DecimalPlaces: 2,
	}))
	friend := must(opening.Open(ctx, staff, wallet.OpenRequest{
		HolderID: "user-2", Slug: "main", Name: "Bruno", Currency: "BRL", DecimalPlaces: 2,
	}))
	shop := must(opening.Open(ctx, staff, wallet.OpenRequest{
		HolderID: "shop-1", Slug: "main", Name: "The shop", Currency: "BRL", DecimalPlaces: 2,
	}))
	collector := must(opening.Open(ctx, staff, wallet.OpenRequest{
		HolderID: "house", Slug: "fees", Name: "Fees", Currency: "BRL", DecimalPlaces: 2,
	}))
	savings := must(opening.Open(ctx, staff, wallet.OpenRequest{
		HolderID: "user-1", Slug: "usd", Name: "Ana in dollars", Currency: "USD", DecimalPlaces: 2,
	}))
	for _, record := range []*wallet.Wallet{buyer, friend, shop, collector, savings} {
		fmt.Printf("    %-16s %-8s %s\n", record.Name, record.Currency, record.ID[:8])
	}

	// The whole service, now that the fee has somewhere to go.
	svc := wallet.NewWalletService(handle, Rates{}, Fees{house: collector.ID}, Discounts{}, announce)

	// ---- money in and out --------------------------------------------------
	say("deposit", "money enters through an operation, and the ledger explains the balance")
	receipt := must(svc.Deposit(ctx, staff, wallet.DepositRequest{
		IdempotencyKey: "seed-1", WalletID: buyer.ID, Amount: "500.00",
		Meta: wallet.Meta{"channel": "pix", "invoice": "INV-1"},
	}))
	fmt.Printf("    %s, and the operation carries %v\n",
		receipt.Entries[0].BalanceAfter.Format(2), receipt.Operation.Meta)

	say("idempotency", "the same key twice moves money once")
	again := must(svc.Deposit(ctx, staff, wallet.DepositRequest{
		IdempotencyKey: "seed-1", WalletID: buyer.ID, Amount: "500.00",
	}))
	fmt.Printf("    replayed: %v, and the balance is still %s\n",
		again.Replayed, balance(ctx, svc, staff, buyer.ID))

	// ---- a payment that costs more than it moves ---------------------------
	say("transfer", "what leaves is what arrives plus the fee, exactly")
	paid := must(svc.Transfer(ctx, staff, wallet.TransferRequest{
		IdempotencyKey: "rent-1", FromWalletID: buyer.ID, ToWalletID: friend.ID, Amount: "100.00",
		Withdrawal: wallet.Leg{Meta: wallet.Meta{"statement": "Rent"}},
		Deposit:    wallet.Leg{Meta: wallet.Meta{"statement": "Rent from Ana"}},
	}))
	if paid.Charge != nil {
		fmt.Printf("    asked %s, discount %s, charged on %s, fee %s to %s\n",
			paid.Charge.Requested(),
			paid.Charge.Discount.Format(2),
			paid.Charge.Base(),
			paid.Charge.Fee(),
			paid.Charge.FeeWalletID[:8])
	}
	for _, entry := range paid.Entries {
		fmt.Printf("    %-8s %-10s %s\n", entry.Kind, entry.Amount.Format(2), entry.Meta["statement"])
	}

	// ---- a rate, recorded as it was applied --------------------------------
	say("exchange", "two wallets counted differently need a rate, and the row keeps it")
	converted := must(svc.Transfer(ctx, staff, wallet.TransferRequest{
		IdempotencyKey: "fx-1", FromWalletID: buyer.ID, ToWalletID: savings.ID, Amount: "10.00",
	}))
	if converted.Conversion != nil {
		fmt.Printf("    %s -> %s at %s, quoted %s, exact: %v\n",
			converted.Conversion.From(), converted.Conversion.To(),
			converted.Conversion.Rate(),
			converted.Conversion.QuotedAt.Format(time.RFC3339),
			converted.Conversion.Exact())
	}

	// ---- recorded now, counting later --------------------------------------
	say("hold", "one side of a payment can count while the other waits")
	held := must(svc.Transfer(ctx, staff, wallet.TransferRequest{
		IdempotencyKey: "escrow-1", FromWalletID: buyer.ID, ToWalletID: friend.ID, Amount: "20.00",
		Deposit: wallet.Leg{Pending: true},
	}))
	fmt.Printf("    Ana holds %s and Bruno holds %s\n",
		balance(ctx, svc, staff, buyer.ID), balance(ctx, svc, staff, friend.ID))
	must(svc.Confirm(ctx, staff, wallet.ConfirmRequest{
		IdempotencyKey: "escrow-confirm-1", OperationID: held.Operation.ID,
	}))
	fmt.Printf("    once confirmed, Bruno holds %s\n", balance(ctx, svc, staff, friend.ID))

	// ---- the basket --------------------------------------------------------
	say("buy", "a basket is one operation, one transaction, and a record of what it was")
	book := &Book{key: "the-go-book", wallet: shop.ID, price: 2500, stock: 10}
	pen := &Book{key: "a-pen", wallet: shop.ID, price: 300, stock: 2}

	bought := must(svc.Pay(ctx, customer, wallet.PayRequest{
		IdempotencyKey: "basket-1",
		PayerWalletID:  buyer.ID,
		Cart: wallet.NewCart(
			wallet.CartItem{Product: book, Quantity: 2, Meta: wallet.Meta{"gift_note": "for me"}},
			wallet.CartItem{Product: pen, BeneficiaryWalletID: friend.ID},
		).WithMeta(wallet.Meta{"order": "A-1"}),
	}))
	for _, line := range bought.Purchases {
		fmt.Printf("    %-14s x%d  %-8s paid %-9s fee %-8s -> %s\n",
			line.ProductKey, line.Quantity, line.Kind,
			line.PaidAmount.Format(2), line.FeeAmount.Format(2), line.ReceiverWalletID[:8])
	}

	say("stock", "the application answers, and it answers before any money moves")
	if _, err := svc.Pay(ctx, customer, wallet.PayRequest{
		IdempotencyKey: "basket-2", PayerWalletID: buyer.ID,
		Cart: wallet.NewCart(wallet.CartItem{Product: pen, Quantity: 99}),
	}); err != nil {
		fmt.Printf("    refused: %v\n", err)
	}

	say("already bought", "one statement for a whole page of questions")
	answers := must(svc.Bought(ctx, staff, []wallet.PurchaseQuery{
		{OwnerWalletID: buyer.ID, ReceiverWalletID: shop.ID, ProductKey: "the-go-book"},
		{OwnerWalletID: buyer.ID, ReceiverWalletID: shop.ID, ProductKey: "a-pen"},
		{OwnerWalletID: friend.ID, ReceiverWalletID: shop.ID, ProductKey: "a-pen", IncludeGifts: true},
	}))
	for i, found := range answers {
		fmt.Printf("    question %d: %v\n", i+1, found != nil)
	}

	// ---- giving one line back ----------------------------------------------
	say("refund", "a basket is undone line by line, and only what was undone comes back")
	before := balance(ctx, svc, staff, buyer.ID)
	must(svc.Refund(ctx, staff, wallet.RefundRequest{
		IdempotencyKey: "refund-1",
		PurchaseIDs:    []string{bought.Purchases[0].ID},
		Reason:         "sent back",
	}))
	fmt.Printf("    Ana held %s and now holds %s\n", before, balance(ctx, svc, staff, buyer.ID))

	after := must(svc.Bought(ctx, staff, []wallet.PurchaseQuery{
		{OwnerWalletID: buyer.ID, ReceiverWalletID: shop.ID, ProductKey: "the-go-book"},
	}))
	fmt.Printf("    still bought: %v\n", after[0] != nil)

	// ---- what the policy refuses -------------------------------------------
	say("refuse", "the holder may spend their own money and may not undo a settled movement")
	if _, err := svc.Reverse(ctx, customer, wallet.ReverseRequest{
		IdempotencyKey: "undo-1", OperationID: paid.Operation.ID, Reason: "changed my mind",
	}); err != nil {
		fmt.Printf("    reversing: %v\n", err)
	}
	stranger := security.Subject{ID: "user-9", Tenant: "globex", Verified: true}
	if _, err := svc.Find(ctx, stranger, buyer.ID); err != nil {
		fmt.Printf("    another customer reading it: %v\n", err)
	}

	// ---- does it add up ----------------------------------------------------
	say("audit", "the balance is a projection, so the entries have to explain it")
	for _, record := range []*wallet.Wallet{buyer, friend, shop, collector, savings} {
		report := must(svc.Reconcile(ctx, staff, record.ID))
		fmt.Printf("    %-16s balanced: %-6v ledger %-10s over %d movements\n",
			record.Name, report.Balanced(),
			report.Settled.Format(record.DecimalPlaces), report.Entries)
	}

	// ---- the words a screen says -------------------------------------------
	say("labels", "the catalogue answers, and the application's own is asked first")
	labels := module.Labels("pt-BR")
	fmt.Printf("    the statement's heading: %s\n", labels.T("screen.statement_title"))
	fmt.Printf("    an exchange, as a person reads it: %s\n", labels.Operation(wallet.OperationExchange))

	say("done", "nothing was left on disk")
	return nil
}

// announce is the listener this pretend application registers.
//
// It is called after the write has committed, in the goroutine that made it, so
// what it prints is what happened: a movement the database threw away is never
// announced, and there is no message that would take an announcement back.
func announce(_ context.Context, event wallet.Event) {
	if event.Kind != wallet.MoneyMoved {
		return
	}
	fmt.Printf("      [event] %s %s on %s, leaving %s\n",
		event.EntryKind, event.Amount.Format(event.DecimalPlaces),
		event.WalletID[:8], event.Balance.Format(event.DecimalPlaces))
}

// balance is what a wallet holds, as a person reads it.
func balance(ctx context.Context, svc *wallet.WalletService, actor security.Subject, id string) string {
	record, err := svc.Find(ctx, actor, id)
	if err != nil {
		return "unreadable"
	}
	return record.Balance.Format(record.DecimalPlaces) + " " + string(record.Currency)
}

// must is what an example does instead of handling an error it has no answer
// for. A real application returns them.
func must[T any](value T, err error) T {
	if err != nil {
		fmt.Fprintln(os.Stderr, "example:", err)
		os.Exit(1)
	}
	return value
}

// say heads a section, so the output reads as the story it is.
func say(step, what string) {
	fmt.Printf("\n== %s: %s\n", step, what)
}
