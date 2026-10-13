package feature_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A credit agreement is a purchase paid in installments against the limit a
// creditor approved: the total is drawn when it opens, so the balance is the
// debt, and every installment paid is deposited. These tests hold the schedule,
// the limit, lateness, the sweep, a payoff, a cancellation and who may do each.

func day(t *testing.T, text string) time.Time {
	t.Helper()
	at, err := time.Parse(time.DateOnly, text)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// noon is midday of a date in São Paulo, well inside the date there.
func noon(t *testing.T, text string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation(time.DateOnly, text, loc)
	if err != nil {
		t.Fatal(err)
	}
	return at.Add(12 * time.Hour)
}

func openAgreement(t *testing.T, service *wallet.WalletService, walletID, key, principal string, count, interestBP int) wallet.CreditStatement {
	t.Helper()
	statement, err := service.OpenCredit(context.Background(), staff(), wallet.CreditOpenRequest{
		IdempotencyKey: key, WalletID: walletID, Reference: "order-" + key,
		Principal: principal, Installments: count, FirstDue: "2026-11-10", TimeZone: "America/Sao_Paulo",
		InterestRateBP: interestBP, LateFeeBP: 200, MoraRateBP: 100, DelinquentAfterDays: 30,
		Terms:      wallet.Meta{"text": "Compra a prazo em " + principal},
		AcceptedAt: day(t, "2026-10-10"), AcceptedFrom: "203.0.113.7",
	})
	if err != nil {
		t.Fatalf("opening the agreement: %v", err)
	}
	return statement
}

func TestAScheduleWithoutInterestSplitsThePrincipalToTheCent(t *testing.T) {
	t.Parallel()
	got, err := wallet.Schedule(1000, 3, 0, day(t, "2026-01-31"))
	if err != nil {
		t.Fatal(err)
	}
	amounts := []wallet.Amount{got[0].Amount, got[1].Amount, got[2].Amount}
	if amounts[0] != 334 || amounts[1] != 333 || amounts[2] != 333 {
		t.Fatalf("installments %v, want 334 333 333", amounts)
	}
	// The 31st has no February: the last day of the month stands in.
	if got[1].DueOn.Format(time.DateOnly) != "2026-02-28" || got[2].DueOn.Format(time.DateOnly) != "2026-03-31" {
		t.Fatalf("due dates %s %s", got[1].DueOn.Format(time.DateOnly), got[2].DueOn.Format(time.DateOnly))
	}
}

func TestAScheduleWithInterestFollowsThePriceTableAndClosesThePrincipal(t *testing.T) {
	t.Parallel()
	// R$ 1.000,00 in 12 at 1% a month: the Price table's installment is
	// R$ 88,85, and the principal parts add up to exactly the principal.
	got, err := wallet.Schedule(100000, 12, 100, day(t, "2026-11-10"))
	if err != nil {
		t.Fatal(err)
	}
	var principal, total wallet.Amount
	for _, item := range got {
		principal += item.PrincipalPart
		total += item.Amount
		if item.Amount != item.PrincipalPart+item.InterestPart {
			t.Fatalf("installment %d is not its parts", item.Sequence)
		}
	}
	if got[0].Amount != 8885 || got[0].InterestPart != 1000 {
		t.Fatalf("first installment %d (interest %d), want 8885 (1000)", got[0].Amount, got[0].InterestPart)
	}
	if principal != 100000 {
		t.Fatalf("the principal parts add up to %d, want 100000", principal)
	}
	if total < 106600 || total > 106640 {
		t.Fatalf("the total is %d, want about 106620", total)
	}
}

func TestAnAgreementSpendsTheLimitAndIsRefusedBeyondIt(t *testing.T) {
	t.Parallel()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "buyer-1", "credit", 2)
	creditLimit(t, service, account.ID, "500.00")

	statement := openAgreement(t, service, account.ID, "sale-1", "300.00", 3, 0)
	if statement.Agreement.Total != 30000 || len(statement.Installments) != 3 || statement.Agreement.Status != wallet.CreditActive {
		t.Fatalf("agreement %+v with %d installments", statement.Agreement, len(statement.Installments))
	}
	if got := balanceOf(t, service, account.ID); got != -30000 {
		t.Fatalf("the balance is %d, want -30000: the debt", got)
	}
	// The same key opens nothing new.
	again := openAgreement(t, service, account.ID, "sale-1", "300.00", 3, 0)
	if again.Agreement.ID != statement.Agreement.ID || balanceOf(t, service, account.ID) != -30000 {
		t.Fatal("opening twice with one key opened twice")
	}
	// R$ 300 owed leaves R$ 200 of limit: R$ 250 more is refused.
	_, err := service.OpenCredit(context.Background(), staff(), wallet.CreditOpenRequest{
		IdempotencyKey: "sale-2", WalletID: account.ID, Principal: "250.00", Installments: 2, FirstDue: "2026-11-10",
		Terms: wallet.Meta{"text": "x"}, AcceptedAt: day(t, "2026-10-10"),
	})
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("an agreement beyond the limit answered %v, want ErrInsufficientFunds", err)
	}
	// A late fee above 2% is not the creditor's to ask.
	_, err = service.OpenCredit(context.Background(), staff(), wallet.CreditOpenRequest{
		IdempotencyKey: "sale-3", WalletID: account.ID, Principal: "10.00", Installments: 1, FirstDue: "2026-11-10",
		LateFeeBP: 500, Terms: wallet.Meta{"text": "x"}, AcceptedAt: day(t, "2026-10-10"),
	})
	if err == nil {
		t.Fatal("a late fee of 5% was accepted")
	}
}

