package feature_test

import (
	"context"
	"errors"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// Behavioural parity with bavix/laravel-wallet and bavix/laravel-wallet-swap.
//
// The parity table in AGENTS.md was written by reading both packages. This file
// is the same claim made executable: one test per behaviour the reference
// offers, exercised against this package's API. A row of that table going stale
// is a paragraph nobody notices; a test here failing is a build that stops.
//
// # What is deliberately not here
//
// The reference spells three variants of most operations -- safeX, X and
// forceX. safeX answers null instead of throwing, X throws, and forceX skips
// the balance check. The first two are one thing in Go: every method here
// returns an error, so the caller decides whether to stop by reading it rather
// than by choosing a method name. Porting the pair would be porting PHP's
// exception-or-return split, which this project's rules refuse by name. The
// third is here, as a field on the request rather than a second method, so
// every later rule is written in one place.
//
// Its float variants -- HasWalletFloat, CanPayFloat -- are not here and will not
// be. Money is an int64 of minor units, and a float is how a cent goes missing.

// TestParityDepositWithdrawTransfer covers HasWallet: deposit, withdraw,
// transfer, and the balance that answers afterwards.
func TestParityDepositWithdrawTransfer(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	payer := openWallet(t, service, "user-1", "payer", 2)
	payee := openWallet(t, service, "user-2", "payee", 2)

	deposit(t, service, payer.ID, "opening", "20.00")
	if got := balanceOf(t, service, payer.ID); got != 2000 {
		t.Fatalf("after a deposit the balance is %d, want 2000", got)
	}

	if err := withdraw(t, service, staff(), payer.ID, "w-1", "5.00", false); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}
	if got := balanceOf(t, service, payer.ID); got != 1500 {
		t.Fatalf("after a withdrawal the balance is %d, want 1500", got)
	}

	if _, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "t-1", FromWalletID: payer.ID, ToWalletID: payee.ID, Amount: "5.00",
	}); err != nil {
		t.Fatalf("transferring: %v", err)
	}
	if got := balanceOf(t, service, payer.ID); got != 1000 {
		t.Fatalf("the payer holds %d after a transfer, want 1000", got)
	}
	if got := balanceOf(t, service, payee.ID); got != 500 {
		t.Fatalf("the payee holds %d after a transfer, want 500", got)
	}
}

// TestParityForceWithdrawGoesPastTheLimit covers forceWithdraw and
// forceTransfer, which are a field here rather than two more methods.
func TestParityForceWithdrawGoesPastTheLimit(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	if err := withdraw(t, service, staff(), account.ID, "refused", "5.00", false); err == nil {
		t.Fatal("an empty wallet paid out without force")
	}
	if err := withdraw(t, service, staff(), account.ID, "forced", "5.00", true); err != nil {
		t.Fatalf("a forced withdrawal was refused: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != -500 {
		t.Fatalf("after a forced withdrawal the balance is %d, want -500", got)
	}
}

// TestParityInsufficientFundsAndEmptyBalance covers InsufficientFunds and
// BalanceIsEmpty, which the reference keeps apart and so does this package.
func TestParityInsufficientFundsAndEmptyBalance(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	err := withdraw(t, service, staff(), account.ID, "on-empty", "1.00", false)
	if !errors.Is(err, wallet.ErrBalanceEmpty) {
		t.Errorf("a withdrawal from an empty wallet answers %v, want ErrBalanceEmpty", err)
	}
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Errorf("it also has to read as ErrInsufficientFunds, so an existing caller is unaffected: %v", err)
	}

	deposit(t, service, account.ID, "opening", "1.00")
	err = withdraw(t, service, staff(), account.ID, "too-much", "5.00", false)
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Errorf("a withdrawal past the balance answers %v, want ErrInsufficientFunds", err)
	}
	if errors.Is(err, wallet.ErrBalanceEmpty) {
		t.Error("a wallet that holds something is not empty, and the two are different things to act on")
	}
}

