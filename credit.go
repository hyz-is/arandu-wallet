package wallet

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
	"github.com/arandu-io/hesape/database/model"
)

// Credit agreements: a holder buying now and paying in installments, against
// the credit limit a creditor approved for them -- a store's own crediário.
//
// The wallet already knows how far below zero it may go (SetCredit). An
// agreement is what spends that limit on one purchase and says how it comes
// back: the total owed is drawn from the wallet when the agreement opens, so
// the balance is the debt, and every installment paid is deposited, so the
// ledger explains the debt line by line. What the holder owes beyond the
// installment -- a late fee and interest for the days late -- is charged by
// whoever collects and recorded on the installment; it pays for lateness and
// is not debt.
//
// What happens when a holder does not pay is told, never done: the sweep marks
// an installment overdue and, after the agreement's grace, the agreement
// delinquent, and a listener hears both. Reporting the debtor to a credit
// bureau is the creditor's act, outside this package; the agreement only keeps
// when the creditor said they reported it and when they withdrew the report.
//
// The ceilings below are the Brazilian consumer ones, because the people who
// sell on their own credit here are Brazilian stores: a late fee of at most 2%
// (Código de Defesa do Consumidor, art. 52 §1), interest for lateness of at
// most 1% a month (Código Civil, art. 406), and interest on the financing of at
// most 1% a month for a creditor that is not a financial institution (Decreto
// 22.626/33). A creditor may ask for less and never for more.

const (
	// MaxLateFeeBP is the largest late fee, in basis points of the installment.
	MaxLateFeeBP = 200
	// MaxMoraRateBP is the largest interest for lateness, in basis points of
	// the installment per month, counted by the day.
	MaxMoraRateBP = 100
	// MaxInterestRateBP is the largest interest on the financing, in basis
	// points a month.
	MaxInterestRateBP = 100
	// MaxInstallments is the longest agreement.
	MaxInstallments = 120
)

// CreditStatus is where an agreement stands.
type CreditStatus string

const (
	// CreditActive is being paid, with nothing past its grace.
	CreditActive CreditStatus = "active"
	// CreditDelinquent has an installment unpaid past the agreement's grace.
	CreditDelinquent CreditStatus = "delinquent"
	// CreditSettled was paid in full.
	CreditSettled CreditStatus = "settled"
	// CreditCancelled was undone: what it still owed was cleared.
	CreditCancelled CreditStatus = "cancelled"
)

// InstallmentStatus is where an installment stands.
type InstallmentStatus string

const (
	// InstallmentOpen is waiting to be paid. It may be overdue: OverdueAt says.
	InstallmentOpen InstallmentStatus = "open"
	// InstallmentPaid was paid.
	InstallmentPaid InstallmentStatus = "paid"
	// InstallmentCancelled will not be paid: the agreement was cancelled or
	// paid off before it.
	InstallmentCancelled InstallmentStatus = "cancelled"
)