func TestPayingOnTimeAndLateChargesWhatTheDayCosts(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var told []wallet.EventKind
	listen := func(_ context.Context, e wallet.Event) { mu.Lock(); told = append(told, e.Kind); mu.Unlock() }
	service := wallet.NewWalletService(database(t), nil, nil, nil, listen)
	account := openWallet(t, service, "buyer-2", "credit", 2)
	creditLimit(t, service, account.ID, "1000.00")
	statement := openAgreement(t, service, account.ID, "sale-4", "300.00", 3, 0)
	ctx := context.Background()

	// On its date, the installment is its amount.
	if _, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
		IdempotencyKey: "pay-1", AgreementID: statement.Agreement.ID, Sequence: 1, Paid: "100.00", At: noon(t, "2026-11-10"),
	}); err != nil {
		t.Fatalf("paying on the date: %v", err)
	}
	// Ten days after its date: 2% once and 1% a month by the day.
	late := noon(t, "2026-12-20")
	if _, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
		IdempotencyKey: "pay-2", AgreementID: statement.Agreement.ID, Sequence: 2, Paid: "100.00", At: late,
	}); !errors.Is(err, wallet.ErrUnderpaid) {
		t.Fatalf("paying a late installment without its charges answered %v, want ErrUnderpaid", err)
	}
	quote := wallet.QuoteInstallment(statement.Agreement, statement.Installments[1], late)
	if quote.DaysLate != 10 || quote.LateFee != 200 || quote.Mora != 33 || quote.Total != 10233 {
		t.Fatalf("quote %+v, want 10 days, fee 200, mora 33, total 10233", quote)
	}
	paid, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
		IdempotencyKey: "pay-2", AgreementID: statement.Agreement.ID, Sequence: 2, Paid: "102.33", At: late,
	})
	if err != nil {
		t.Fatalf("paying late with the charges: %v", err)
	}
	if got := paid.Installments[1]; got.Status != wallet.InstallmentPaid || got.LateFee != 200 || got.Mora != 33 || got.PaidAmount != 10233 {
		t.Fatalf("the late installment reads %+v", got)
	}
	// The debt fell by the installments, not by the charges.
	if got := balanceOf(t, service, account.ID); got != -10000 {
		t.Fatalf("the balance is %d, want -10000", got)
	}
	// Paying again with the same key records nothing new.
	if _, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
		IdempotencyKey: "pay-2", AgreementID: statement.Agreement.ID, Sequence: 2, Paid: "102.33", At: late,
	}); err != nil || balanceOf(t, service, account.ID) != -10000 {
		t.Fatalf("a replayed payment answered %v and moved the balance", err)
	}
	// The last one settles the agreement.
	settled, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
		IdempotencyKey: "pay-3", AgreementID: statement.Agreement.ID, Sequence: 3, Paid: "100.00", At: noon(t, "2027-01-05"),
	})
	if err != nil || settled.Agreement.Status != wallet.CreditSettled || balanceOf(t, service, account.ID) != 0 {
		t.Fatalf("paying the last answered %v with the agreement %s", err, settled.Agreement.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if !containsKind(told, wallet.CreditOpened) || !containsKind(told, wallet.CreditInstallmentPaid) || !containsKind(told, wallet.CreditWasSettled) {
		t.Fatalf("the listeners were told %v", told)
	}
}

func containsKind(kinds []wallet.EventKind, want wallet.EventKind) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

func TestTheSweepMarksOverdueThenDelinquentOnceAndPayingRegularizes(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var told []wallet.EventKind
	listen := func(_ context.Context, e wallet.Event) { mu.Lock(); told = append(told, e.Kind); mu.Unlock() }
	service := wallet.NewWalletService(database(t), nil, nil, nil, listen)
	account := openWallet(t, service, "buyer-3", "credit", 2)
	creditLimit(t, service, account.ID, "1000.00")
	statement := openAgreement(t, service, account.ID, "sale-5", "200.00", 2, 0)
	ctx := context.Background()

	// On its date it is not overdue.
	if got, err := service.SweepCredit(ctx, staff(), noon(t, "2026-11-10")); err != nil || got.Overdue != 0 {
		t.Fatalf("sweeping on the date found %+v, %v", got, err)
	}
	// The next day it is, once.
	if got, _ := service.SweepCredit(ctx, staff(), noon(t, "2026-11-11")); got.Overdue != 1 || got.Delinquent != 0 {
		t.Fatalf("the day after found %+v", got)
	}
	if got, _ := service.SweepCredit(ctx, staff(), noon(t, "2026-11-12")); got.Overdue != 0 {
		t.Fatalf("a second sweep found the same installment again: %+v", got)
	}
	// Past the 30 days of grace, the agreement is delinquent.
	got, _ := service.SweepCredit(ctx, staff(), noon(t, "2026-12-11"))
	if got.Delinquent != 1 {
		t.Fatalf("31 days late found %+v", got)
	}
	read, _ := service.Credit(ctx, staff(), statement.Agreement.ID)
	if read.Agreement.Status != wallet.CreditDelinquent || read.Agreement.DelinquentAt == nil {
		t.Fatalf("the agreement reads %s", read.Agreement.Status)
	}
	// The creditor reports it, and paying both late installments regularizes
	// and settles it: the record says the report is theirs to withdraw.
	if _, err := service.MarkCreditReported(ctx, staff(), statement.Agreement.ID, true); err != nil {
		t.Fatal(err)
	}
	at := noon(t, "2026-12-11")
	for _, seq := range []int{1, 2} {
		q := wallet.QuoteInstallment(read.Agreement, read.Installments[seq-1], at)
		if _, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
			IdempotencyKey: "late-" + q.Amount.Format(2) + string(rune('a'+seq)), AgreementID: statement.Agreement.ID,
			Sequence: seq, Paid: q.Total.Format(2), At: at,
		}); err != nil {
			t.Fatalf("paying installment %d: %v", seq, err)
		}
	}
	final, _ := service.Credit(ctx, staff(), statement.Agreement.ID)
	if final.Agreement.Status != wallet.CreditSettled || final.Agreement.ReportedAt == nil || final.Agreement.ReportClearedAt != nil {
		t.Fatalf("the settled agreement reads %+v", final.Agreement)
	}
	mu.Lock()
	defer mu.Unlock()
	if !containsKind(told, wallet.CreditInstallmentOverdue) || !containsKind(told, wallet.CreditWentDelinquent) {
		t.Fatalf("the listeners were told %v", told)
	}
}

