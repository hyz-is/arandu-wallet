package feature_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/filesystem"

	wallet "github.com/hyz-is/arandu-wallet"
)

// payouts is the service with a Disk for the receipts, over a fresh database.
func payouts(t *testing.T) (*wallet.WalletService, *data.DB) {
	t.Helper()
	db := database(t)
	adapter, err := filesystem.NewLocalFilesystemAdapter(t.TempDir())
	if err != nil {
		t.Fatalf("a Disk for the receipts: %v", err)
	}
	module, err := wallet.New(wallet.Config{Tenant: tenant, Receipts: filesystem.NewDisk("receipts", adapter)}, db, sessionStore())
	if err != nil {
		t.Fatalf("building the module: %v", err)
	}
	return module.Service(), db
}

// earnings opens a wallet that pays out only by request, with R$ 1.000,00.
func earnings(t *testing.T, svc *wallet.WalletService, holder string) *wallet.Wallet {
	t.Helper()
	w, err := svc.Open(context.Background(), staff(), wallet.OpenRequest{
		HolderID: holder, Slug: "earnings", Name: "Ganhos", Currency: "BRL", DecimalPlaces: 2, PayoutByRequest: true,
	})
	if err != nil {
		t.Fatalf("opening the wallet: %v", err)
	}
	deposit(t, svc, w.ID, "credit-"+holder, "1000.00")
	return w
}

func pixAccount(t *testing.T, svc *wallet.WalletService, holder string) *wallet.PayoutAccount {
	t.Helper()
	a, err := svc.AddPayoutAccount(context.Background(), person(holder), wallet.PayoutAccountRequest{
		HolderID: holder, Kind: "pix", Label: "Minha chave", Details: wallet.Meta{"key": holder + "@example.com", "key_type": "email"},
		HolderName: "Ana", HolderDocument: "52998224725",
	})
	if err != nil {
		t.Fatalf("declaring the account: %v", err)
	}
	return a
}

func walletNow(t *testing.T, svc *wallet.WalletService, id string) *wallet.Wallet {
	t.Helper()
	w, err := svc.Find(context.Background(), staff(), id)
	if err != nil {
		t.Fatalf("reading the wallet: %v", err)
	}
	return w
}

func ask(t *testing.T, svc *wallet.WalletService, holder, walletID, accountID, key, amount string) *wallet.WithdrawalRequest {
	t.Helper()
	r, err := svc.RequestWithdrawal(context.Background(), person(holder), wallet.WithdrawalRequestInput{
		IdempotencyKey: key, WalletID: walletID, Amount: amount, AccountID: accountID, Reference: "invoice:1",
	})
	if err != nil {
		t.Fatalf("asking for %s: %v", amount, err)
	}
	return r
}

func TestAWalletThatPaysOutByRequestRefusesTheHoldersOtherWaysOut(t *testing.T) {
	t.Parallel()
	svc, _ := payouts(t)
	ctx := context.Background()
	w := earnings(t, svc, "ana")

	_, err := svc.Withdraw(ctx, person("ana"), wallet.WithdrawRequest{IdempotencyKey: "direct", WalletID: w.ID, Amount: "10.00"})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the holder withdrew directly from a payout-by-request wallet: %v", err)
	}
	if _, err := svc.Hold(ctx, person("ana"), wallet.HoldRequest{IdempotencyKey: "h", WalletID: w.ID, Amount: "10.00"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the holder held money of their own: %v", err)
	}
	// The operator keeps every way, which is how a mistake is corrected.
	if _, err := svc.Withdraw(ctx, staff(), wallet.WithdrawRequest{IdempotencyKey: "fix", WalletID: w.ID, Amount: "1.00"}); err != nil {
		t.Fatalf("the operator was refused a withdrawal: %v", err)
	}
}