// CreditAgreement is one purchase paid in installments against a holder's
// credit limit.
type CreditAgreement struct {
	model.Model

	ID       string `db:"id"`
	TenantID string `db:"tenant_id"`
	WalletID string `db:"wallet_id"`
	HolderID string `db:"holder_id"`

	// IdempotencyKey is the creditor's: opening twice with it opens one.
	IdempotencyKey string `db:"idempotency_key"`
	// Reference is what was bought: an order, a sale. This package never
	// reads it.
	Reference string `db:"reference"`

	// Principal is what was financed, and Total what the installments add up
	// to, the financing's interest included. Total is what was drawn from the
	// wallet when the agreement opened.
	Principal Amount `db:"principal"`
	Total     Amount `db:"total"`
	// Installments is how many there are.
	Installments int `db:"installments"`
	// InterestRateBP is the financing's interest, in basis points a month;
	// zero for an agreement without interest.
	InterestRateBP int `db:"interest_rate_bp"`
	// LateFeeBP and MoraRateBP are what lateness costs: a fee once, and
	// interest a month counted by the day, in basis points of the installment.
	LateFeeBP  int `db:"late_fee_bp"`
	MoraRateBP int `db:"mora_rate_bp"`
	// DelinquentAfterDays is how long the oldest unpaid installment may be
	// late before the agreement is delinquent.
	DelinquentAfterDays int `db:"delinquent_after_days"`
	// TimeZone is where the due dates are dates: an installment due on the
	// 10th is late from the 11th there.
	TimeZone string `db:"time_zone"`

	// Terms are what the holder was shown and accepted, as the creditor
	// recorded them, and AcceptedAt and AcceptedFrom when and from where. They
	// are the proof of the debt.
	Terms        Meta      `db:"terms"`
	AcceptedAt   time.Time `db:"accepted_at"`
	AcceptedFrom string    `db:"accepted_from"`

	Status CreditStatus `db:"status"`
	// OperationID is the drawing of Total from the wallet.
	OperationID string `db:"operation_id"`
	OpenedBy    string `db:"opened_by"`

	DelinquentAt *time.Time `db:"delinquent_at"`
	SettledAt    *time.Time `db:"settled_at"`
	CancelledAt  *time.Time `db:"cancelled_at"`
	ClosedBy     string     `db:"closed_by"`
	CancelReason string     `db:"cancel_reason"`
	// Rebate is the financing's interest not charged because the agreement was
	// paid off before it was due.
	Rebate Amount `db:"rebate"`

	// ReportedAt is when the creditor said they reported the holder to a
	// credit bureau, and ReportClearedAt when they said they withdrew it.
	ReportedAt      *time.Time `db:"reported_at"`
	ReportClearedAt *time.Time `db:"report_cleared_at"`
	ReportedBy      string     `db:"reported_by"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

var creditAgreementTable = model.NewTable(model.TableSpec{
	Name:      creditAgreementsTable,
	New:       func() model.Entity { return new(CreditAgreement) },
	ManualKey: true,
})

// CreditInstallment is one installment of an agreement.
type CreditInstallment struct {
	model.Model

	ID          string `db:"id"`
	TenantID    string `db:"tenant_id"`
	AgreementID string `db:"agreement_id"`
	WalletID    string `db:"wallet_id"`
	HolderID    string `db:"holder_id"`

	// Sequence is the installment's place, from 1.
	Sequence int `db:"sequence"`
	// DueOn is the date it is due, at midnight UTC: a date, read in the
	// agreement's time zone.
	DueOn time.Time `db:"due_on"`
	// Amount is what it is, and PrincipalPart and InterestPart what it is
	// made of: the interest part is what a payoff before the date takes off.
	Amount        Amount `db:"amount"`
	PrincipalPart Amount `db:"principal_part"`
	InterestPart  Amount `db:"interest_part"`

	Status InstallmentStatus `db:"status"`
	// OverdueAt is when the sweep found it unpaid after its date.
	OverdueAt *time.Time `db:"overdue_at"`
	// What was paid, and what of it paid for lateness.
	PaidAmount  Amount     `db:"paid_amount"`
	LateFee     Amount     `db:"late_fee"`
	Mora        Amount     `db:"mora"`
	PaidAt      *time.Time `db:"paid_at"`
	PaidBy      string     `db:"paid_by"`
	OperationID string     `db:"operation_id"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

var creditInstallmentTable = model.NewTable(model.TableSpec{
	Name:      creditInstallmentsTable,
	New:       func() model.Entity { return new(CreditInstallment) },
	ManualKey: true,
})

// The errors of the flow, so a consumer can tell them apart with errors.Is.
var (
	// ErrCreditClosed is a change asked of an agreement settled or cancelled.
	ErrCreditClosed = fmt.Errorf("wallet: the credit agreement is already closed: %w", ErrOperationConflict)
	// ErrInstallmentClosed is a payment of an installment already paid or
	// cancelled.
	ErrInstallmentClosed = fmt.Errorf("wallet: the installment is already closed: %w", ErrOperationConflict)
	// ErrUnderpaid is a payment smaller than what is due.
	ErrUnderpaid = errors.New("wallet: the amount paid is less than what is due")
)

// CreditOpenRequest opens an agreement.
type CreditOpenRequest struct {
	IdempotencyKey string
	// WalletID is the holder's wallet with the credit limit the agreement
	// spends.
	WalletID  string
	Reference string
	// Principal is what is financed, in the wallet's decimal notation.
	Principal string
	// Installments is how many, from 1 to MaxInstallments, monthly from
	// FirstDue.
	Installments int
	// FirstDue is the date of the first, "2026-11-10".
	FirstDue string
	// TimeZone is where the dates are dates; empty is UTC.
	TimeZone string
	// InterestRateBP, LateFeeBP and MoraRateBP are in basis points, under the
	// ceilings above.
	InterestRateBP int
	LateFeeBP      int
	MoraRateBP     int
	// DelinquentAfterDays is the grace before the agreement is delinquent;
	// zero is 30 days.
	DelinquentAfterDays int
	// Terms, AcceptedAt and AcceptedFrom are the holder's acceptance.
	Terms        Meta
	AcceptedAt   time.Time
	AcceptedFrom string
	// Meta goes on the drawing's operation.
	Meta Meta
}

// Validate reports the errors per field.
func (r CreditOpenRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "wallet_id", r.WalletID)
	validation.MaxLen(e, "wallet_id", r.WalletID, maxIdentifierLen)
	validation.MaxLen(e, "reference", r.Reference, maxNameLen)
	validation.Required(e, "principal", r.Principal)
	if r.Installments < 1 || r.Installments > MaxInstallments {
		e.Add("installments", fmt.Sprintf("must be between 1 and %d", MaxInstallments))
	}
	if _, err := time.Parse(time.DateOnly, strings.TrimSpace(r.FirstDue)); err != nil {
		e.Add("first_due", "must be a date such as 2026-11-10")
	}
	if _, err := loadZone(r.TimeZone); err != nil {
		e.Add("time_zone", "must be a time zone such as America/Sao_Paulo")
	}
	if r.InterestRateBP < 0 || r.InterestRateBP > MaxInterestRateBP {
		e.Add("interest_rate_bp", fmt.Sprintf("must be between 0 and %d", MaxInterestRateBP))
	}
	if r.LateFeeBP < 0 || r.LateFeeBP > MaxLateFeeBP {
		e.Add("late_fee_bp", fmt.Sprintf("must be between 0 and %d", MaxLateFeeBP))
	}
	if r.MoraRateBP < 0 || r.MoraRateBP > MaxMoraRateBP {
		e.Add("mora_rate_bp", fmt.Sprintf("must be between 0 and %d", MaxMoraRateBP))
	}
	if r.DelinquentAfterDays < 0 || r.DelinquentAfterDays > 365 {
		e.Add("delinquent_after_days", "must be between 0 and 365")
	}
	if len(r.Terms) == 0 {
		e.Add("terms", "record what the holder accepted")
	}
	checkMeta(e, "terms", r.Terms)
	if r.AcceptedAt.IsZero() {
		e.Add("accepted_at", "record when the holder accepted")
	}
	validation.MaxLen(e, "accepted_from", r.AcceptedFrom, maxIdentifierLen)
	checkMeta(e, "meta", r.Meta)
	return e
}

var _ validation.Validatable = CreditOpenRequest{}

func loadZone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(name)
}

// ScheduledInstallment is one installment of a schedule before it is stored.
type ScheduledInstallment struct {
	Sequence      int
	DueOn         time.Time
	Amount        Amount
	PrincipalPart Amount
	InterestPart  Amount
}