func TestAPayoffTakesOffTheInterestNotYetDue(t *testing.T) {
	t.Parallel()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "buyer-4", "credit", 2)
	creditLimit(t, service, account.ID, "2000.00")
	statement := openAgreement(t, service, account.ID, "sale-6", "1000.00", 12, 100)
	ctx := context.Background()

	at := noon(t, "2026-10-20")
	quote, err := service.QuoteCreditPayoff(ctx, staff(), statement.Agreement.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is due yet: every interest part comes off, and what remains is
	// the principal.
	if quote.Remaining != statement.Agreement.Total || quote.Total != 100000 || quote.Charges != 0 {
		t.Fatalf("payoff %+v, want the principal 100000", quote)
	}
	if _, err := service.PayOffCredit(ctx, staff(), wallet.CreditPayoffRequest{
		IdempotencyKey: "payoff-1", AgreementID: statement.Agreement.ID, Paid: "999.99", At: at,
	}); !errors.Is(err, wallet.ErrUnderpaid) {
		t.Fatalf("an underpaid payoff answered %v", err)
	}
	done, err := service.PayOffCredit(ctx, staff(), wallet.CreditPayoffRequest{
		IdempotencyKey: "payoff-1", AgreementID: statement.Agreement.ID, Paid: "1000.00", At: at,
	})
	if err != nil || done.Agreement.Status != wallet.CreditSettled || done.Agreement.Rebate != quote.Rebate {
		t.Fatalf("payoff answered %v, agreement %+v", err, done.Agreement)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("after the payoff the balance is %d, want 0", got)
	}
}

