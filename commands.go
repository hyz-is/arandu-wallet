// What an operator runs from a terminal.
//
// They are values of the framework's own command type, built by a constructor
// this package exports, and the application adds them to its console beside
// every other group it holds. They are not a program: a package that carried a
// main would be a second way to reach these tables, compiled by nobody who
// installed it and audited by nothing -- see PublishCommand for the same
// reasoning on the other half of the install.
//
// # They do not move money
//
// Every command here answers a question: what wallets are there, what happened
// on one of them, what was bought with it, and whether its ledger still adds up
// to the balance beside it. None of them deposits, withdraws, transfers,
// reverses or refunds, and that is a decision rather than a gap. Money that
// moves carries an idempotency key so that a retry is safe, and a terminal is
// exactly where a command is retried by somebody who is not sure whether the
// first one worked -- a shell that scrolled away is not a receipt.
//
// The audit is the one that writes, and what it writes is not money: a wallet
// whose ledger has stopped explaining its balance is frozen, so that nothing is
// paid out of a number this package cannot account for. It moves no balance and
// closes no difference; the command that would is the one that does not exist,
// for the reason below.
//
// # Every command authorizes, and none of them invents a subject
//
// A command reaches the same service the routes do, so it passes the same
// policy. Who it runs as is the application's answer and not this package's:
// Deps carries an Operator, the application writes it, and a package that
// minted a subject for itself would be a package that authorizes itself. With
// the policy this package ships, an operator without the role is refused every
// command below -- which is what a package whose rules nobody has opened yet
// should do from a terminal as well as from a browser.

package wallet

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/console"
)

// CommandPrefix is what every command of this package is called under, so
// `aru list` groups them and two packages cannot claim one name.
const CommandPrefix = "wallet:"

// Deps is what the commands need, built by the application where it wires
// everything else.
//
// It is a struct and not a list of parameters because the set grows: a
// constructor per command, each threading the same two values, is the same
// wiring written four times and corrected in three of them.
type Deps struct {
	// Service is the same service the routes call. One of it, holding one
	// database handle, so a balance read from a terminal and a balance read
	// through a request are the same rows decided by the same policy.
	Service *WalletService

	// Operator says who a command runs as, for the customer it names.
	//
	// It is the application's decision. A command has no session to read a
	// subject from, and a subject this package invented would be one no policy
	// of the application ever agreed to -- so the application writes the
	// function, and what it returns is what the policy is asked about.
	Operator func(tenant string) security.Subject
}

// Validate reports what the dependencies cannot be used with.
func (d Deps) Validate() error {
	if d.Service == nil {
		return errors.New("wallet: Deps.Service is required: a command with no service has no policy to pass and no rows to read")
	}
	if d.Operator == nil {
		return errors.New("wallet: Deps.Operator is required: a command runs as somebody, and who that is belongs to the application rather than to this package")
	}
	return nil
}

// Commands builds every command of this package against one set of
// dependencies, so an application registers the group in a single call rather
// than naming each command and threading the same values through all of them.
//
// It returns an error rather than panicking, for the reason New does: everything
// it refuses is a wiring mistake, and a wiring mistake found where the console
// is assembled costs one restart.
func Commands(deps Deps) ([]console.Command, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	return []console.Command{
		walletsCommand(deps),
		statementCommand(deps),
		purchasesCommand(deps),
		auditCommand(deps),
	}, nil
}

// walletsCommand prints the wallets a customer holds.
func walletsCommand(deps Deps) console.Command {
	return console.Command{
		Signature: CommandPrefix + "wallets {--tenant= : The customer to read as}" +
			" {--holder= : Narrow to one holder}",
		Description: "list the wallets a customer holds",
		Run: func(ctx context.Context, o *console.IO) error {
			actor, tenant, err := operator(deps, o)
			if err != nil {
				return err
			}

			records, err := deps.Service.List(ctx, actor, ListRequest{
				Query:    data.Query{Limit: maxLimit},
				HolderID: o.Option("holder").String(),
			})
			if err != nil {
				return fail(o, err)
			}
			if len(records) == 0 {
				o.Comment("%s holds no wallet", tenant)
				return nil
			}

			rows := make([][]string, 0, len(records))
			for _, record := range records {
				rows = append(rows, []string{
					record.ID, record.HolderID, record.Slug, string(record.Currency),
					record.Balance.Format(record.DecimalPlaces),
					record.CreditLimit.Format(record.DecimalPlaces),
				})
			}
			o.Table([]string{"id", "holder", "slug", "currency", "balance", "credit"}, rows)
			return nil
		},
	}
}