// Schedule works out the installments of a financing: equal parts without
// interest, the first ones a cent larger where the principal does not divide;
// with interest, the French (Price) table, each installment's interest rounded
// on the balance it is due on and the last one closing the principal exactly.
// Monthly from firstDue, on the same day, or the month's last when it is
// shorter.
func Schedule(principal Amount, count, interestRateBP int, firstDue time.Time) ([]ScheduledInstallment, error) {
	if principal <= 0 || count < 1 || count > MaxInstallments || interestRateBP < 0 {
		return nil, fmt.Errorf("wallet: a schedule needs a positive principal and from 1 to %d installments", MaxInstallments)
	}
	out := make([]ScheduledInstallment, count)
	for i := range out {
		out[i].Sequence = i + 1
		out[i].DueOn = addMonths(firstDue, i)
	}
	if interestRateBP == 0 {
		base := principal / Amount(count)
		extra := int(principal - base*Amount(count))
		for i := range out {
			part := base
			if i < extra {
				part++
			}
			out[i].Amount, out[i].PrincipalPart = part, part
		}
		return out, nil
	}
	rate := big.NewRat(int64(interestRateBP), 10_000)
	growth := new(big.Rat).Add(big.NewRat(1, 1), rate)
	power := big.NewRat(1, 1)
	for i := 0; i < count; i++ {
		power.Mul(power, growth)
	}
	// PMT = P * i * (1+i)^n / ((1+i)^n - 1)
	numerator := new(big.Rat).Mul(new(big.Rat).SetInt64(int64(principal)), rate)
	numerator.Mul(numerator, power)
	denominator := new(big.Rat).Sub(power, big.NewRat(1, 1))
	payment := roundRat(new(big.Rat).Quo(numerator, denominator))
	balance := principal
	for i := range out {
		interest := roundRat(new(big.Rat).Mul(new(big.Rat).SetInt64(int64(balance)), rate))
		part := payment - interest
		if i == count-1 || part > balance {
			part = balance
		}
		out[i].PrincipalPart, out[i].InterestPart = part, interest
		out[i].Amount = part + interest
		balance -= part
	}
	return out, nil
}