func TestCancellingClearsWhatIsStillOwed(t *testing.T) {
	t.Parallel()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "buyer-5", "credit", 2)
	creditLimit(t, service, account.ID, "1000.00")
	statement := openAgreement(t, service, account.ID, "sale-7", "300.00", 3, 0)
	ctx := context.Background()
	if _, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
		IdempotencyKey: "pay-c1", AgreementID: statement.Agreement.ID, Sequence: 1, Paid: "100.00", At: noon(t, "2026-11-10"),
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.CancelCredit(ctx, staff(), wallet.CreditCancelRequest{
		IdempotencyKey: "cancel-1", AgreementID: statement.Agreement.ID, Reason: "arrependimento",
	})
	if err != nil || cancelled.Agreement.Status != wallet.CreditCancelled {
		t.Fatalf("cancelling answered %v", err)
	}
	if cancelled.Installments[0].Status != wallet.InstallmentPaid || cancelled.Installments[1].Status != wallet.InstallmentCancelled {
		t.Fatalf("the installments read %s and %s", cancelled.Installments[0].Status, cancelled.Installments[1].Status)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("after cancelling the balance is %d, want 0", got)
	}
	if _, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
		IdempotencyKey: "pay-c2", AgreementID: statement.Agreement.ID, Sequence: 2, Paid: "100.00",
	}); !errors.Is(err, wallet.ErrInstallmentClosed) {
		t.Fatalf("paying a cancelled installment answered %v", err)
	}
}

