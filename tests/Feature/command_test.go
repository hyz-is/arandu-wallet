package feature_test

import (
	"context"
	"strings"
	"testing"

	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/console"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What an operator runs, and the two things that make it safe to run.
//
// The first is that a command reaches the same service a request does, so it
// passes the same policy: an operator the application does not vouch for is
// refused from a terminal exactly as they are from a browser. The second is that
// none of these commands moves money -- what they answer is what is there, and
// the one that checks a ledger reports what it finds and never repairs it.

func TestTheCommandsAreBuiltAsAGroupOrNotAtAll(t *testing.T) {
	t.Parallel()

	if _, err := wallet.Commands(wallet.Deps{}); err == nil {
		t.Fatal("a group with no service was built, and its first command would be the thing that said so")
	}
	if _, err := wallet.Commands(wallet.Deps{Service: wallet.NewWalletService(nil, nil, nil, nil)}); err == nil {
		t.Fatal("a group with no operator was built, and a command runs as somebody")
	}

	built, err := wallet.Commands(wallet.Deps{
		Service:  wallet.NewWalletService(nil, nil, nil, nil),
		Operator: func(t string) security.Subject { return security.Subject{ID: "staff-1", Tenant: t} },
	})
	if err != nil {
		t.Fatalf("building the group: %v", err)
	}
	if len(built) == 0 {
		t.Fatal("the group is empty, so every check below would pass by having nothing to read")
	}
	for _, command := range built {
		if !strings.HasPrefix(command.Signature, wallet.CommandPrefix) {
			t.Errorf("%q is not under %q, so two packages could claim one name", command.Signature, wallet.CommandPrefix)
		}
		if command.Description == "" {
			t.Errorf("%q has no description, so `aru list` says nothing about it", command.Signature)
		}
		if command.Run == nil {
			t.Errorf("%q does nothing", command.Signature)
		}
		if !strings.Contains(command.Signature, "--tenant=") {
			t.Errorf("%q takes no tenant, and there is no session here to say which customer it reads", command.Signature)
		}
	}
}

func TestACommandIsRefusedWithoutATenantAndWithTheWrongOne(t *testing.T) {
	t.Parallel()

	deps := wallet.Deps{
		Service: wallet.NewWalletService(database(t), nil, nil, nil),
		// An operator of another customer, which is the one answer the package
		// refuses on the application's behalf.
		Operator: func(string) security.Subject {
			return security.Subject{ID: "staff-1", Tenant: "globex", Roles: []string{wallet.OperatorRole}}
		},
	}
	commands, err := wallet.Commands(deps)
	if err != nil {
		t.Fatalf("building the group: %v", err)
	}

	for _, command := range commands {
		out, err := run(t, command)
		if err == nil {
			t.Errorf("%q ran with no tenant: %s", command.Signature, out)
			continue
		}
		if console.ExitCode(err) != 1 {
			t.Errorf("%q exited %d with no tenant, want 1", command.Signature, console.ExitCode(err))
		}

		out, err = run(t, command, "--tenant", tenant)
		if err == nil {
			t.Errorf("%q ran as another customer's operator: %s", command.Signature, out)
		}
	}
}

func TestTheAuditCommandSaysAWholeLedgerAddsUp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)

	source := openWallet(t, service, "user-1", "main", 2)
	target := openWallet(t, service, "user-2", "main", 2)
	deposit(t, service, source.ID, "seed-1", "50.00")
	if _, err := service.Transfer(ctx, staff(), wallet.TransferRequest{
		IdempotencyKey: "move-1", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "10.00",
	}); err != nil {
		t.Fatalf("transferring: %v", err)
	}

	// The reconciliation the command prints, read directly so the numbers are
	// checkable rather than parsed out of a table.
	report, err := service.Reconcile(ctx, staff(), source.ID)
	if err != nil {
		t.Fatalf("reconciling: %v", err)
	}
	if !report.Balanced() {
		t.Fatalf("a ledger nothing touched does not add up: balance %d, entries sum to %d",
			report.Wallet.Balance, report.Settled)
	}
	if report.Settled != 4000 {
		t.Errorf("the ledger sums to %d, want 4000", report.Settled)
	}
	if report.Entries != 2 {
		t.Errorf("%d movements were read, want 2", report.Entries)
	}
	if report.LastBalanceAfter != report.Wallet.Balance {
		t.Errorf("the newest movement recorded %d and the balance is %d",
			report.LastBalanceAfter, report.Wallet.Balance)
	}
	if report.Difference() != 0 {
		t.Errorf("the balance holds %d beyond what the ledger explains", report.Difference())
	}

	commands, err := wallet.Commands(wallet.Deps{Service: service, Operator: operatorFor})
	if err != nil {
		t.Fatalf("building the group: %v", err)
	}
	out, err := run(t, named(t, commands, "audit"), "--tenant", tenant)
	if err != nil {
		t.Fatalf("auditing every wallet: %v", err)
	}
	if !strings.Contains(out, "2 wallets add up") {
		t.Fatalf("the audit said %q", out)
	}
}