// roundRat rounds half away from zero to a whole minor unit.
func roundRat(r *big.Rat) Amount {
	num, den := new(big.Int).Set(r.Num()), r.Denom()
	q, m := new(big.Int).QuoRem(num, den, new(big.Int))
	if new(big.Int).Mul(new(big.Int).Abs(m), big.NewInt(2)).Cmp(den) >= 0 {
		if num.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return Amount(q.Int64())
}

// addMonths is the same day n months later, or that month's last day.
func addMonths(day time.Time, n int) time.Time {
	y, m, d := day.Date()
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
}

// localDate is the date an instant falls on in a time zone, at midnight UTC so
// it compares with a due date.
func localDate(at time.Time, zone string) time.Time {
	loc, err := loadZone(zone)
	if err != nil {
		loc = time.UTC
	}
	y, m, d := at.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// dateOf is a stored due date as the date it was written: whatever zone the
// driver read it back in, the calendar day in UTC is the one that was stored.
func dateOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// InstallmentQuote is what an installment costs on a day.
type InstallmentQuote struct {
	Sequence int
	Amount   Amount
	// DaysLate is how many days after its date; zero on or before it.
	DaysLate int
	LateFee  Amount
	Mora     Amount
	// Total is what has to be paid: Amount, LateFee and Mora.
	Total Amount
}

// QuoteInstallment prices an installment on the date at falls on: on or before
// its date it is its amount; after it, the late fee once and the interest for
// lateness by the day, a month being thirty.
func QuoteInstallment(a CreditAgreement, in CreditInstallment, at time.Time) InstallmentQuote {
	q := InstallmentQuote{Sequence: in.Sequence, Amount: in.Amount, Total: in.Amount}
	days := int(localDate(at, a.TimeZone).Sub(dateOf(in.DueOn)).Hours() / 24)
	if days <= 0 {
		return q
	}
	q.DaysLate = days
	q.LateFee = roundRat(big.NewRat(int64(in.Amount)*int64(a.LateFeeBP), 10_000))
	q.Mora = roundRat(big.NewRat(int64(in.Amount)*int64(a.MoraRateBP)*int64(days), 10_000*30))
	q.Total = q.Amount + q.LateFee + q.Mora
	return q
}

// PayoffQuote is what clears an agreement on a day.
type PayoffQuote struct {
	// Remaining is what the open installments add up to: the debt.
	Remaining Amount
	// Rebate is the financing's interest of the installments not yet due,
	// which an early payoff does not pay.
	Rebate Amount
	// Charges are the late fees and interest for lateness of the overdue ones.
	Charges Amount
	// Total is what has to be paid: Remaining less Rebate, plus Charges.
	Total Amount
}

// QuotePayoff prices clearing every open installment on the date at falls on,
// taking off the interest of the ones not yet due (Código de Defesa do
// Consumidor, art. 52 §2).
func QuotePayoff(a CreditAgreement, installments []CreditInstallment, at time.Time) PayoffQuote {
	var q PayoffQuote
	today := localDate(at, a.TimeZone)
	for _, in := range installments {
		if in.Status != InstallmentOpen {
			continue
		}
		q.Remaining += in.Amount
		if dateOf(in.DueOn).After(today) {
			q.Rebate += in.InterestPart
			continue
		}
		p := QuoteInstallment(a, in, at)
		q.Charges += p.LateFee + p.Mora
	}
	q.Total = q.Remaining - q.Rebate + q.Charges
	return q
}

// CreditStatement is an agreement with its installments, in order.
type CreditStatement struct {
	Agreement    CreditAgreement
	Installments []CreditInstallment
}

// OpenCredit opens an agreement against the holder's credit limit: the
// installments are worked out, the total is drawn from the wallet -- refused
// with ErrInsufficientFunds when the limit does not cover it -- and the
// agreement and its installments are written in the same transaction as the
// drawing. Opening again with the same key answers the agreement already
// opened. It is the creditor's act, so the operator's.
func (s *WalletService) OpenCredit(ctx context.Context, actor security.Subject, in CreditOpenRequest) (CreditStatement, error) {
	if errs := in.Validate(); errs.Any() {
		return CreditStatement{}, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditOpen, Wallet{})
	if err != nil {
		return CreditStatement{}, err
	}
	source, err := Wallets(s.db).WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return CreditStatement{}, err
	}
	if source == nil {
		return CreditStatement{}, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletCreditOpen, *source); err != nil {
		return CreditStatement{}, err
	}
	principal, err := positiveAmount(in.Principal, source.DecimalPlaces)
	if err != nil {
		return CreditStatement{}, err
	}
	if existing, err := CreditAgreements(s.db).Where("idempotency_key", in.IdempotencyKey).First(ctx, g); err != nil || existing != nil {
		if err != nil {
			return CreditStatement{}, err
		}
		if existing.WalletID != source.ID || existing.Principal != principal || existing.Installments != in.Installments {
			return CreditStatement{}, ErrOperationConflict
		}
		return s.creditStatement(ctx, g, existing)
	}
	first, _ := time.Parse(time.DateOnly, strings.TrimSpace(in.FirstDue))
	schedule, err := Schedule(principal, in.Installments, in.InterestRateBP, first)
	if err != nil {
		return CreditStatement{}, err
	}
	var total Amount
	for _, item := range schedule {
		if total, err = total.Add(item.Amount); err != nil {
			return CreditStatement{}, err
		}
	}

	now := time.Now().UTC()
	agreementID, err := data.NewID()
	if err != nil {
		return CreditStatement{}, err
	}
	agreement, err := CreditAgreements(s.db).New()
	if err != nil {
		return CreditStatement{}, err
	}
	agreement.ID, agreement.TenantID = agreementID, data.Tenant(g)
	agreement.WalletID, agreement.HolderID = source.ID, source.HolderID
	agreement.IdempotencyKey, agreement.Reference = in.IdempotencyKey, strings.TrimSpace(in.Reference)
	agreement.Principal, agreement.Total, agreement.Installments = principal, total, in.Installments
	agreement.InterestRateBP, agreement.LateFeeBP, agreement.MoraRateBP = in.InterestRateBP, in.LateFeeBP, in.MoraRateBP
	agreement.DelinquentAfterDays = in.DelinquentAfterDays
	if agreement.DelinquentAfterDays == 0 {
		agreement.DelinquentAfterDays = 30
	}
	agreement.TimeZone = strings.TrimSpace(in.TimeZone)
	if agreement.TimeZone == "" {
		agreement.TimeZone = "UTC"
	}
	agreement.Terms, agreement.AcceptedAt, agreement.AcceptedFrom = in.Terms, in.AcceptedAt.UTC(), strings.TrimSpace(in.AcceptedFrom)
	agreement.Status, agreement.OpenedBy = CreditActive, actor.ID
	agreement.CreatedAt, agreement.UpdatedAt = now, now

	installments := make([]*CreditInstallment, 0, len(schedule))
	for _, item := range schedule {
		row, err := CreditInstallments(s.db).New()
		if err != nil {
			return CreditStatement{}, err
		}
		if row.ID, err = data.NewID(); err != nil {
			return CreditStatement{}, err
		}
		row.TenantID, row.AgreementID = data.Tenant(g), agreementID
		row.WalletID, row.HolderID = source.ID, source.HolderID
		row.Sequence, row.DueOn = item.Sequence, item.DueOn
		row.Amount, row.PrincipalPart, row.InterestPart = item.Amount, item.PrincipalPart, item.InterestPart
		row.Status = InstallmentOpen
		row.CreatedAt, row.UpdatedAt = now, now
		installments = append(installments, row)
	}

	meta := Meta{}
	for k, v := range in.Meta {
		meta[k] = v
	}
	meta["credit_agreement"] = agreementID
	key := "credit-open:" + in.IdempotencyKey
	asked := askedMovement(source.ID, EntryWithdraw, total, false)
	receipt, err := s.commit(ctx, g, operation{
		key:   key,
		kind:  OperationWithdraw,
		meta:  meta,
		owner: source.ID,
		asked: asked,
		record: func(ctx context.Context, g security.Grant, operationID string) error {
			agreement.OperationID = operationID
			if _, err := agreement.Save(ctx, g); err != nil {
				return err
			}
			for _, row := range installments {
				if _, err := row.Save(ctx, g); err != nil {
					return err
				}
			}
			return nil
		},
	}, []movement{{wallet: source, kind: EntryWithdraw, amount: total}})
	if err != nil {
		return CreditStatement{}, err
	}
	stored, err := CreditAgreements(s.db).Where("idempotency_key", in.IdempotencyKey).First(ctx, g)
	if err != nil {
		return CreditStatement{}, err
	}
	if stored == nil {
		return CreditStatement{}, ErrNotFound
	}
	statement, err := s.creditStatement(ctx, g, stored)
	if err == nil && receipt.Operation.ID == stored.OperationID {
		s.notify(ctx, g, creditEvent(CreditOpened, *source, statement.Agreement, 0, total))
	}
	return statement, err
}

// creditStatement loads an agreement's installments.
func (s *WalletService) creditStatement(ctx context.Context, g security.Grant, agreement *CreditAgreement) (CreditStatement, error) {
	rows, err := CreditInstallments(s.db).Where("agreement_id", agreement.ID).OrderBy("sequence").Get(ctx, g)
	if err != nil {
		return CreditStatement{}, err
	}
	out := CreditStatement{Agreement: *agreement, Installments: make([]CreditInstallment, 0, len(rows))}
	for _, row := range rows {
		out.Installments = append(out.Installments, *row)
	}
	return out, nil
}

// creditWallet loads an agreement's wallet and asks the policy about it with
// the action given: the call that decides, on the row a holder rule can read.
func (s *WalletService) creditWallet(ctx context.Context, g security.Grant, actor security.Subject, a security.Action, agreement *CreditAgreement) (*Wallet, error) {
	if agreement == nil {
		return nil, ErrNotFound
	}
	source, err := Wallets(s.db).WhereKey(agreement.WalletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, a, *source); err != nil {
		return nil, err
	}
	return source, nil
}

// Credit reads one agreement with its installments: the holder their own, the
// operator any.
func (s *WalletService) Credit(ctx context.Context, actor security.Subject, agreementID string) (CreditStatement, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditView, Wallet{})
	if err != nil {
		return CreditStatement{}, err
	}
	agreement, err := CreditAgreements(s.db).WhereKey(agreementID).First(ctx, g)
	if err != nil {
		return CreditStatement{}, err
	}
	if _, err := s.creditWallet(ctx, g, actor, WalletCreditView, agreement); err != nil {
		return CreditStatement{}, err
	}
	return s.creditStatement(ctx, g, agreement)
}

