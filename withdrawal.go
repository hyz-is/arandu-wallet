package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"
	"github.com/arandu-io/hesape/database"
	"github.com/arandu-io/hesape/database/model"
	"github.com/arandu-io/hesape/filesystem"
)

// Withdrawal requests: money leaving a wallet because a person asked for it and
// an operator paid it, outside the system, and attached the proof.
//
// The holder asks; the amount is held at once, so it cannot be spent twice
// while the request waits. The operator marks it processing, pays it by PIX or
// transfer, and attaches the receipt and the end-to-end identifier: the hold is
// captured and the money has left. Or the operator rejects it with a reason,
// or the holder cancels it before it is processed, and the hold is released.
//
// What a consumer decides -- one withdrawal a day, a minimum, the whole of an
// invoice -- is the consumer's, checked before it calls RequestWithdrawal. This
// file is the flow every consumer shares, so none of them writes a second one.

// PayoutAccount is where a holder wants to be paid: a PIX key, a bank account,
// a crypto wallet, or another kind the application accepts.
type PayoutAccount struct {
	model.Model

	ID       string `db:"id"`
	TenantID string `db:"tenant_id"`
	HolderID string `db:"holder_id"`

	// Kind is "pix", "bank", "crypto" or another the application names.
	Kind string `db:"kind"`
	// Label is how the holder calls it.
	Label string `db:"label"`
	// Details are the kind's own fields: the PIX key and its type, the bank,
	// branch and account, the network and address.
	Details Meta `db:"details"`
	// HolderName and HolderDocument are whose account it is. The application
	// decides whether they must match the holder's own; this package keeps them
	// so the operator paying it can check.
	HolderName     string `db:"holder_name"`
	HolderDocument string `db:"holder_document"`
	// Disabled accounts stay readable on the requests that used them and take
	// no new one.
	Disabled Flag `db:"disabled"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

var payoutAccountTable = model.NewTable(model.TableSpec{
	Name:      payoutAccountsTable,
	New:       func() model.Entity { return new(PayoutAccount) },
	ManualKey: true,
})

// WithdrawalStatus is where a request stands.
type WithdrawalStatus string

const (
	// WithdrawalRequested is waiting for an operator. The amount is held.
	WithdrawalRequested WithdrawalStatus = "requested"
	// WithdrawalProcessing is being paid by an operator. The amount is held.
	WithdrawalProcessing WithdrawalStatus = "processing"
	// WithdrawalPaid was paid: the hold was captured and the money left.
	WithdrawalPaid WithdrawalStatus = "paid"
	// WithdrawalRejected was refused by an operator: the hold was released.
	WithdrawalRejected WithdrawalStatus = "rejected"
	// WithdrawalCancelled was withdrawn by the holder before processing.
	WithdrawalCancelled WithdrawalStatus = "cancelled"
)

// WithdrawalRequest is one request to take money out of a wallet.
type WithdrawalRequest struct {
	model.Model

	ID       string `db:"id"`
	TenantID string `db:"tenant_id"`
	WalletID string `db:"wallet_id"`
	HolderID string `db:"holder_id"`

	// IdempotencyKey is the caller's: asking twice with it makes one request.
	IdempotencyKey string `db:"idempotency_key"`
	Amount         Amount `db:"amount"`
	// Reference is what the consumer withdraws: an invoice, a period. This
	// package never reads it.
	Reference string `db:"reference"`

	// AccountID is the account chosen, and AccountSnapshot what it said when
	// the request was made: changing the account later does not change where
	// an old request was paid.
	AccountID       string `db:"account_id"`
	AccountSnapshot Meta   `db:"account_snapshot"`

	Status WithdrawalStatus `db:"status"`
	// HoldID is the hold that keeps the amount; OperationID the capture that
	// took it, once paid.
	HoldID      string `db:"hold_id"`
	OperationID string `db:"operation_id"`

	// Who did what, and when. Every change of status writes its pair.
	RequestedBy  string     `db:"requested_by"`
	RequestedAt  time.Time  `db:"requested_at"`
	ProcessingBy string     `db:"processing_by"`
	ProcessingAt *time.Time `db:"processing_at"`
	ClosedBy     string     `db:"closed_by"`
	ClosedAt     *time.Time `db:"closed_at"`

	// The proof of payment: the end-to-end identifier of the PIX (or the
	// transfer's own), and the receipt kept on the application's Disk.
	EndToEndID     string `db:"end_to_end_id"`
	ReceiptKey     string `db:"receipt_key"`
	ReceiptName    string `db:"receipt_name"`
	ReceiptType    string `db:"receipt_type"`
	ReceiptSHA256  string `db:"receipt_sha256"`
	RejectedReason string `db:"rejected_reason"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

var withdrawalRequestTable = model.NewTable(model.TableSpec{
	Name:      withdrawalRequestsTable,
	New:       func() model.Entity { return new(WithdrawalRequest) },
	ManualKey: true,
})

// Open reports whether the request still holds its amount.
func (r WithdrawalRequest) Open() bool {
	return r.Status == WithdrawalRequested || r.Status == WithdrawalProcessing
}

// The errors of the flow, so a consumer can tell them apart with errors.Is.
var (
	// ErrWithdrawalClosed is a change asked of a request already paid,
	// rejected or cancelled.
	ErrWithdrawalClosed = fmt.Errorf("wallet: the withdrawal request is already closed: %w", ErrOperationConflict)
	// ErrWithdrawalProcessing is a cancellation asked after an operator took
	// the request: the money may already be on its way.
	ErrWithdrawalProcessing = fmt.Errorf("wallet: the withdrawal request is being paid and can no longer be cancelled: %w", ErrOperationConflict)
	// ErrAccountUnusable is an account that is disabled or is another holder's.
	ErrAccountUnusable = errors.New("wallet: the payout account cannot take this request")
)

// MaxReceiptBytes is the largest receipt accepted: a PDF or a screenshot of a
// PIX is far below it.
const MaxReceiptBytes = 10 << 20

// receiptTypes are the files a receipt may be.
var receiptTypes = map[string]string{
	"application/pdf": ".pdf",
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
}

// PayoutAccountRequest declares a payout account for a holder.
type PayoutAccountRequest struct {
	HolderID       string
	Kind           string
	Label          string
	Details        Meta
	HolderName     string
	HolderDocument string
}

// Validate reports the errors per field.
func (r PayoutAccountRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "holder_id", r.HolderID)
	validation.MaxLen(e, "holder_id", r.HolderID, maxIdentifierLen)
	validation.Required(e, "kind", r.Kind)
	validation.MaxLen(e, "kind", r.Kind, maxIdentifierLen)
	validation.Required(e, "label", r.Label)
	validation.MaxLen(e, "label", r.Label, maxNameLen)
	validation.Required(e, "holder_name", r.HolderName)
	validation.MaxLen(e, "holder_name", r.HolderName, maxNameLen)
	validation.Required(e, "holder_document", r.HolderDocument)
	validation.MaxLen(e, "holder_document", r.HolderDocument, maxIdentifierLen)
	if len(r.Details) == 0 {
		e.Add("details", "describe the account: a PIX key, a bank account, an address")
	}
	checkMeta(e, "details", r.Details)
	return e
}