// statementCommand prints the ledger of one wallet, oldest first.
func statementCommand(deps Deps) console.Command {
	return console.Command{
		Signature: CommandPrefix + "statement {wallet : The identifier of the wallet}" +
			" {--tenant= : The customer it belongs to}" +
			" {--limit= : How many movements to print, newest page last}",
		Description: "print the ledger of one wallet",
		Run: func(ctx context.Context, o *console.IO) error {
			actor, _, err := operator(deps, o)
			if err != nil {
				return err
			}

			statement, err := deps.Service.History(ctx, actor, HistoryRequest{
				WalletID: o.Argument("wallet").String(),
				Query:    data.Query{Limit: count(o.Option("limit").String(), defaultLimit)},
			})
			if err != nil {
				return fail(o, err)
			}
			if len(statement.Entries) == 0 {
				o.Comment("nothing has moved on %s", statement.Wallet.ID)
				return nil
			}

			places := statement.Wallet.DecimalPlaces
			rows := make([][]string, 0, len(statement.Entries))
			for _, entry := range statement.Entries {
				if entry == nil {
					continue
				}
				kind := string(statement.Operations[entry.OperationID].Kind)
				settled := "yes"
				if !entry.Settled {
					settled = "waiting"
				}
				rows = append(rows, []string{
					strconv.FormatInt(entry.Sequence, 10),
					kind,
					string(entry.Kind),
					entry.Amount.Format(places),
					entry.BalanceAfter.Format(places),
					settled,
				})
			}
			o.Table([]string{"seq", "operation", "direction", "amount", "balance", "settled"}, rows)

			// The rates and the charges go under the page rather than into it,
			// because each belongs to an operation and an operation writes an
			// entry on two or three wallets: repeating one on every line would
			// be repeating one fact until two copies of it could differ.
			for _, conversion := range statement.Conversions {
				o.TwoColumnDetail(
					fmt.Sprintf("rate on %s", conversion.OperationID),
					fmt.Sprintf("%s -> %s at %s", conversion.From(), conversion.To(), conversion.Rate()),
				)
			}
			for _, charge := range statement.Charges {
				o.TwoColumnDetail(
					fmt.Sprintf("charge on %s", charge.OperationID),
					fmt.Sprintf("fee %s, discount %s", charge.Fee(),
						Money{Amount: charge.Discount, Currency: charge.Currency, DecimalPlaces: charge.DecimalPlaces}),
				)
			}
			return nil
		},
	}
}

// purchasesCommand prints what one wallet bought, newest first.
func purchasesCommand(deps Deps) console.Command {
	return console.Command{
		Signature: CommandPrefix + "purchases {wallet : The identifier of the wallet}" +
			" {--tenant= : The customer it belongs to}" +
			" {--limit= : How many lines to print}",
		Description: "print what one wallet has bought",
		Run: func(ctx context.Context, o *console.IO) error {
			actor, _, err := operator(deps, o)
			if err != nil {
				return err
			}
			id := o.Argument("wallet").String()

			lines, err := deps.Service.PurchasesOf(ctx, actor, id,
				data.Query{Limit: count(o.Option("limit").String(), defaultLimit)})
			if err != nil {
				return fail(o, err)
			}
			if len(lines) == 0 {
				o.Comment("%s has bought nothing", id)
				return nil
			}

			rows := make([][]string, 0, len(lines))
			for _, line := range lines {
				if line == nil {
					continue
				}
				rows = append(rows, []string{
					line.ID,
					string(line.Kind),
					line.ProductKey,
					strconv.Itoa(line.Quantity),
					line.ReceiverWalletID,
					line.PaidAmount.Format(line.DecimalPlaces),
					line.FeeAmount.Format(line.DecimalPlaces),
				})
			}
			o.Table([]string{"id", "kind", "product", "qty", "paid to", "paid", "fee"}, rows)
			return nil
		},
	}
}