// CreditQuery narrows a listing of agreements.
type CreditQuery struct {
	// HolderID is whose; an operator may leave it empty for every holder.
	HolderID string
	// Status is one status, or empty for all.
	Status CreditStatus
}

// Credits lists agreements, newest first: a holder their own, the operator
// any holder's.
func (s *WalletService) Credits(ctx context.Context, actor security.Subject, in CreditQuery) ([]*CreditAgreement, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditView, Wallet{})
	if err != nil {
		return nil, err
	}
	holder := strings.TrimSpace(in.HolderID)
	if !actor.HasRole(OperatorRole) {
		holder = actor.ID
	}
	if holder != "" {
		if _, err := security.Authorize(ctx, s.policy, actor, WalletCreditView, Wallet{HolderID: holder}); err != nil {
			return nil, err
		}
	}
	q := CreditAgreements(s.db).OrderByDesc("created_at").OrderByDesc("id")
	if holder != "" {
		q = q.Where("holder_id", "=", holder)
	}
	if in.Status != "" {
		q = q.Where("status", "=", string(in.Status))
	}
	return q.Get(ctx, g)
}

// CreditPaymentRequest records an installment paid.
type CreditPaymentRequest struct {
	IdempotencyKey string
	AgreementID    string
	Sequence       int
	// Paid is what the holder paid, in the wallet's decimal notation: at least
	// the installment's quote on At.
	Paid string
	// At is when it was paid; zero is now.
	At   time.Time
	Meta Meta
}

// Validate reports the errors per field.
func (r CreditPaymentRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "agreement_id", r.AgreementID)
	validation.MaxLen(e, "agreement_id", r.AgreementID, maxIdentifierLen)
	if r.Sequence < 1 || r.Sequence > MaxInstallments {
		e.Add("sequence", "name an installment of the agreement")
	}
	validation.Required(e, "paid", r.Paid)
	checkMeta(e, "meta", r.Meta)
	return e
}

var _ validation.Validatable = CreditPaymentRequest{}

// PayInstallment records an installment paid: its amount is deposited, which
// lowers the debt, and what was paid for lateness is kept on it. The payment
// is refused with ErrUnderpaid when it is less than the quote on At. When the
// last open installment is paid the agreement is settled; when an agreement
// that was delinquent has nothing past its grace any more, it is active again.
// Paying again with the same key answers the payment already recorded. Money
// arrives from outside this package, so recording it is the operator's.
func (s *WalletService) PayInstallment(ctx context.Context, actor security.Subject, in CreditPaymentRequest) (CreditStatement, error) {
	if errs := in.Validate(); errs.Any() {
		return CreditStatement{}, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditManage, Wallet{})
	if err != nil {
		return CreditStatement{}, err
	}
	agreement, err := CreditAgreements(s.db).WhereKey(in.AgreementID).First(ctx, g)
	if err != nil {
		return CreditStatement{}, err
	}
	source, err := s.creditWallet(ctx, g, actor, WalletCreditManage, agreement)
	if err != nil {
		return CreditStatement{}, err
	}
	installment, err := CreditInstallments(s.db).Where("agreement_id", agreement.ID).Where("sequence", in.Sequence).First(ctx, g)
	if err != nil {
		return CreditStatement{}, err
	}
	if installment == nil {
		return CreditStatement{}, ErrNotFound
	}
	key := "credit-pay:" + in.IdempotencyKey
	if installment.Status == InstallmentPaid {
		if prior, err := Operations(s.db).Where("idempotency_key", key).First(ctx, g); err == nil && prior != nil && prior.ID == installment.OperationID {
			return s.creditStatement(ctx, g, agreement)
		}
		return CreditStatement{}, ErrInstallmentClosed
	}
	if installment.Status != InstallmentOpen {
		return CreditStatement{}, ErrInstallmentClosed
	}
	if agreement.Status == CreditSettled || agreement.Status == CreditCancelled {
		return CreditStatement{}, ErrCreditClosed
	}
	paid, err := positiveAmount(in.Paid, source.DecimalPlaces)
	if err != nil {
		return CreditStatement{}, err
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	quote := QuoteInstallment(*agreement, *installment, at)
	if paid < quote.Total {
		return CreditStatement{}, ErrUnderpaid
	}
	meta := Meta{}
	for k, v := range in.Meta {
		meta[k] = v
	}
	meta["credit_agreement"], meta["credit_installment"] = agreement.ID, fmt.Sprint(installment.Sequence)
	now := time.Now().UTC()
	var closed CreditStatus
	asked := askedMovement(source.ID, EntryDeposit, installment.Amount, false)
	_, err = s.commit(ctx, g, operation{
		key:   key,
		kind:  OperationDeposit,
		meta:  meta,
		owner: source.ID,
		asked: asked,
		record: func(ctx context.Context, g security.Grant, operationID string) error {
			n, err := CreditInstallments(s.db).Where("id", installment.ID).Where("status", string(InstallmentOpen)).Update(ctx, g, map[string]any{
				"status": string(InstallmentPaid), "paid_amount": int64(paid), "late_fee": int64(quote.LateFee), "mora": int64(quote.Mora),
				"paid_at": at.UTC(), "paid_by": actor.ID, "operation_id": operationID, "updated_at": now,
			})
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrInstallmentClosed
			}
			closed, err = s.afterPayment(ctx, g, agreement, at, now)
			return err
		},
	}, []movement{{wallet: source, kind: EntryDeposit, amount: installment.Amount}})
	if err != nil {
		return CreditStatement{}, err
	}
	statement, err := s.creditStatement(ctx, g, agreement)
	if err != nil {
		return CreditStatement{}, err
	}
	if reloaded, rerr := CreditAgreements(s.db).WhereKey(agreement.ID).First(ctx, g); rerr == nil && reloaded != nil {
		statement.Agreement = *reloaded
	}
	events := []Event{creditEvent(CreditInstallmentPaid, *source, statement.Agreement, installment.Sequence, paid)}
	switch closed {
	case CreditSettled:
		events = append(events, creditEvent(CreditWasSettled, *source, statement.Agreement, 0, statement.Agreement.Total))
	case CreditActive:
		events = append(events, creditEvent(CreditRegularized, *source, statement.Agreement, 0, 0))
	}
	s.notify(ctx, g, events...)
	return statement, nil
}