// TestParityConfirmAndItsRefusals covers CanConfirm: a movement recorded
// without counting, then made to count, and the two refusals around it.
func TestParityConfirmAndItsRefusals(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	receipt := pend(t, service, account.ID, "pending", "10.00")
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("a pending deposit moved the balance to %d, want 0", got)
	}

	if _, err := confirm(t, service, staff(), receipt.Operation.ID, "c-1"); err != nil {
		t.Fatalf("confirming: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 1000 {
		t.Fatalf("after a confirmation the balance is %d, want 1000", got)
	}

	if _, err := confirm(t, service, staff(), receipt.Operation.ID, "c-2"); !errors.Is(err, wallet.ErrAlreadyConfirmed) {
		t.Errorf("confirming twice answers %v, want ErrAlreadyConfirmed", err)
	}

	settled := deposit(t, service, account.ID, "settled", "1.00")
	if _, err := confirm(t, service, staff(), settled.Operation.ID, "c-3"); !errors.Is(err, wallet.ErrNotPending) {
		t.Errorf("confirming what already counted answers %v, want ErrNotPending", err)
	}
}

// TestParityAConfirmationIsUndoneByReversingIt is where this package answers
// resetConfirm, and it answers it differently on purpose.
//
// The reference turns a confirmed transaction back into an unconfirmed one, in
// place. The ledger here is appended to and never rewritten, so what undoes a
// confirmation is a reversal of it: the money goes back, the confirmation stays
// readable, and what happened is two rows instead of one row edited twice.
//
// The behaviour a caller depends on -- the balance returns -- is the same. What
// differs is that the movement does not become pending again, because a history
// that can be walked backwards is the property this package keeps instead.
func TestParityAConfirmationIsUndoneByReversingIt(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "20.00")

	proposed := pend(t, service, account.ID, "pending", "10.00")
	confirmed, err := confirm(t, service, staff(), proposed.Operation.ID, "c-1")
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}
	before := balanceOf(t, service, account.ID)

	if _, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
		IdempotencyKey: "undo", OperationID: confirmed.Operation.ID,
		Reason: "the confirmation was a mistake",
	}); err != nil {
		t.Fatalf("reversing a confirmation: %v", err)
	}

	if got, want := balanceOf(t, service, account.ID), before-1000; got != want {
		t.Errorf("after undoing a confirmation the balance is %d, want %d", got, want)
	}
	if ledger := ledgerOf(t, service, account.ID); ledger != balanceOf(t, service, account.ID) {
		t.Errorf("the ledger sums to %d and the balance column disagrees", ledger)
	}
}

// TestParityExchangeAcrossCurrencies covers CanExchange, and the difference
// this package makes at exactly that point.
//
// The reference credits floor(amount x rate) and never applies
// 10^(to_dp - from_dp), so a conversion between two scales is wrong by that
// factor -- and its default ExchangeService returns the amount unchanged, which
// is a rate of one until the swap package is installed. Here the scale is part
// of the arithmetic and the rate is recorded.
func TestParityExchangeAcrossCurrencies(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), &fixedRate{numerator: 1, denominator: 2}, nil, nil)
	from := openIn(t, service, "user-1", "brl", "BRL", 2)
	to := openIn(t, service, "user-2", "usd", "USD", 8)
	deposit(t, service, from.ID, "opening", "100.00")

	receipt, err := service.Transfer(context.Background(), staff(), wallet.TransferRequest{
		IdempotencyKey: "x-1", FromWalletID: from.ID, ToWalletID: to.ID, Amount: "10.00",
	})
	if err != nil {
		t.Fatalf("exchanging: %v", err)
	}
	if receipt.Conversion == nil {
		t.Fatal("an exchange left no conversion row, so the rate it settled at is not recoverable")
	}
	if receipt.Operation.Kind != wallet.OperationExchange {
		t.Errorf("the operation is recorded as %q, want an exchange", receipt.Operation.Kind)
	}
	assertConversionReproduces(t, *receipt.Conversion)

	// Two scales apart: ten units at one half, into a wallet counted to eight
	// places, is 5 whole units and therefore 500000000 minor ones. Without the
	// scale factor it would be 500.
	if got, want := balanceOf(t, service, to.ID), wallet.Amount(500000000); got != want {
		t.Errorf("the exchange credited %d, want %d: the scale difference was not applied", got, want)
	}
}

