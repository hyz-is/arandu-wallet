package wallet

import (
	"strings"
	"testing"
)

// The guard on a balance, read off the statement that carries it.
//
// This is the test the package's whole concurrency story rests on, and it lives
// beside the code because what it asks is unexported: moveStatement composes the
// only statement here that writes a balance, and the point is that there is no
// way through it that emits one without its predicate. Asking the function and
// reading what comes back is a stronger claim than reading the source for a
// builder call, because it is the string the database is actually sent.
//
// It replaced a syntax audit over Increment and Decrement, and it holds more
// than that one did: the guard, the tenant, the two reasons a wallet is out of
// service, and the clause that makes the whole thing one round trip. The one
// thing the old audit had that this does not is reach -- it would have caught a
// second balance-moving site somewhere else in the package. That half is held
// in tests/Unit/audit_test.go, by TestOnlyOneStatementInThePackageWritesABalance.

// aWallet is the row every statement below is composed against.
func aWallet() *Wallet {
	return &Wallet{ID: "wallet-1", Balance: 5000, CreditLimit: 1000, LastSequence: 7}
}

// TestEveryBalanceStatementCarriesItsOwnGuard walks every shape of movement
// this package can produce and requires the predicate on each.
//
// The mutation it exists for is the one the reference makes: dropping the
// per-row condition in favour of a batch that names the wallets and nothing
// else. With any of these predicates removed the matching case below fails, and
// on PostgreSQL the behavioural suite fails with it -- a balance goes negative,
// or a frozen wallet pays out.
func TestEveryBalanceStatementCarriesItsOwnGuard(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		m    movement
		want []string
	}{
		{
			name: "a withdrawal is guarded on the balance and the limit",
			m:    movement{wallet: aWallet(), kind: EntryWithdraw, amount: 250},
			want: []string{
				`"balance" = "balance" - 250`,
				`"balance" >= 250 - "credit_limit"`,
				servableGuard,
			},
		},
		{
			name: "a forced withdrawal is still guarded, on the range of the column",
			m:    movement{wallet: aWallet(), kind: EntryWithdraw, amount: 250, force: true},
			want: []string{
				`"balance" = "balance" - 250`,
				`"balance" >= -9223372036854775558`,
				servableGuard,
			},
		},
		{
			name: "a deposit is guarded on the room left in the column",
			m:    movement{wallet: aWallet(), kind: EntryDeposit, amount: 250},
			want: []string{
				`"balance" = "balance" + 250`,
				`"balance" <= 9223372036854775557`,
				servableGuard,
			},
		},
		{
			name: "a pending movement moves no balance and is still gated",
			m:    movement{wallet: aWallet(), kind: EntryDeposit, amount: 250, pending: true},
			want: []string{
				`"last_sequence" = "last_sequence" + 1`,
				servableGuard,
			},
		},
		{
			name: "an adjustment names the state it measured, and lifts the freeze",
			m:    movement{wallet: aWallet(), kind: EntryDeposit, amount: 250, adjust: true},
			want: []string{
				`"frozen" = 1`,
				`"closed" = 0`,
				`"balance" = 5000`,
				`"last_sequence" = 7`,
				`"frozen" = 0`,
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			statement, bindings := moveStatement(c.m, "acme")
			if statement == "" {
				t.Fatal("no statement was composed for a movement this package can produce")
			}
			for _, want := range c.want {
				if !strings.Contains(statement, want) {
					t.Errorf("the statement does not carry %q:\n%s", want, statement)
				}
			}

			// The row and the customer, on every one of them. A statement that
			// named the wallet and not the tenant would reach another
			// customer's row with an identifier somebody guessed.
			for _, want := range []string{`"id" = ?`, `"tenant_id" = ?`} {
				if !strings.Contains(statement, want) {
					t.Errorf("the statement does not carry %q:\n%s", want, statement)
				}
			}
			if len(bindings) != 2 || bindings[0] != "wallet-1" || bindings[1] != "acme" {
				t.Errorf("the bindings are %v, want the wallet and the tenant", bindings)
			}

			// And it reports what it left behind, which is what makes it one
			// round trip instead of two.
			if !strings.Contains(statement, `returning "balance", "credit_limit", "last_sequence", "frozen", "closed"`) {
				t.Errorf("the statement does not report the row it left:\n%s", statement)
			}
		})
	}
}

// TestABalanceStatementIsOneRoundTrip states the property P1-5 exists for, in
// the only terms that survive a rewrite: the statement that moves a balance is
// also the statement that reports it, so nothing follows it on the happy path.
//
// It is a count rather than a timing, because a timing measures the machine.
func TestABalanceStatementIsOneRoundTrip(t *testing.T) {
	t.Parallel()

	statement, _ := moveStatement(movement{wallet: aWallet(), kind: EntryWithdraw, amount: 1}, "acme")
	if got := strings.Count(statement, ";"); got != 0 {
		t.Errorf("the statement is %d statements, and a balance moves in one", got+1)
	}
	if !strings.HasPrefix(statement, `update "wallets" set `) {
		t.Errorf("the statement does not begin as an update of the wallets table:\n%s", statement)
	}
}

// TestAMovementWithNoDirectionComposesNothing holds the closed set. A kind
// nobody wrote a branch for produces no statement at all, rather than one with
// no guard on it.
func TestAMovementWithNoDirectionComposesNothing(t *testing.T) {
	t.Parallel()

	statement, bindings := moveStatement(movement{wallet: aWallet(), kind: "sideways", amount: 1}, "acme")
	if statement != "" || bindings != nil {
		t.Fatalf("a direction money does not move in composed %q", statement)
	}
}