// afterPayment settles an agreement with nothing open, and makes a delinquent
// one active again when nothing is past its grace any more. It answers the
// status it moved the agreement to, or empty.
func (s *WalletService) afterPayment(ctx context.Context, g security.Grant, agreement *CreditAgreement, at, now time.Time) (CreditStatus, error) {
	open, err := CreditInstallments(s.db).Where("agreement_id", agreement.ID).Where("status", string(InstallmentOpen)).OrderBy("sequence").Get(ctx, g)
	if err != nil {
		return "", err
	}
	if len(open) == 0 {
		_, err := CreditAgreements(s.db).Where("id", agreement.ID).WhereIn("status", []any{string(CreditActive), string(CreditDelinquent)}).
			Update(ctx, g, map[string]any{"status": string(CreditSettled), "settled_at": now, "updated_at": now})
		return CreditSettled, err
	}
	if agreement.Status != CreditDelinquent {
		return "", nil
	}
	if pastGrace(*agreement, *open[0], at) {
		return "", nil
	}
	_, err = CreditAgreements(s.db).Where("id", agreement.ID).Where("status", string(CreditDelinquent)).
		Update(ctx, g, map[string]any{"status": string(CreditActive), "delinquent_at": nil, "updated_at": now})
	return CreditActive, err
}

// pastGrace reports that an open installment is late beyond the agreement's
// grace on the date at falls on.
func pastGrace(a CreditAgreement, in CreditInstallment, at time.Time) bool {
	days := int(localDate(at, a.TimeZone).Sub(dateOf(in.DueOn)).Hours() / 24)
	return days > a.DelinquentAfterDays
}

// QuoteCreditPayoff prices clearing an agreement on the date at falls on.
func (s *WalletService) QuoteCreditPayoff(ctx context.Context, actor security.Subject, agreementID string, at time.Time) (PayoffQuote, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditView, Wallet{})
	if err != nil {
		return PayoffQuote{}, err
	}
	agreement, err := CreditAgreements(s.db).WhereKey(agreementID).First(ctx, g)
	if err != nil {
		return PayoffQuote{}, err
	}
	if _, err := s.creditWallet(ctx, g, actor, WalletCreditView, agreement); err != nil {
		return PayoffQuote{}, err
	}
	statement, err := s.creditStatement(ctx, g, agreement)
	if err != nil {
		return PayoffQuote{}, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	return QuotePayoff(statement.Agreement, statement.Installments, at), nil
}

// CreditPayoffRequest records an agreement cleared at once.
type CreditPayoffRequest struct {
	IdempotencyKey string
	AgreementID    string
	// Paid is what the holder paid: at least the payoff quote on At.
	Paid string
	At   time.Time
	Meta Meta
}

// Validate reports the errors per field.
func (r CreditPayoffRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "agreement_id", r.AgreementID)
	validation.MaxLen(e, "agreement_id", r.AgreementID, maxIdentifierLen)
	validation.Required(e, "paid", r.Paid)
	checkMeta(e, "meta", r.Meta)
	return e
}

var _ validation.Validatable = CreditPayoffRequest{}