// TestParityCartPayAndRefundByLine covers CartPay and CanPay: a basket paid for
// in one operation, and a line given back.
func TestParityCartPayAndRefundByLine(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	payer := openWallet(t, service, "user-1", "payer", 2)
	seller := openWallet(t, service, "shop", "till", 2)
	deposit(t, service, payer.ID, "opening", "100.00")

	book := &item{key: "book", wallet: seller.ID, price: 1000}
	receipt, err := service.Pay(context.Background(), staff(), wallet.PayRequest{
		IdempotencyKey: "cart-1",
		PayerWalletID:  payer.ID,
		Cart:           wallet.NewCart(wallet.CartItem{Product: book, Quantity: 1}),
	})
	if err != nil {
		t.Fatalf("paying for a basket: %v", err)
	}
	if len(receipt.Purchases) != 1 {
		t.Fatalf("a basket of one line recorded %d purchases", len(receipt.Purchases))
	}
	if got := balanceOf(t, service, payer.ID); got != 9000 {
		t.Fatalf("the payer holds %d after paying 10.00, want 9000", got)
	}

	if _, err := service.Refund(context.Background(), staff(), wallet.RefundRequest{
		IdempotencyKey: "refund-1", PurchaseIDs: []string{receipt.Purchases[0].ID}, Reason: "returned",
	}); err != nil {
		t.Fatalf("refunding a line: %v", err)
	}
	if got := balanceOf(t, service, payer.ID); got != 10000 {
		t.Fatalf("the payer holds %d after the refund, want 10000", got)
	}

	if _, err := service.Refund(context.Background(), staff(), wallet.RefundRequest{
		IdempotencyKey: "refund-2", PurchaseIDs: []string{receipt.Purchases[0].ID}, Reason: "returned again",
	}); !errors.Is(err, wallet.ErrAlreadyRefunded) {
		t.Errorf("refunding the same line twice answers %v, want ErrAlreadyRefunded", err)
	}
}

// TestParityWalletsPerHolder covers HasWallets: several wallets for one holder,
// reached by the name their holder knows them by.
func TestParityWalletsPerHolder(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	first := openWallet(t, service, "user-1", "main", 2)
	second := openWallet(t, service, "user-1", "savings", 2)

	found, err := service.FindBySlug(context.Background(), staff(), "user-1", "savings")
	if err != nil {
		t.Fatalf("finding a wallet by its slug: %v", err)
	}
	if found.ID != second.ID {
		t.Errorf("the slug reached %s, want %s", found.ID, second.ID)
	}

	held, err := service.List(context.Background(), staff(), wallet.ListRequest{HolderID: "user-1"})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(held) != 2 {
		t.Errorf("the holder has %d wallets, want 2", len(held))
	}

	if _, err := service.Open(context.Background(), staff(), wallet.OpenRequest{
		HolderID: "user-1", Slug: "main", Name: "again", Currency: "BRL", DecimalPlaces: 2,
	}); !errors.Is(err, wallet.ErrWalletExists) {
		t.Errorf("opening a second wallet under one slug answers %v, want ErrWalletExists", err)
	}
	_ = first
}

// TestParityCanWithdrawAsksWithoutMoving covers HasWallet::canWithdraw.
//
// It answers the same arithmetic the guard does, and it is a photograph: the
// test that matters is not that it says yes, but that saying yes is not what
// lets the money move. The withdrawal it approves is still refused if the
// balance left in between.
func TestParityCanWithdrawAsksWithoutMoving(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)

	if can, err := service.CanWithdraw(ctx, staff(), account.ID, "5.00"); err != nil || can {
		t.Errorf("an empty wallet says it can pay out 5.00 (err=%v)", err)
	}

	deposit(t, service, account.ID, "opening", "20.00")
	if can, err := service.CanWithdraw(ctx, staff(), account.ID, "5.00"); err != nil || !can {
		t.Errorf("a wallet holding 20.00 says it cannot pay out 5.00 (err=%v)", err)
	}
	if can, err := service.CanWithdraw(ctx, staff(), account.ID, "50.00"); err != nil || can {
		t.Errorf("a wallet holding 20.00 says it can pay out 50.00 (err=%v)", err)
	}
	if got := balanceOf(t, service, account.ID); got != 2000 {
		t.Errorf("asking moved the balance to %d: it is a question and not a withdrawal", got)
	}

	// The credit limit counts, exactly as it does in the guard.
	creditLimit(t, service, account.ID, "10.00")
	if can, err := service.CanWithdraw(ctx, staff(), account.ID, "30.00"); err != nil || !can {
		t.Errorf("20.00 with 10.00 of credit says it cannot pay out 30.00 (err=%v)", err)
	}
	if can, err := service.CanWithdraw(ctx, staff(), account.ID, "30.01"); err != nil || can {
		t.Errorf("it says it can pay out one cent past its limit (err=%v)", err)
	}

	// And the two reasons a wallet is out of service.
	if _, err := service.Close(ctx, staff(), wallet.CloseRequest{WalletID: account.ID}); err == nil {
		t.Fatal("a wallet holding money was closed")
	}
}