// auditCommand reports whether every ledger still adds up to the balance beside
// it, and freezes the wallets where it does not.
//
// It repairs nothing, and the flag that would repair does not exist. A balance
// this package quietly rewrote would be a defect nobody ever heard about, in the
// one table where the defect is money -- what an operator needs is the number
// and the wallet, so that somebody can find out why. Closing a difference is
// WalletService.Rebuild: it takes a reason, it writes a row rather than editing
// a column, and a shell history is not where that decision belongs.
//
// The freeze is not a repair and is not optional. A wallet found to disagree
// with its own ledger goes on serving withdrawals until something stops it, and
// the audit is the thing that has just found out.
func auditCommand(deps Deps) console.Command {
	return console.Command{
		Signature: CommandPrefix + "audit {wallet? : One wallet, or every wallet when it is left out}" +
			" {--tenant= : The customer to read as}",
		Description: "check that every ledger adds up to the balance beside it",
		Run: func(ctx context.Context, o *console.IO) error {
			actor, tenant, err := operator(deps, o)
			if err != nil {
				return err
			}

			ids := []string{}
			if one := o.Argument("wallet").String(); one != "" {
				ids = append(ids, one)
			} else {
				records, err := deps.Service.List(ctx, actor, ListRequest{Query: data.Query{Limit: maxLimit}})
				if err != nil {
					return fail(o, err)
				}
				for _, record := range records {
					ids = append(ids, record.ID)
				}
			}
			if len(ids) == 0 {
				o.Comment("%s holds no wallet, so there is nothing to check", tenant)
				return nil
			}

			broken := 0
			for _, id := range ids {
				report, err := deps.Service.Reconcile(ctx, actor, id)
				if err != nil {
					return fail(o, err)
				}
				places := report.Wallet.DecimalPlaces
				if report.Balanced() {
					o.TwoColumnDetail(report.Wallet.ID,
						fmt.Sprintf("%s over %d movements", report.Settled.Format(places), report.Entries))
					continue
				}
				broken++
				o.Alert("%s: the balance says %s and the ledger sums to %s, a difference of %s over %d movements%s",
					report.Wallet.ID,
					report.Wallet.Balance.Format(places),
					report.Settled.Format(places),
					report.Difference().Format(places),
					report.Entries,
					frozenNote(report))
			}

			if broken > 0 {
				return console.Exit(1, "%d of %d wallets do not add up", broken, len(ids))
			}
			o.Info("%d wallets add up", len(ids))
			return nil
		},
	}
}

// frozenNote says what the audit did about a wallet that does not add up, and
// nothing where the wallet still moves.
//
// It is on the line rather than in a summary, because the operator reading it
// is deciding what to do about that wallet and "it no longer serves anybody" is
// half of what they need to know.
func frozenNote(report Reconciliation) string {
	if !report.Frozen {
		return ""
	}
	return ". It is frozen and moves no money until Rebuild closes the difference"
}

// operator reads the customer off the command line and asks the application who
// the command runs as.
//
// The tenant is a flag and not a value this package works out, and it is the one
// place a tenant is named from outside a Grant. It is not the same thing as
// reading one out of a request: a request comes from whoever is on the far end
// of the internet, and this comes from whoever holds the terminal the process
// runs on -- the person who could read the database directly anyway.
func operator(deps Deps, o *console.IO) (security.Subject, string, error) {
	tenant := strings.TrimSpace(o.Option("tenant").String())
	if tenant == "" {
		return security.Subject{}, "", console.Exit(1, "--tenant is required: a command reads one customer's money, and there is no session here to say which")
	}
	if !security.ValidTenant(tenant) {
		return security.Subject{}, "", console.Exit(1, "--tenant is %q, which cannot be a tenant: lowercase letters, digits, - and _, up to 64 characters", tenant)
	}

	actor := deps.Operator(tenant)
	if actor.Tenant != tenant {
		// The application's own function is what decides who runs; this only
		// refuses the one answer that cannot be right. A subject of another
		// customer would read that customer's money under a command line that
		// named this one, and the operator would never see it.
		return security.Subject{}, "", console.Exit(1,
			"the operator for %q belongs to %q, so the command would read another customer's money", tenant, actor.Tenant)
	}
	return actor, tenant, nil
}

// count reads a page size off the command line, falling back where nothing was
// asked for.
//
// Anything that is not a number is read as the fallback rather than refused,
// because every command here reads and the worst a wrong number does is show a
// page of the wrong size. The service bounds it either way.
func count(text string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// fail turns what the service refused into an exit code and one line.
//
// A refusal is a 1, and there is no lost race among these: every command reads,
// and a read that was refused is refused again.
func fail(o *console.IO, err error) error {
	switch {
	case errors.Is(err, security.ErrForbidden):
		return console.Exit(1, "the policy refused this. The operator needs the %s role, or a rule in policy.go", OperatorRole)
	case errors.Is(err, ErrNotFound):
		return console.Exit(1, "there is no such wallet")
	}
	return console.Exit(1, "%v", err)
}