// PayOffCredit records every open installment paid at once, the interest of
// the ones not yet due taken off: the whole debt is deposited, the rebate kept
// on the agreement, and the agreement settled.
func (s *WalletService) PayOffCredit(ctx context.Context, actor security.Subject, in CreditPayoffRequest) (CreditStatement, error) {
	if errs := in.Validate(); errs.Any() {
		return CreditStatement{}, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditManage, Wallet{})
	if err != nil {
		return CreditStatement{}, err
	}
	agreement, err := CreditAgreements(s.db).WhereKey(in.AgreementID).First(ctx, g)
	if err != nil {
		return CreditStatement{}, err
	}
	source, err := s.creditWallet(ctx, g, actor, WalletCreditManage, agreement)
	if err != nil {
		return CreditStatement{}, err
	}
	key := "credit-payoff:" + in.IdempotencyKey
	if agreement.Status == CreditSettled {
		if prior, err := Operations(s.db).Where("idempotency_key", key).First(ctx, g); err == nil && prior != nil {
			return s.creditStatement(ctx, g, agreement)
		}
	}
	if agreement.Status == CreditSettled || agreement.Status == CreditCancelled {
		return CreditStatement{}, ErrCreditClosed
	}
	statement, err := s.creditStatement(ctx, g, agreement)
	if err != nil {
		return CreditStatement{}, err
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	quote := QuotePayoff(*agreement, statement.Installments, at)
	paid, err := positiveAmount(in.Paid, source.DecimalPlaces)
	if err != nil {
		return CreditStatement{}, err
	}
	if paid < quote.Total {
		return CreditStatement{}, ErrUnderpaid
	}
	meta := Meta{}
	for k, v := range in.Meta {
		meta[k] = v
	}
	meta["credit_agreement"], meta["credit_payoff"] = agreement.ID, "true"
	now := time.Now().UTC()
	asked := askedMovement(source.ID, EntryDeposit, quote.Remaining, false)
	_, err = s.commit(ctx, g, operation{
		key:   key,
		kind:  OperationDeposit,
		meta:  meta,
		owner: source.ID,
		asked: asked,
		record: func(ctx context.Context, g security.Grant, operationID string) error {
			today := localDate(at, agreement.TimeZone)
			for _, in := range statement.Installments {
				if in.Status != InstallmentOpen {
					continue
				}
				fields := map[string]any{"status": string(InstallmentPaid), "paid_at": at.UTC(), "paid_by": actor.ID, "operation_id": operationID, "updated_at": now}
				if dateOf(in.DueOn).After(today) {
					fields["paid_amount"] = int64(in.Amount - in.InterestPart)
				} else {
					q := QuoteInstallment(*agreement, in, at)
					fields["paid_amount"], fields["late_fee"], fields["mora"] = int64(q.Total), int64(q.LateFee), int64(q.Mora)
				}
				n, err := CreditInstallments(s.db).Where("id", in.ID).Where("status", string(InstallmentOpen)).Update(ctx, g, fields)
				if err != nil {
					return err
				}
				if n != 1 {
					return ErrInstallmentClosed
				}
			}
			n, err := CreditAgreements(s.db).Where("id", agreement.ID).WhereIn("status", []any{string(CreditActive), string(CreditDelinquent)}).
				Update(ctx, g, map[string]any{"status": string(CreditSettled), "settled_at": now, "rebate": int64(quote.Rebate), "updated_at": now})
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrCreditClosed
			}
			return nil
		},
	}, []movement{{wallet: source, kind: EntryDeposit, amount: quote.Remaining}})
	if err != nil {
		return CreditStatement{}, err
	}
	reloaded, err := CreditAgreements(s.db).WhereKey(agreement.ID).First(ctx, g)
	if err != nil || reloaded == nil {
		return CreditStatement{}, errors.Join(err, ErrNotFound)
	}
	out, err := s.creditStatement(ctx, g, reloaded)
	if err == nil {
		s.notify(ctx, g, creditEvent(CreditWasSettled, *source, out.Agreement, 0, paid))
	}
	return out, err
}

// CreditCancelRequest undoes an agreement: a purchase the holder gave up, within
// the days the law gives them, or one the creditor called off.
type CreditCancelRequest struct {
	IdempotencyKey string
	AgreementID    string
	Reason         string
}

// Validate reports the errors per field.
func (r CreditCancelRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "agreement_id", r.AgreementID)
	validation.MaxLen(e, "agreement_id", r.AgreementID, maxIdentifierLen)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	return e
}

var _ validation.Validatable = CreditCancelRequest{}

// CancelCredit undoes an agreement: what its open installments still owed is
// deposited back, which clears the debt, and they are cancelled. Installments
// already paid stay paid; giving that money back is the creditor's, outside
// this package.
func (s *WalletService) CancelCredit(ctx context.Context, actor security.Subject, in CreditCancelRequest) (CreditStatement, error) {
	if errs := in.Validate(); errs.Any() {
		return CreditStatement{}, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditManage, Wallet{})
	if err != nil {
		return CreditStatement{}, err
	}
	agreement, err := CreditAgreements(s.db).WhereKey(in.AgreementID).First(ctx, g)
	if err != nil {
		return CreditStatement{}, err
	}
	source, err := s.creditWallet(ctx, g, actor, WalletCreditManage, agreement)
	if err != nil {
		return CreditStatement{}, err
	}
	if agreement.Status == CreditCancelled {
		return s.creditStatement(ctx, g, agreement)
	}
	if agreement.Status == CreditSettled {
		return CreditStatement{}, ErrCreditClosed
	}
	statement, err := s.creditStatement(ctx, g, agreement)
	if err != nil {
		return CreditStatement{}, err
	}
	var owed Amount
	for _, in := range statement.Installments {
		if in.Status == InstallmentOpen {
			owed += in.Amount
		}
	}
	now := time.Now().UTC()
	close := func(ctx context.Context, g security.Grant) error {
		if _, err := CreditInstallments(s.db).Where("agreement_id", agreement.ID).Where("status", string(InstallmentOpen)).
			Update(ctx, g, map[string]any{"status": string(InstallmentCancelled), "updated_at": now}); err != nil {
			return err
		}
		n, err := CreditAgreements(s.db).Where("id", agreement.ID).WhereIn("status", []any{string(CreditActive), string(CreditDelinquent)}).
			Update(ctx, g, map[string]any{"status": string(CreditCancelled), "cancelled_at": now, "closed_by": actor.ID,
				"cancel_reason": strings.TrimSpace(in.Reason), "updated_at": now})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrCreditClosed
		}
		return nil
	}
	if owed > 0 {
		_, err = s.commit(ctx, g, operation{
			key:    "credit-cancel:" + in.IdempotencyKey,
			kind:   OperationDeposit,
			meta:   Meta{"credit_agreement": agreement.ID, "credit_cancel": "true"},
			owner:  source.ID,
			asked:  askedMovement(source.ID, EntryDeposit, owed, false),
			record: func(ctx context.Context, g security.Grant, _ string) error { return close(ctx, g) },
		}, []movement{{wallet: source, kind: EntryDeposit, amount: owed}})
	} else {
		err = close(ctx, g)
	}
	if err != nil {
		return CreditStatement{}, err
	}
	reloaded, err := CreditAgreements(s.db).WhereKey(agreement.ID).First(ctx, g)
	if err != nil || reloaded == nil {
		return CreditStatement{}, errors.Join(err, ErrNotFound)
	}
	out, err := s.creditStatement(ctx, g, reloaded)
	if err == nil {
		s.notify(ctx, g, creditEvent(CreditWasCancelled, *source, out.Agreement, 0, owed))
	}
	return out, err
}