var _ validation.Validatable = PayoutAccountRequest{}

// AddPayoutAccount declares where a holder wants to be paid. The holder may
// declare their own; an operator, anybody's.
func (s *WalletService) AddPayoutAccount(ctx context.Context, actor security.Subject, in PayoutAccountRequest) (*PayoutAccount, error) {
	if errs := in.Validate(); errs.Any() {
		return nil, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{})
	if err != nil {
		return nil, err
	}
	// The candidate names the holder, so the policy's rule about the holder is
	// the one that decides.
	if _, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{HolderID: in.HolderID}); err != nil {
		return nil, err
	}
	account, err := PayoutAccounts(s.db).New()
	if err != nil {
		return nil, err
	}
	if account.ID, err = data.NewID(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	account.TenantID = data.Tenant(g)
	account.HolderID = in.HolderID
	account.Kind = strings.TrimSpace(in.Kind)
	account.Label = strings.TrimSpace(in.Label)
	account.Details = in.Details
	account.HolderName = strings.TrimSpace(in.HolderName)
	account.HolderDocument = strings.TrimSpace(in.HolderDocument)
	account.CreatedAt, account.UpdatedAt = now, now
	if _, err := account.Save(ctx, g); err != nil {
		return nil, err
	}
	return account, nil
}

// DisablePayoutAccount stops an account from taking new requests. The requests
// that used it keep their snapshot of it.
func (s *WalletService) DisablePayoutAccount(ctx context.Context, actor security.Subject, accountID string) (*PayoutAccount, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{})
	if err != nil {
		return nil, err
	}
	account, err := PayoutAccounts(s.db).WhereKey(accountID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{HolderID: account.HolderID}); err != nil {
		return nil, err
	}
	account.Disabled = true
	account.UpdatedAt = time.Now().UTC()
	if _, err := account.Save(ctx, g); err != nil {
		return nil, err
	}
	return account, nil
}