// TestParityAGiftIsRecordedAsOne covers HasGift and TransferStatus::Gift: a line
// bought for somebody else, distinguishable in the record afterwards.
func TestParityAGiftIsRecordedAsOne(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	payer := openWallet(t, service, "user-1", "payer", 2)
	friend := openWallet(t, service, "user-2", "friend", 2)
	seller := openWallet(t, service, "shop", "till", 2)
	deposit(t, service, payer.ID, "opening", "100.00")

	book := &item{key: "book", wallet: seller.ID, price: 1000}
	receipt, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "gift-1",
		PayerWalletID:  payer.ID,
		Cart: wallet.NewCart(wallet.CartItem{
			Product:             book,
			Quantity:            1,
			BeneficiaryWalletID: friend.ID,
		}),
	})
	if err != nil {
		t.Fatalf("buying a line for somebody else: %v", err)
	}
	if len(receipt.Purchases) != 1 {
		t.Fatalf("a basket of one line recorded %d purchases", len(receipt.Purchases))
	}
	if got := receipt.Purchases[0].Kind; got != wallet.PurchaseGift {
		t.Errorf("the line is recorded as %q, want a gift: what it was is what the record has to say", got)
	}

	// The money still leaves the payer, and the friend holds the purchase.
	if got := balanceOf(t, service, payer.ID); got != 9000 {
		t.Errorf("the payer holds %d, want 9000: a gift is paid for by whoever gave it", got)
	}
	if got := balanceOf(t, service, friend.ID); got != 0 {
		t.Errorf("the beneficiary holds %d, want 0: a gift is a purchase and not a transfer", got)
	}
}

// TestParityAFreeLineIsBought covers CanPay::payFree and CartPay::payFreeCart.
func TestParityAFreeLineIsBought(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	payer := openWallet(t, service, "user-1", "payer", 2)
	seller := openWallet(t, service, "shop", "till", 2)

	free := &item{key: "sample", wallet: seller.ID, price: 0}
	receipt, err := service.Pay(ctx, staff(), wallet.PayRequest{
		IdempotencyKey: "free-1",
		PayerWalletID:  payer.ID,
		Cart:           wallet.NewCart(wallet.CartItem{Product: free, Quantity: 1}),
	})
	if err != nil {
		t.Fatalf("buying a line priced at nothing: %v", err)
	}
	if len(receipt.Purchases) != 1 || !receipt.Purchases[0].Free() {
		t.Errorf("a line priced at zero was not recorded as free")
	}
	if len(receipt.Entries) != 0 {
		t.Errorf("a free line wrote %d ledger entries, and it moves no money", len(receipt.Entries))
	}
	if got := balanceOf(t, service, payer.ID); got != 0 {
		t.Errorf("a free line moved the balance to %d", got)
	}
}

// TestCanWithdrawIsNotPermission holds the sentence in its doc comment.
//
// A caller that asks and then withdraws is racing, and the race is won by the
// statement rather than by the answer: here the wallet is emptied between the
// two, and the withdrawal that was approved a moment earlier is refused.
//
// It is the test that keeps CanWithdraw from becoming the read-then-check this
// package exists to avoid. If it ever passes because the withdrawal succeeded,
// the guard was removed and something else decided.
func TestCanWithdrawIsNotPermission(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "opening", "10.00")

	can, err := service.CanWithdraw(ctx, staff(), account.ID, "10.00")
	if err != nil || !can {
		t.Fatalf("a wallet holding 10.00 says it cannot pay out 10.00 (err=%v)", err)
	}

	// Somebody else spends it in between.
	if err := withdraw(t, service, staff(), account.ID, "somebody-else", "10.00", false); err != nil {
		t.Fatalf("the first withdrawal: %v", err)
	}

	err = withdraw(t, service, staff(), account.ID, "the-approved-one", "10.00", false)
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("the withdrawal CanWithdraw approved was allowed through: %v", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d, want 0: the money moved twice", got)
	}
}