// MarkCreditReported keeps that the creditor reported the holder to a credit
// bureau (reported true) or withdrew the report (false). Reporting is the
// creditor's act, outside this package; this is the record of it, so an
// agreement settled while reported tells who has to withdraw it.
func (s *WalletService) MarkCreditReported(ctx context.Context, actor security.Subject, agreementID string, reported bool) (CreditStatement, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditManage, Wallet{})
	if err != nil {
		return CreditStatement{}, err
	}
	agreement, err := CreditAgreements(s.db).WhereKey(agreementID).First(ctx, g)
	if err != nil {
		return CreditStatement{}, err
	}
	if _, err := s.creditWallet(ctx, g, actor, WalletCreditManage, agreement); err != nil {
		return CreditStatement{}, err
	}
	now := time.Now().UTC()
	fields := map[string]any{"reported_by": actor.ID, "updated_at": now}
	if reported {
		fields["reported_at"], fields["report_cleared_at"] = now, nil
	} else {
		if agreement.ReportedAt == nil {
			return s.creditStatement(ctx, g, agreement)
		}
		fields["report_cleared_at"] = now
	}
	if _, err := CreditAgreements(s.db).Where("id", agreement.ID).Update(ctx, g, fields); err != nil {
		return CreditStatement{}, err
	}
	reloaded, err := CreditAgreements(s.db).WhereKey(agreement.ID).First(ctx, g)
	if err != nil || reloaded == nil {
		return CreditStatement{}, errors.Join(err, ErrNotFound)
	}
	return s.creditStatement(ctx, g, reloaded)
}

// CreditSweep is what a sweep found.
type CreditSweep struct {
	// Overdue are the installments found unpaid after their date for the first
	// time, and Delinquent the agreements that passed their grace.
	Overdue    int
	Delinquent int
}

// SweepCredit marks, on the date at falls on, the open installments past their
// date as overdue and the agreements whose oldest open installment is past
// their grace as delinquent, and tells the listeners each, once. It is what a
// creditor runs every day; running it twice finds nothing new.
func (s *WalletService) SweepCredit(ctx context.Context, actor security.Subject, at time.Time) (CreditSweep, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditManage, Wallet{})
	if err != nil {
		return CreditSweep{}, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	now := time.Now().UTC()
	var out CreditSweep
	agreements, err := CreditAgreements(s.db).WhereIn("status", []any{string(CreditActive), string(CreditDelinquent)}).OrderBy("created_at").Get(ctx, g)
	if err != nil {
		return CreditSweep{}, err
	}
	for _, agreement := range agreements {
		source, err := Wallets(s.db).WhereKey(agreement.WalletID).First(ctx, g)
		if err != nil {
			return out, err
		}
		if source == nil {
			continue
		}
		if _, err := security.Authorize(ctx, s.policy, actor, WalletCreditManage, *source); err != nil {
			return out, err
		}
		today := localDate(at, agreement.TimeZone)
		open, err := CreditInstallments(s.db).Where("agreement_id", agreement.ID).Where("status", string(InstallmentOpen)).OrderBy("sequence").Get(ctx, g)
		if err != nil {
			return out, err
		}
		var events []Event
		for _, in := range open {
			if !today.After(dateOf(in.DueOn)) || in.OverdueAt != nil {
				continue
			}
			n, err := CreditInstallments(s.db).Where("id", in.ID).Where("status", string(InstallmentOpen)).WhereNull("overdue_at").
				Update(ctx, g, map[string]any{"overdue_at": now, "updated_at": now})
			if err != nil {
				return out, err
			}
			if n == 1 {
				out.Overdue++
				events = append(events, creditEvent(CreditInstallmentOverdue, *source, *agreement, in.Sequence, in.Amount))
			}
		}
		if agreement.Status == CreditActive && len(open) > 0 && pastGrace(*agreement, *open[0], at) {
			n, err := CreditAgreements(s.db).Where("id", agreement.ID).Where("status", string(CreditActive)).
				Update(ctx, g, map[string]any{"status": string(CreditDelinquent), "delinquent_at": now, "updated_at": now})
			if err != nil {
				return out, err
			}
			if n == 1 {
				out.Delinquent++
				agreement.Status = CreditDelinquent
				events = append(events, creditEvent(CreditWentDelinquent, *source, *agreement, open[0].Sequence, open[0].Amount))
			}
		}
		s.notify(ctx, g, events...)
	}
	return out, nil
}

// CreditDue lists the open installments due from one date to another,
// inclusive, oldest first: what a creditor reminds holders of before the date,
// and charges on it.
func (s *WalletService) CreditDue(ctx context.Context, actor security.Subject, from, to time.Time) ([]*CreditInstallment, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletCreditManage, Wallet{})
	if err != nil {
		return nil, err
	}
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return CreditInstallments(s.db).Where("status", string(InstallmentOpen)).
		Where("due_on", ">=", start).Where("due_on", "<=", end).OrderBy("due_on").OrderBy("sequence").Get(ctx, g)
}

// creditEvent is what an agreement's change is told as.
func creditEvent(kind EventKind, w Wallet, a CreditAgreement, sequence int, amount Amount) Event {
	meta := Meta{"credit_agreement": a.ID, "credit_status": string(a.Status)}
	if sequence > 0 {
		meta["credit_installment"] = fmt.Sprint(sequence)
	}
	if a.Reference != "" {
		meta["credit_reference"] = a.Reference
	}
	return Event{
		Kind:          kind,
		WalletID:      w.ID,
		Currency:      w.Currency,
		DecimalPlaces: w.DecimalPlaces,
		OperationID:   a.OperationID,
		Amount:        amount,
		Balance:       w.Balance,
		Meta:          meta,
	}
}