// PayoutAccountsOf lists a holder's accounts, the disabled ones last.
func (s *WalletService) PayoutAccountsOf(ctx context.Context, actor security.Subject, holderID string) ([]*PayoutAccount, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{})
	if err != nil {
		return nil, err
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{HolderID: holderID}); err != nil {
		return nil, err
	}
	return PayoutAccounts(s.db).Where("holder_id", "=", holderID).
		OrderBy("disabled").OrderBy("created_at").OrderBy("id").Get(ctx, g)
}

// WithdrawalRequestInput asks for money to leave a wallet.
type WithdrawalRequestInput struct {
	IdempotencyKey string
	WalletID       string
	Amount         string
	AccountID      string
	Reference      string
}

// Validate reports the errors per field.
func (r WithdrawalRequestInput) Validate() validation.Errors {
	e := validateMovement(r.IdempotencyKey, r.WalletID, r.Amount, nil)
	validation.Required(e, "account_id", r.AccountID)
	validation.MaxLen(e, "account_id", r.AccountID, maxIdentifierLen)
	validation.MaxLen(e, "reference", r.Reference, maxNameLen)
	return e
}

var _ validation.Validatable = WithdrawalRequestInput{}

// RequestWithdrawal asks for an amount to leave a wallet, to an account of its
// holder, and holds it at once. Asking again with the same key answers the
// request already made.
func (s *WalletService) RequestWithdrawal(ctx context.Context, actor security.Subject, in WithdrawalRequestInput) (*WithdrawalRequest, error) {
	if errs := in.Validate(); errs.Any() {
		return nil, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{})
	if err != nil {
		return nil, err
	}
	source, err := Wallets(s.db).WhereKey(in.WalletID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, ErrNotFound
	}
	if _, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, *source); err != nil {
		return nil, err
	}
	amount, err := positiveAmount(in.Amount, source.DecimalPlaces)
	if err != nil {
		return nil, err
	}
	if existing, err := WithdrawalRequests(s.db).Where("idempotency_key", in.IdempotencyKey).First(ctx, g); err != nil || existing != nil {
		if existing != nil && (existing.WalletID != source.ID || existing.Amount != amount) {
			return nil, ErrOperationConflict
		}
		return existing, err
	}
	account, err := PayoutAccounts(s.db).WhereKey(in.AccountID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if account == nil || account.HolderID != source.HolderID || bool(account.Disabled) {
		return nil, ErrAccountUnusable
	}

	now := time.Now().UTC()
	holdID, err := data.NewID()
	if err != nil {
		return nil, err
	}
	requestID, err := data.NewID()
	if err != nil {
		return nil, err
	}
	h, err := Holds(s.db).New()
	if err != nil {
		return nil, err
	}
	h.ID, h.TenantID, h.WalletID = holdID, data.Tenant(g), source.ID
	h.IdempotencyKey, h.Amount, h.Status = "withdrawal-request:"+requestID, amount, HoldHeld
	h.Meta = Meta{"withdrawal_request": requestID}
	h.CreatedAt, h.UpdatedAt = now, now

	req, err := WithdrawalRequests(s.db).New()
	if err != nil {
		return nil, err
	}
	req.ID, req.TenantID, req.WalletID, req.HolderID = requestID, data.Tenant(g), source.ID, source.HolderID
	req.IdempotencyKey, req.Amount = in.IdempotencyKey, amount
	req.Reference = strings.TrimSpace(in.Reference)
	req.AccountID = account.ID
	req.AccountSnapshot = accountSnapshot(account)
	req.Status = WithdrawalRequested
	req.HoldID = holdID
	req.RequestedBy, req.RequestedAt = actor.ID, now
	req.CreatedAt, req.UpdatedAt = now, now

	err = database.TransactionAt(ctx, s.db, sql.LevelReadCommitted, func(ctx context.Context) error {
		placed, err := s.setAside(ctx, g, source, amount)
		if err != nil {
			return err
		}
		if !placed {
			return s.whyNotHeld(ctx, g, source.ID)
		}
		if _, err := h.Save(ctx, g); err != nil {
			return err
		}
		_, err = req.Save(ctx, g)
		return err
	})
	if err != nil {
		if errors.Is(err, database.ErrUniqueViolation) {
			if existing, ferr := WithdrawalRequests(s.db).Where("idempotency_key", in.IdempotencyKey).First(ctx, g); ferr == nil && existing != nil {
				return existing, nil
			}
		}
		return nil, err
	}
	return req, nil
}