func TestTheHolderReadsTheirOwnAgreementsAndDecidesNothing(t *testing.T) {
	t.Parallel()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "buyer-6", "credit", 2)
	creditLimit(t, service, account.ID, "1000.00")
	statement := openAgreement(t, service, account.ID, "sale-8", "100.00", 1, 0)
	ctx := context.Background()
	holder, stranger := person("buyer-6"), person("buyer-7")

	if _, err := service.Credit(ctx, holder, statement.Agreement.ID); err != nil {
		t.Fatalf("the holder could not read their agreement: %v", err)
	}
	if list, err := service.Credits(ctx, holder, wallet.CreditQuery{}); err != nil || len(list) != 1 {
		t.Fatalf("the holder's listing answered %d, %v", len(list), err)
	}
	if _, err := service.Credit(ctx, stranger, statement.Agreement.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("another person read the agreement: %v", err)
	}
	if list, _ := service.Credits(ctx, stranger, wallet.CreditQuery{HolderID: "buyer-6"}); len(list) != 0 {
		t.Fatal("another person listed the holder's agreements")
	}
	for name, err := range map[string]error{
		"open": func() error {
			_, err := service.OpenCredit(ctx, holder, wallet.CreditOpenRequest{IdempotencyKey: "self", WalletID: account.ID, Principal: "10.00",
				Installments: 1, FirstDue: "2026-11-10", Terms: wallet.Meta{"text": "x"}, AcceptedAt: day(t, "2026-10-10")})
			return err
		}(),
		"pay": func() error {
			_, err := service.PayInstallment(ctx, holder, wallet.CreditPaymentRequest{IdempotencyKey: "self-pay",
				AgreementID: statement.Agreement.ID, Sequence: 1, Paid: "100.00"})
			return err
		}(),
		"cancel": func() error {
			_, err := service.CancelCredit(ctx, holder, wallet.CreditCancelRequest{IdempotencyKey: "self-cancel",
				AgreementID: statement.Agreement.ID, Reason: "x"})
			return err
		}(),
		"sweep": func() error { _, err := service.SweepCredit(ctx, holder, time.Now()); return err }(),
	} {
		if !errors.Is(err, security.ErrForbidden) {
			t.Errorf("the holder could %s: %v", name, err)
		}
	}
}

// The same agreement on PostgreSQL: the due dates read back as dates, the
// sweep's "not told yet" is a NULL the update matches, and a delinquent
// agreement regularizes by writing NULL back -- three things SQLite is lenient
// about and a real server is not.
func TestACreditAgreementRunsWholeOnPostgres(t *testing.T) {
	t.Parallel()
	service := wallet.NewWalletService(postgres(t), nil, nil, nil)
	account := openWallet(t, service, "buyer-pg", "credit", 2)
	creditLimit(t, service, account.ID, "1000.00")
	statement := openAgreement(t, service, account.ID, "sale-pg", "300.00", 3, 100)
	ctx := context.Background()

	if got := statement.Installments[0].DueOn.UTC().Format(time.DateOnly); got != "2026-11-10" {
		t.Fatalf("the first installment is due on %s", got)
	}
	if got, err := service.SweepCredit(ctx, staff(), noon(t, "2026-12-15")); err != nil || got.Overdue != 2 || got.Delinquent != 1 {
		t.Fatalf("the sweep found %+v, %v", got, err)
	}
	if got, _ := service.SweepCredit(ctx, staff(), noon(t, "2026-12-16")); got.Overdue != 0 || got.Delinquent != 0 {
		t.Fatalf("a second sweep found %+v", got)
	}
	at := noon(t, "2026-12-15")
	read, _ := service.Credit(ctx, staff(), statement.Agreement.ID)
	q := wallet.QuoteInstallment(read.Agreement, read.Installments[0], at)
	paid, err := service.PayInstallment(ctx, staff(), wallet.CreditPaymentRequest{
		IdempotencyKey: "pg-pay-1", AgreementID: statement.Agreement.ID, Sequence: 1, Paid: q.Total.Format(2), At: at,
	})
	if err != nil || paid.Agreement.Status != wallet.CreditActive || paid.Agreement.DelinquentAt != nil {
		t.Fatalf("paying the oldest answered %v with %s", err, paid.Agreement.Status)
	}
	payoff, _ := service.QuoteCreditPayoff(ctx, staff(), statement.Agreement.ID, at)
	done, err := service.PayOffCredit(ctx, staff(), wallet.CreditPayoffRequest{
		IdempotencyKey: "pg-payoff", AgreementID: statement.Agreement.ID, Paid: payoff.Total.Format(2), At: at,
	})
	if err != nil || done.Agreement.Status != wallet.CreditSettled || balanceOf(t, service, account.ID) != 0 {
		t.Fatalf("the payoff answered %v, %s, balance %d", err, done.Agreement.Status, balanceOf(t, service, account.ID))
	}
}