func TestARequestHoldsTheAmountAndIsMadeOnce(t *testing.T) {
	t.Parallel()
	svc, _ := payouts(t)
	w := earnings(t, svc, "ana")
	account := pixAccount(t, svc, "ana")

	r := ask(t, svc, "ana", w.ID, account.ID, "req-1", "250.00")
	if r.Status != wallet.WithdrawalRequested || r.AccountSnapshot["details.key"] != "ana@example.com" {
		t.Fatalf("the request is %s with snapshot %v", r.Status, r.AccountSnapshot)
	}
	after := walletNow(t, svc, w.ID)
	if after.Balance != 100000 || after.Held != 25000 {
		t.Fatalf("balance %d held %d; want 100000 and 25000", after.Balance, after.Held)
	}

	if again := ask(t, svc, "ana", w.ID, account.ID, "req-1", "250.00"); again.ID != r.ID {
		t.Fatalf("asking again made a second request %s", again.ID)
	}
	_, err := svc.RequestWithdrawal(context.Background(), person("ana"), wallet.WithdrawalRequestInput{
		IdempotencyKey: "req-1", WalletID: w.ID, Amount: "300.00", AccountID: account.ID,
	})
	if !errors.Is(err, wallet.ErrOperationConflict) {
		t.Fatalf("the same key for another amount answered %v, want a conflict", err)
	}
	_, err = svc.RequestWithdrawal(context.Background(), person("ana"), wallet.WithdrawalRequestInput{
		IdempotencyKey: "req-2", WalletID: w.ID, Amount: "800.00", AccountID: account.ID,
	})
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("asking for more than is free answered %v, want insufficient funds", err)
	}
}

func TestPayingCapturesTheHoldWithItsReceiptOnce(t *testing.T) {
	t.Parallel()
	svc, _ := payouts(t)
	ctx := context.Background()
	w := earnings(t, svc, "ana")
	r := ask(t, svc, "ana", w.ID, pixAccount(t, svc, "ana").ID, "req-1", "250.00")

	if _, err := svc.StartProcessing(ctx, staff(), r.ID); err != nil {
		t.Fatalf("taking the request: %v", err)
	}
	if _, err := svc.CancelWithdrawal(ctx, person("ana"), r.ID); !errors.Is(err, wallet.ErrWithdrawalProcessing) {
		t.Fatalf("the holder cancelled a request being paid: %v", err)
	}

	pay := wallet.PaymentInput{RequestID: r.ID, EndToEndID: "E0000000020261013", ReceiptName: "comprovante.pdf", ReceiptType: "application/pdf"}
	if _, err := svc.PayWithdrawal(ctx, staff(), pay, nil); err == nil {
		t.Fatal("a payment with no receipt was accepted")
	}
	paid, err := svc.PayWithdrawal(ctx, staff(), pay, strings.NewReader("%PDF pix"))
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	if paid.Status != wallet.WithdrawalPaid || paid.OperationID == "" || paid.ClosedBy != staff().ID || paid.ClosedAt == nil {
		t.Fatalf("paid request: %s op %q by %q at %v", paid.Status, paid.OperationID, paid.ClosedBy, paid.ClosedAt)
	}
	after := walletNow(t, svc, w.ID)
	if after.Balance != 75000 || after.Held != 0 {
		t.Fatalf("balance %d held %d after paying; want 75000 and 0", after.Balance, after.Held)
	}

	// Paying again is the same payment.
	if again, err := svc.PayWithdrawal(ctx, staff(), pay, strings.NewReader("%PDF pix")); err != nil || again.OperationID != paid.OperationID {
		t.Fatalf("paying again answered %v, %v", again, err)
	}
	if b := walletNow(t, svc, w.ID).Balance; b != 75000 {
		t.Fatalf("paying twice left %d", b)
	}

	// The holder reads the receipt; another holder does not.
	_, file, err := svc.WithdrawalReceipt(ctx, person("ana"), r.ID)
	if err != nil {
		t.Fatalf("Ana reading her receipt: %v", err)
	}
	body, _ := io.ReadAll(file.Body)
	_ = file.Body.Close()
	if string(body) != "%PDF pix" {
		t.Fatalf("the receipt reads %q", body)
	}
	if _, _, err := svc.WithdrawalReceipt(ctx, person("bruno"), r.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("another holder read Ana's receipt: %v", err)
	}
}