// accountSnapshot is what an account said when a request named it.
func accountSnapshot(a *PayoutAccount) Meta {
	snap := Meta{
		"kind":            a.Kind,
		"label":           a.Label,
		"holder_name":     a.HolderName,
		"holder_document": a.HolderDocument,
	}
	for k, v := range a.Details {
		snap["details."+k] = v
	}
	return snap
}

// walletOf loads a request's wallet and asks the policy about it with the
// action given: the call that decides, on the row a holder rule can read.
func (s *WalletService) walletOf(ctx context.Context, g security.Grant, actor security.Subject, a security.Action, req *WithdrawalRequest) (*Wallet, error) {
	source, err := Wallets(s.db).WhereKey(req.WalletID).First(ctx, g)
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

// StartProcessing is an operator taking a request to pay it. From here the
// holder can no longer cancel it.
func (s *WalletService) StartProcessing(ctx context.Context, actor security.Subject, requestID string) (*WithdrawalRequest, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsManage, Wallet{})
	if err != nil {
		return nil, err
	}
	req, err := WithdrawalRequests(s.db).WhereKey(requestID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrNotFound
	}
	if _, err := s.walletOf(ctx, g, actor, WalletWithdrawalsManage, req); err != nil {
		return nil, err
	}
	switch req.Status {
	case WithdrawalProcessing:
		return req, nil
	case WithdrawalRequested:
	default:
		return nil, ErrWithdrawalClosed
	}
	now := time.Now().UTC()
	n, err := WithdrawalRequests(s.db).Where("id", req.ID).Where("status", string(WithdrawalRequested)).
		Update(ctx, g, map[string]any{"status": string(WithdrawalProcessing), "processing_by": actor.ID, "processing_at": now, "updated_at": now})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return WithdrawalRequests(s.db).WhereKey(req.ID).First(ctx, g)
	}
	return WithdrawalRequests(s.db).WhereKey(req.ID).First(ctx, g)
}

// PaymentInput is the proof an operator attaches when the money was sent.
type PaymentInput struct {
	RequestID   string
	EndToEndID  string
	ReceiptName string
	ReceiptType string
}

// Validate reports the errors per field.
func (r PaymentInput) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "request_id", r.RequestID)
	validation.Required(e, "end_to_end_id", r.EndToEndID)
	validation.MaxLen(e, "end_to_end_id", r.EndToEndID, maxIdentifierLen)
	validation.Required(e, "receipt_name", r.ReceiptName)
	validation.MaxLen(e, "receipt_name", r.ReceiptName, maxNameLen)
	if _, ok := receiptTypes[r.ReceiptType]; !ok {
		e.Add("receipt", "must be a PDF, a JPEG or a PNG")
	}
	return e
}

var _ validation.Validatable = PaymentInput{}

