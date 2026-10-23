// Package everytenant is the ledger read without the tenant filter: the one
// read of arandu-wallet that crosses tenants, WalletService.ListAllOperations,
// after the wallet's policy allowed it to a platform operator.
//
// It is internal so no application can import it. Its tables are Global, which
// is how Hesape spells a table with no tenant filter, and any Grant at all would
// read every tenant through them; the package that may reach them is the one
// whose only caller has asked the policy first. Its rows never leave that
// caller, which copies them into snapshots holding no connection, and nothing
// here writes: there is no path from these types to a row of anybody's money.
package everytenant

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/arandu-io/hesape/database/model"
)

// Number is an integer column, read whatever the engine answers it with: an
// integer, a float, or the text or bytes of one. A row read across tenants is
// copied into the wallet's own types by the caller, so this is the only place
// the reading is spelled.
type Number int64

// Scan reads the column.
func (n *Number) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*n = 0
		return nil
	case int64:
		*n = Number(v)
		return nil
	case int32:
		*n = Number(v)
		return nil
	case int16:
		*n = Number(v)
		return nil
	case int:
		*n = Number(v)
		return nil
	case float64:
		*n = Number(v)
		return nil
	case bool:
		if v {
			*n = 1
		} else {
			*n = 0
		}
		return nil
	case []byte:
		return n.scanText(string(v))
	case string:
		return n.scanText(v)
	}
	return fmt.Errorf("everytenant: an integer column answered with a %T", value)
}

// scanText reads an integer an engine answered as text.
func (n *Number) scanText(text string) error {
	text = strings.TrimSpace(text)
	switch strings.ToLower(text) {
	case "", "f", "false":
		*n = 0
		return nil
	case "t", "true":
		*n = 1
		return nil
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("everytenant: an integer column answered %q", text)
	}
	*n = Number(parsed)
	return nil
}

// Entry is a row of the ledger, of any tenant. Its columns are those of
// wallet.Entry.
type Entry struct {
	model.Model

	ID           string    `db:"id"`
	TenantID     string    `db:"tenant_id"`
	OperationID  string    `db:"operation_id"`
	WalletID     string    `db:"wallet_id"`
	Kind         string    `db:"kind"`
	Sequence     Number    `db:"sequence"`
	Position     Number    `db:"position"`
	Amount       Number    `db:"amount"`
	BalanceAfter Number    `db:"balance_after"`
	Settled      Number    `db:"settled"`
	Meta         string    `db:"meta"`
	CreatedAt    time.Time `db:"created_at"`
}

// Operation is a row of the operations table, of any tenant. Its columns are
// those of wallet.Operation.
type Operation struct {
	model.Model

	ID             string     `db:"id"`
	TenantID       string     `db:"tenant_id"`
	IdempotencyKey string     `db:"idempotency_key"`
	Kind           string     `db:"kind"`
	SettlesID      string     `db:"reverses_id"`
	Reason         string     `db:"reason"`
	Meta           string     `db:"meta"`
	RefersID       string     `db:"refers_id"`
	ReversedAmount Number     `db:"reversed_amount"`
	ResolvedBy     string     `db:"resolved_by"`
	AccruedAt      *time.Time `db:"accrued_at"`
	CreatedAt      time.Time  `db:"created_at"`
}

// Wallet is a row of the wallets table, of any tenant: the columns a listing
// shows beside a movement, and nothing that moves money.
type Wallet struct {
	model.Model

	ID            string `db:"id"`
	TenantID      string `db:"tenant_id"`
	HolderID      string `db:"holder_id"`
	Slug          string `db:"slug"`
	Name          string `db:"name"`
	Currency      string `db:"currency"`
	DecimalPlaces Number `db:"decimal_places"`
}

// The three tables, Global: no tenant filter on any statement. The names are
// written here rather than imported, because the package that owns the
// constants imports this one.
var (
	entryTable = model.NewTable(model.TableSpec{
		Name:         "wallet_entries",
		New:          func() model.Entity { return new(Entry) },
		ManualKey:    true,
		NoTimestamps: true,
		Global:       true,
	})
	operationTable = model.NewTable(model.TableSpec{
		Name:         "wallet_operations",
		New:          func() model.Entity { return new(Operation) },
		ManualKey:    true,
		NoTimestamps: true,
		Global:       true,
	})
	walletTable = model.NewTable(model.TableSpec{
		Name:         "wallets",
		New:          func() model.Entity { return new(Wallet) },
		ManualKey:    true,
		NoTimestamps: true,
		Global:       true,
	})
)