func TestRejectingOrCancellingGivesTheAmountBack(t *testing.T) {
	t.Parallel()
	svc, _ := payouts(t)
	ctx := context.Background()
	w := earnings(t, svc, "ana")
	account := pixAccount(t, svc, "ana")

	rejected := ask(t, svc, "ana", w.ID, account.ID, "req-1", "100.00")
	if _, err := svc.RejectWithdrawal(ctx, staff(), rejected.ID, ""); err == nil {
		t.Fatal("a rejection with no reason was accepted")
	}
	r, err := svc.RejectWithdrawal(ctx, staff(), rejected.ID, "chave PIX não confere com o titular")
	if err != nil || r.Status != wallet.WithdrawalRejected || r.RejectedReason == "" {
		t.Fatalf("rejecting: %v, %v", r, err)
	}

	cancelled := ask(t, svc, "ana", w.ID, account.ID, "req-2", "200.00")
	if _, err := svc.CancelWithdrawal(ctx, person("bruno"), cancelled.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("somebody else cancelled Ana's request: %v", err)
	}
	c, err := svc.CancelWithdrawal(ctx, person("ana"), cancelled.ID)
	if err != nil || c.Status != wallet.WithdrawalCancelled {
		t.Fatalf("cancelling: %v, %v", c, err)
	}
	if after := walletNow(t, svc, w.ID); after.Balance != 100000 || after.Held != 0 {
		t.Fatalf("balance %d held %d; want everything back", after.Balance, after.Held)
	}
	if _, err := svc.PayWithdrawal(ctx, staff(), wallet.PaymentInput{RequestID: cancelled.ID, EndToEndID: "E1",
		ReceiptName: "c.pdf", ReceiptType: "application/pdf"}, strings.NewReader("%PDF")); !errors.Is(err, wallet.ErrWithdrawalClosed) {
		t.Fatalf("a cancelled request was paid: %v", err)
	}
}

func TestOnlyTheHoldersOwnUsableAccountTakesARequest(t *testing.T) {
	t.Parallel()
	svc, _ := payouts(t)
	ctx := context.Background()
	w := earnings(t, svc, "ana")
	brunos := pixAccount(t, svc, "bruno")
	_, err := svc.RequestWithdrawal(ctx, person("ana"), wallet.WithdrawalRequestInput{
		IdempotencyKey: "req-1", WalletID: w.ID, Amount: "10.00", AccountID: brunos.ID,
	})
	if !errors.Is(err, wallet.ErrAccountUnusable) {
		t.Fatalf("Ana asked to be paid into Bruno's account: %v", err)
	}
	mine := pixAccount(t, svc, "ana")
	if _, err := svc.DisablePayoutAccount(ctx, person("ana"), mine.ID); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	_, err = svc.RequestWithdrawal(ctx, person("ana"), wallet.WithdrawalRequestInput{
		IdempotencyKey: "req-2", WalletID: w.ID, Amount: "10.00", AccountID: mine.ID,
	})
	if !errors.Is(err, wallet.ErrAccountUnusable) {
		t.Fatalf("a disabled account took a request: %v", err)
	}
	if _, err := svc.AddPayoutAccount(ctx, person("ana"), wallet.PayoutAccountRequest{
		HolderID: "bruno", Kind: "pix", Label: "x", Details: wallet.Meta{"key": "k"}, HolderName: "B", HolderDocument: "1",
	}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Ana declared an account for Bruno: %v", err)
	}
}

func TestAHolderListsOnlyTheirOwnRequests(t *testing.T) {
	t.Parallel()
	svc, _ := payouts(t)
	ctx := context.Background()
	wa, wb := earnings(t, svc, "ana"), earnings(t, svc, "bruno")
	ask(t, svc, "ana", wa.ID, pixAccount(t, svc, "ana").ID, "a-1", "10.00")
	ask(t, svc, "bruno", wb.ID, pixAccount(t, svc, "bruno").ID, "b-1", "20.00")

	mine, err := svc.WithdrawalRequestsOf(ctx, person("ana"), wallet.WithdrawalFilter{HolderID: "bruno"})
	if err != nil || len(mine) != 1 || mine[0].HolderID != "ana" {
		t.Fatalf("Ana's listing: %d rows, %v", len(mine), err)
	}
	queue, err := svc.WithdrawalRequestsOf(ctx, staff(), wallet.WithdrawalFilter{Status: wallet.WithdrawalRequested})
	if err != nil || len(queue) != 2 {
		t.Fatalf("the operator's queue: %d rows, %v; want both", len(queue), err)
	}
}