// PayWithdrawal records that an operator paid a request: the receipt is kept,
// the hold is captured, and the request is paid. Each step is idempotent, so a
// payment that failed halfway is finished by asking again, never repeated.
func (s *WalletService) PayWithdrawal(ctx context.Context, actor security.Subject, in PaymentInput, receipt io.Reader) (*WithdrawalRequest, error) {
	if errs := in.Validate(); errs.Any() {
		return nil, errs
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsManage, Wallet{})
	if err != nil {
		return nil, err
	}
	req, err := WithdrawalRequests(s.db).WhereKey(in.RequestID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrNotFound
	}
	source, err := s.walletOf(ctx, g, actor, WalletWithdrawalsManage, req)
	if err != nil {
		return nil, err
	}
	if req.Status == WithdrawalPaid {
		return req, nil
	}
	if !req.Open() {
		return nil, ErrWithdrawalClosed
	}
	if s.receipts == nil {
		return nil, fmt.Errorf("wallet: no Disk was configured for the receipts (Config.Receipts)")
	}
	body, digest, err := readReceipt(receipt)
	if err != nil {
		return nil, err
	}
	key := path.Join("wallet", "withdrawals", req.ID, digest+receiptTypes[in.ReceiptType])
	if err := s.receipts.Put(ctx, g, key, bytes.NewReader(body), in.ReceiptType); err != nil {
		return nil, err
	}

	h, err := Holds(s.db).WhereKey(req.HoldID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, ErrNotFound
	}
	opKey := "withdrawal-request:paid:" + req.ID
	asked := askedMovement(source.ID, EntryWithdraw, h.Amount, false)
	receiptOp, found, err := s.replay(ctx, g, opKey, OperationWithdraw, source.ID, asked)
	if err != nil {
		return nil, err
	}
	if !found {
		receiptOp, err = s.commit(ctx, g, operation{
			key:   opKey,
			kind:  OperationWithdraw,
			meta:  Meta{"hold": h.ID, "withdrawal_request": req.ID, "end_to_end_id": in.EndToEndID},
			owner: source.ID,
			asked: asked,
			hold:  h.ID,
		}, []movement{{wallet: source, kind: EntryWithdraw, amount: h.Amount, fromHold: true}})
		if err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	_, err = WithdrawalRequests(s.db).Where("id", req.ID).Where("status", "!=", string(WithdrawalPaid)).
		Update(ctx, g, map[string]any{
			"status": string(WithdrawalPaid), "operation_id": receiptOp.Operation.ID,
			"closed_by": actor.ID, "closed_at": now,
			"end_to_end_id": strings.TrimSpace(in.EndToEndID),
			"receipt_key":   key, "receipt_name": strings.TrimSpace(in.ReceiptName),
			"receipt_type": in.ReceiptType, "receipt_sha256": digest, "updated_at": now,
		})
	if err != nil {
		return nil, err
	}
	return WithdrawalRequests(s.db).WhereKey(req.ID).First(ctx, g)
}

// readReceipt reads a receipt, refusing one past the limit, and returns it
// with its digest.
func readReceipt(r io.Reader) ([]byte, string, error) {
	if r == nil {
		return nil, "", validation.Errors{"receipt": {"is required"}}
	}
	body, err := io.ReadAll(io.LimitReader(r, MaxReceiptBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) == 0 {
		return nil, "", validation.Errors{"receipt": {"is empty"}}
	}
	if len(body) > MaxReceiptBytes {
		return nil, "", validation.Errors{"receipt": {fmt.Sprintf("is larger than %d MB", MaxReceiptBytes>>20)}}
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

// RejectWithdrawal is an operator refusing a request, with the reason the
// holder will read. The hold is released.
func (s *WalletService) RejectWithdrawal(ctx context.Context, actor security.Subject, requestID, reason string) (*WithdrawalRequest, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > maxReasonLen {
		return nil, validation.Errors{"reason": {fmt.Sprintf("is required, up to %d characters", maxReasonLen)}}
	}
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsManage, Wallet{})
	if err != nil {
		return nil, err
	}
	req, err := WithdrawalRequests(s.db).WhereKey(requestID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrNotFound
	}
	if _, err := s.walletOf(ctx, g, actor, WalletWithdrawalsManage, req); err != nil {
		return nil, err
	}
	return s.closeUnpaid(ctx, g, actor, req, WithdrawalRejected, reason)
}

// CancelWithdrawal is the holder taking back a request no operator has taken
// yet. The hold is released.
func (s *WalletService) CancelWithdrawal(ctx context.Context, actor security.Subject, requestID string) (*WithdrawalRequest, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{})
	if err != nil {
		return nil, err
	}
	req, err := WithdrawalRequests(s.db).WhereKey(requestID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrNotFound
	}
	if _, err := s.walletOf(ctx, g, actor, WalletWithdrawalsRequest, req); err != nil {
		return nil, err
	}
	if req.Status == WithdrawalProcessing {
		return nil, ErrWithdrawalProcessing
	}
	return s.closeUnpaid(ctx, g, actor, req, WithdrawalCancelled, "cancelled by the holder")
}

// closeUnpaid releases a request's hold and closes it as rejected or
// cancelled. Closing it again as the same answers it as it is.
func (s *WalletService) closeUnpaid(ctx context.Context, g security.Grant, actor security.Subject, req *WithdrawalRequest, to WithdrawalStatus, reason string) (*WithdrawalRequest, error) {
	if req.Status == to {
		return req, nil
	}
	if !req.Open() {
		return nil, ErrWithdrawalClosed
	}
	h, err := Holds(s.db).WhereKey(req.HoldID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, ErrNotFound
	}
	if _, err := s.giveBack(ctx, g, h, HoldReleased, reason); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	fields := map[string]any{"status": string(to), "closed_by": actor.ID, "closed_at": now, "updated_at": now}
	if to == WithdrawalRejected {
		fields["rejected_reason"] = reason
	}
	n, err := WithdrawalRequests(s.db).Where("id", req.ID).
		WhereIn("status", []any{string(WithdrawalRequested), string(WithdrawalProcessing)}).Update(ctx, g, fields)
	if err != nil {
		return nil, err
	}
	after, err := WithdrawalRequests(s.db).WhereKey(req.ID).First(ctx, g)
	if err != nil {
		return nil, err
	}
	if n != 1 && after != nil && after.Status != to {
		return nil, ErrWithdrawalClosed
	}
	return after, nil
}

// WithdrawalFilter narrows a listing of requests.
type WithdrawalFilter struct {
	HolderID string
	WalletID string
	Status   WithdrawalStatus
	Limit    int
}

// WithdrawalRequestsOf lists requests, newest first. An operator lists
// anybody's; a holder only their own, whatever the filter asks.
func (s *WalletService) WithdrawalRequestsOf(ctx context.Context, actor security.Subject, f WithdrawalFilter) ([]*WithdrawalRequest, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{})
	if err != nil {
		return nil, err
	}
	q := WithdrawalRequests(s.db)
	if !actor.HasRole(OperatorRole) {
		// Narrowed by the statement, not by reading the rows and dropping
		// the ones that are not the subject's.
		q = q.Where("holder_id", "=", actor.ID)
	} else if f.HolderID != "" {
		q = q.Where("holder_id", "=", f.HolderID)
	}
	if f.WalletID != "" {
		q = q.Where("wallet_id", "=", f.WalletID)
	}
	if f.Status != "" {
		q = q.Where("status", "=", string(f.Status))
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return q.OrderByDesc("requested_at").OrderByDesc("id").Limit(limit).Get(ctx, g)
}

// WithdrawalReceipt opens the receipt of a paid request, for the holder and
// the operators. The caller closes the body.
func (s *WalletService) WithdrawalReceipt(ctx context.Context, actor security.Subject, requestID string) (*WithdrawalRequest, filesystem.File, error) {
	g, err := security.Authorize(ctx, s.policy, actor, WalletWithdrawalsRequest, Wallet{})
	if err != nil {
		return nil, filesystem.File{}, err
	}
	req, err := WithdrawalRequests(s.db).WhereKey(requestID).First(ctx, g)
	if err != nil {
		return nil, filesystem.File{}, err
	}
	if req == nil {
		return nil, filesystem.File{}, ErrNotFound
	}
	if _, err := s.walletOf(ctx, g, actor, WalletWithdrawalsRequest, req); err != nil {
		return nil, filesystem.File{}, err
	}
	if req.ReceiptKey == "" {
		return nil, filesystem.File{}, ErrNotFound
	}
	if s.receipts == nil {
		return nil, filesystem.File{}, fmt.Errorf("wallet: no Disk was configured for the receipts (Config.Receipts)")
	}
	f, err := s.receipts.Get(ctx, g, req.ReceiptKey)
	if err != nil {
		return nil, filesystem.File{}, err
	}
	return req, f, nil
}
