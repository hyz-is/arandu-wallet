package wallet

import (
	"fmt"

	"github.com/arandu-io/framework/validation"
)

// Validate reports the errors per field.
func (r OpenRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "holder_id", r.HolderID)
	validation.MaxLen(e, "holder_id", r.HolderID, maxIdentifierLen)
	validation.Required(e, "slug", r.Slug)
	validation.MaxLen(e, "slug", r.Slug, maxIdentifierLen)
	validation.Required(e, "name", r.Name)
	validation.MaxLen(e, "name", r.Name, maxNameLen)
	validation.MaxLen(e, "description", r.Description, maxDescriptionLen)
	checkMeta(e, "meta", r.Meta)
	validation.Required(e, "currency", string(r.Currency))
	validation.MaxLen(e, "currency", string(r.Currency), maxCurrencyLen)
	if !ValidDecimalPlaces(r.DecimalPlaces) {
		e.Add("decimal_places", fmt.Sprintf("has to be between 0 and %d", MaxDecimalPlaces))
	}
	return e
}

// Validate reports the errors per field.
func (r CreditRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "wallet_id", r.WalletID)
	validation.MaxLen(e, "wallet_id", r.WalletID, maxIdentifierLen)
	validation.Required(e, "limit", r.Limit)
	return e
}

// Validate reports the errors per field.
func (r DepositRequest) Validate() validation.Errors {
	return validateMovement(r.IdempotencyKey, r.WalletID, r.Amount, r.Meta)
}

// Validate reports the errors per field.
func (r WithdrawRequest) Validate() validation.Errors {
	return validateMovement(r.IdempotencyKey, r.WalletID, r.Amount, r.Meta)
}

// Validate reports the errors per field.
func (r TransferRequest) Validate() validation.Errors {
	e := validateMovement(r.IdempotencyKey, r.FromWalletID, r.Amount, r.Meta)
	validation.Required(e, "to_wallet_id", r.ToWalletID)
	validation.MaxLen(e, "to_wallet_id", r.ToWalletID, maxIdentifierLen)
	checkMeta(e, "withdrawal_meta", r.Withdrawal.Meta)
	checkMeta(e, "deposit_meta", r.Deposit.Meta)
	return e
}

// Validate reports the errors per field.
func (r ReverseRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "operation_id", r.OperationID)
	validation.MaxLen(e, "operation_id", r.OperationID, maxIdentifierLen)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	checkMeta(e, "meta", r.Meta)
	return e
}

// Validate reports the errors per field.
func (r ConfirmRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "operation_id", r.OperationID)
	validation.MaxLen(e, "operation_id", r.OperationID, maxIdentifierLen)
	checkMeta(e, "meta", r.Meta)
	return e
}

// validateMovement holds what every movement of money requires: a key to make
// the request replayable, a wallet to move, and an amount that is not empty.
// The amount's digits are checked against the wallet's scale later, where the
// scale is known.
func validateMovement(key, walletID, amount string, meta Meta) validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", key)
	validation.MaxLen(e, "idempotency_key", key, maxIdempotencyKeyLen)
	validation.Required(e, "wallet_id", walletID)
	validation.MaxLen(e, "wallet_id", walletID, maxIdentifierLen)
	validation.Required(e, "amount", amount)
	checkMeta(e, "meta", meta)
	return e
}

// checkMeta reports why what the application attached under a field cannot be
// stored.
//
// It is answered here rather than left to the column, because a movement the
// database refuses is a movement refused after the operation was recorded --
// and the caller would be told about a storage limit by a failed write instead
// of about the payload it sent by a rejected field.
func checkMeta(e validation.Errors, field string, meta Meta) {
	if err := meta.Validate(); err != nil {
		e.Add(field, err.Error())
	}
}

// Validate reports the errors per field.
func (r PayRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "payer_wallet_id", r.PayerWalletID)
	validation.MaxLen(e, "payer_wallet_id", r.PayerWalletID, maxIdentifierLen)
	for field, messages := range r.Cart.Validate() {
		for _, message := range messages {
			e.Add(field, message)
		}
	}
	return e
}

// Validate reports the errors per field.
func (r RefundRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	if len(r.PurchaseIDs) == 0 {
		e.Add("purchase_ids", "names no line, and a refund of nothing is not a movement")
	}
	if len(r.PurchaseIDs) > MaxCartLines {
		e.Add("purchase_ids", ErrCartTooLarge.Error())
	}
	for _, id := range r.PurchaseIDs {
		validation.Required(e, "purchase_ids", id)
		validation.MaxLen(e, "purchase_ids", id, maxIdentifierLen)
	}
	checkMeta(e, "meta", r.Meta)
	return e
}

// Validate reports the errors per field.
func (r CloseRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "wallet_id", r.WalletID)
	validation.MaxLen(e, "wallet_id", r.WalletID, maxIdentifierLen)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	return e
}

// validateName reports why a holder and a slug cannot name a wallet.
func validateName(holderID, slug string) validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "holder_id", holderID)
	validation.MaxLen(e, "holder_id", holderID, maxIdentifierLen)
	validation.Required(e, "slug", slug)
	validation.MaxLen(e, "slug", slug, maxIdentifierLen)
	return e
}

// Validate reports the errors per field.
func (r DescribeRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "wallet_id", r.WalletID)
	validation.MaxLen(e, "wallet_id", r.WalletID, maxIdentifierLen)
	validation.Required(e, "name", r.Name)
	validation.MaxLen(e, "name", r.Name, maxNameLen)
	validation.MaxLen(e, "description", r.Description, maxDescriptionLen)
	checkMeta(e, "meta", r.Meta)
	return e
}

// positiveAmount reads what the caller wrote at the scale of the wallet it is
// for, and refuses everything that is not money moving.
func positiveAmount(text string, places int) (Amount, error) {
	amount, err := ParseAmount(text, places)
	if err != nil {
		return 0, err
	}
	if amount <= 0 {
		return 0, ErrAmountNotPositive
	}
	return amount, nil
}

// boundedLimit is what a page size becomes: the default when nothing was asked
// for, and the maximum when more was.
func boundedLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	}
	return limit
}

// Validate reports the errors per field.
func (r RebuildRequest) Validate() validation.Errors {
	e := validation.Errors{}
	validation.Required(e, "idempotency_key", r.IdempotencyKey)
	validation.MaxLen(e, "idempotency_key", r.IdempotencyKey, maxIdempotencyKeyLen)
	validation.Required(e, "wallet_id", r.WalletID)
	validation.MaxLen(e, "wallet_id", r.WalletID, maxIdentifierLen)
	validation.Required(e, "reason", r.Reason)
	validation.MaxLen(e, "reason", r.Reason, maxReasonLen)
	checkMeta(e, "meta", r.Meta)
	return e
}