func TestAPendingMovementIsCountedAsWaitingAndNotAsMoney(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "seed-1", "50.00")

	if _, err := service.Deposit(ctx, staff(), wallet.DepositRequest{
		IdempotencyKey: "later-1", WalletID: account.ID, Amount: "10.00", Pending: true,
	}); err != nil {
		t.Fatalf("recording: %v", err)
	}

	report, err := service.Reconcile(ctx, staff(), account.ID)
	if err != nil {
		t.Fatalf("reconciling: %v", err)
	}
	if !report.Balanced() {
		t.Fatalf("a wallet with something waiting does not add up: balance %d, settled %d",
			report.Wallet.Balance, report.Settled)
	}
	if report.Settled != 5000 {
		t.Errorf("the settled movements sum to %d, want 5000", report.Settled)
	}
	if report.Proposed != 1000 {
		t.Errorf("what is waiting sums to %d, want 1000", report.Proposed)
	}
	if report.Gaps != 0 {
		t.Errorf("%d gaps were found in a ledger nothing skipped", report.Gaps)
	}
}

func TestTheStatementCommandPrintsWhatMoved(t *testing.T) {
	t.Parallel()

	db := database(t)
	service := wallet.NewWalletService(db, nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	deposit(t, service, account.ID, "seed-1", "12.34")

	commands, err := wallet.Commands(wallet.Deps{Service: service, Operator: operatorFor})
	if err != nil {
		t.Fatalf("building the group: %v", err)
	}

	out, err := run(t, named(t, commands, "statement"), "--tenant", tenant, account.ID)
	if err != nil {
		t.Fatalf("printing the statement: %v", err)
	}
	if !strings.Contains(out, "12.34") {
		t.Fatalf("the statement does not show what moved: %s", out)
	}

	out, err = run(t, named(t, commands, "wallets"), "--tenant", tenant)
	if err != nil {
		t.Fatalf("listing the wallets: %v", err)
	}
	if !strings.Contains(out, account.ID) {
		t.Fatalf("the listing does not name the wallet: %s", out)
	}

	// A wallet that does not exist is one refusal and not a stack trace.
	if _, err := run(t, named(t, commands, "statement"), "--tenant", tenant, "nothing"); !isExit(err, 1) {
		t.Fatalf("a wallet that does not exist answered %v, want an exit of 1", err)
	}
}

// operatorFor is the operator the command tests run as: this customer's, with
// the role the shipped policy reads.
func operatorFor(name string) security.Subject {
	return security.Subject{ID: "staff-1", Tenant: name, Roles: []string{wallet.OperatorRole}, Verified: true}
}

// named is the command whose signature is under this name.
func named(t *testing.T, commands []console.Command, name string) console.Command {
	t.Helper()

	for _, command := range commands {
		if strings.HasPrefix(command.Signature, wallet.CommandPrefix+name) {
			return command
		}
	}
	t.Fatalf("there is no %s%s command", wallet.CommandPrefix, name)
	return console.Command{}
}

// run executes one command with these arguments and answers with what it wrote.
//
// It binds the signature the way the console application does, so what the test
// exercises is the command as an operator reaches it -- a test that filled the
// arguments in by hand would be a test of a command line nobody types.
func run(t *testing.T, command console.Command, args ...string) (string, error) {
	t.Helper()

	name, arguments, options, err := console.Parse(command.Signature)
	if err != nil {
		t.Fatalf("parsing %q: %v", command.Signature, err)
	}

	out := &strings.Builder{}
	io := console.NewIO(name, args, out, out, nil)
	in := console.NewInput(arguments, options)
	if err := in.Parse(args); err != nil {
		return out.String(), err
	}
	io.SetInput(in)
	// The command runs before the buffer is read: Go evaluates a return's
	// operands left to right, so reading it in the same expression would read
	// it empty.
	err = command.Run(context.Background(), io)
	return out.String(), err
}

// isExit reports that an error is a refusal with this exit code.
func isExit(err error, code int) bool {
	return err != nil && console.ExitCode(err) == code
}
