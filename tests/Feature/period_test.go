package feature_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/framework/validation"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A statement by period: a ledger read between two instants, by when each entry
// was written or by the date its money is about, and the balance a wallet held
// at an instant.

// mark is an instant strictly between what was written before it and what is
// written after it.
//
// The ledger keeps its timestamps to the second, so the mark is the next whole
// second, waited for: everything written before it was stamped with an earlier
// second, and everything written after it with that second or a later one.
func mark() time.Time {
	now := time.Now().UTC()
	next := now.Truncate(time.Second).Add(time.Second)
	time.Sleep(next.Sub(now) + 5*time.Millisecond)
	return next
}

// periodOf reads one page of a wallet's ledger narrowed to a period.
func periodOf(t *testing.T, service *wallet.WalletService, in wallet.HistoryRequest) []wallet.Amount {
	t.Helper()
	in.Query = data.Query{Limit: 200}
	statement, err := service.History(context.Background(), staff(), in)
	if err != nil {
		t.Fatalf("reading the period: %v", err)
	}
	out := make([]wallet.Amount, 0, len(statement.Entries))
	for _, entry := range statement.Entries {
		out = append(out, entry.Amount)
	}
	return out
}

func sameAmounts(got []wallet.Amount, want ...wallet.Amount) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestAStatementIsReadByThePeriodItWasRecordedIn(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		ctx := context.Background()
		service := wallet.NewWalletService(db, nil, nil, nil)
		account := openWallet(t, service, "user-1", "main", 2)

		before := mark()
		deposit(t, service, account.ID, "a", "10.00")
		first := mark()
		deposit(t, service, account.ID, "b", "5.00")
		if err := withdraw(t, service, staff(), account.ID, "c", "3.00", false); err != nil {
			t.Fatal(err)
		}
		second := mark()
		deposit(t, service, account.ID, "d", "1.00")

		if got := periodOf(t, service, wallet.HistoryRequest{WalletID: account.ID, From: first, Until: second}); !sameAmounts(got, 500, 300) {
			t.Fatalf("between the two marks the ledger reads %v, want [500 300]", got)
		}
		if got := periodOf(t, service, wallet.HistoryRequest{WalletID: account.ID, Until: first}); !sameAmounts(got, 1000) {
			t.Fatalf("up to the first mark the ledger reads %v, want [1000]", got)
		}
		if got := periodOf(t, service, wallet.HistoryRequest{WalletID: account.ID, From: first}); !sameAmounts(got, 500, 300, 100) {
			t.Fatalf("from the first mark the ledger reads %v, want [500 300 100]", got)
		}
		if got := periodOf(t, service, wallet.HistoryRequest{WalletID: account.ID, From: second, Newest: true}); !sameAmounts(got, 100) {
			t.Fatalf("from the second mark, newest first, the ledger reads %v, want [100]", got)
		}

		// The balance at an instant is what the last entry at or before it left.
		// An instant is read to the second it falls in, so "just before a mark" is
		// the second before it.
		for _, at := range []struct {
			name string
			at   time.Time
			want wallet.Amount
		}{
			{"before the first entry", before.Add(-time.Millisecond), 0},
			{"after the first deposit", first.Add(-time.Millisecond), 1000},
			{"after the deposit and the withdrawal", second.Add(-time.Millisecond), 1200},
			{"now", time.Now().Add(time.Second), 1300},
		} {
			money, err := service.BalanceAt(ctx, staff(), account.ID, at.at)
			if err != nil {
				t.Fatalf("the balance %s: %v", at.name, err)
			}
			if money.Amount != at.want || money.Currency != "BRL" || money.DecimalPlaces != 2 {
				t.Fatalf("the balance %s is %+v, want %d BRL at 2 places", at.name, money, at.want)
			}
		}

		// It is a read of the ledger, and asked of the policy as one.
		if _, err := service.BalanceAt(ctx, person("user-1"), account.ID, first); err != nil {
			t.Fatalf("the holder was refused the balance of their own wallet at an instant: %v", err)
		}
		if _, err := service.BalanceAt(ctx, person("user-2"), account.ID, first); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("somebody else asking the balance at an instant got %v, want ErrForbidden", err)
		}
		if _, err := service.BalanceAt(ctx, staff(), "no-such-wallet", first); !errors.Is(err, wallet.ErrNotFound) {
			t.Fatalf("a wallet that does not exist answered %v, want ErrNotFound", err)
		}
	})
}

func TestAPeriodThatCannotBeReadIsRefused(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	now := time.Now()

	for name, in := range map[string]wallet.HistoryRequest{
		"an end before the start":   {WalletID: account.ID, From: now, Until: now.Add(-time.Hour)},
		"an end equal to the start": {WalletID: account.ID, From: now, Until: now},
		"a basis nobody knows":      {WalletID: account.ID, Basis: "settled"},
	} {
		var errs validation.Errors
		if _, err := service.History(context.Background(), staff(), in); !errors.As(err, &errs) {
			t.Errorf("%s answered %v, want a validation error", name, err)
		}
	}
}

func TestAStatementIsReadByTheDateItsMoneyAccrued(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		ctx := context.Background()
		told := &recorder{}
		service := wallet.NewWalletService(db, nil, nil, nil, told.listen)
		account := openWallet(t, service, "user-1", "main", 2)

		january := time.Date(2020, time.January, 15, 0, 0, 0, 0, time.UTC)
		february := time.Date(2020, time.February, 10, 0, 0, 0, 0, time.UTC)
		march := time.Date(2020, time.March, 3, 0, 0, 0, 0, time.UTC)
		month := func(year int, m time.Month) (time.Time, time.Time) {
			start := time.Date(year, m, 1, 0, 0, 0, 0, time.UTC)
			return start, start.AddDate(0, 1, 0)
		}

		first, err := service.Deposit(ctx, staff(), wallet.DepositRequest{
			IdempotencyKey: "january", WalletID: account.ID, Amount: "10.00", AccruedAt: january,
		})
		if err != nil {
			t.Fatal(err)
		}
		if first.Operation.AccruedAt == nil || !first.Operation.AccruedAt.Equal(january) {
			t.Fatalf("the operation accrued at %v, want %v", first.Operation.AccruedAt, january)
		}
		if _, err := service.Withdraw(ctx, staff(), wallet.WithdrawRequest{
			IdempotencyKey: "february", WalletID: account.ID, Amount: "4.00", AccruedAt: february,
		}); err != nil {
			t.Fatal(err)
		}
		deposit(t, service, account.ID, "undated", "1.00")

		// A pending movement accrued in March, confirmed without a date: the
		// confirmation is the same money, and keeps the date.
		proposed, err := service.Deposit(ctx, staff(), wallet.DepositRequest{
			IdempotencyKey: "march", WalletID: account.ID, Amount: "2.00", Pending: true, AccruedAt: march,
		})
		if err != nil {
			t.Fatal(err)
		}
		confirmed, err := confirm(t, service, staff(), proposed.Operation.ID, "march-confirmed")
		if err != nil {
			t.Fatal(err)
		}
		if confirmed.Operation.AccruedAt == nil || !confirmed.Operation.AccruedAt.Equal(march) {
			t.Fatalf("the confirmation accrued at %v, want the %v of what it confirmed", confirmed.Operation.AccruedAt, march)
		}

		from, until := month(2020, time.January)
		if got := periodOf(t, service, wallet.HistoryRequest{WalletID: account.ID, From: from, Until: until, Basis: wallet.DateAccrued}); !sameAmounts(got, 1000) {
			t.Fatalf("January 2020 by accrual reads %v, want [1000]", got)
		}
		from, until = month(2020, time.February)
		if got := periodOf(t, service, wallet.HistoryRequest{WalletID: account.ID, From: from, Until: until, Basis: wallet.DateAccrued}); !sameAmounts(got, 400) {
			t.Fatalf("February 2020 by accrual reads %v, want [400]", got)
		}
		from, until = month(2020, time.March)
		if got := periodOf(t, service, wallet.HistoryRequest{WalletID: account.ID, From: from, Until: until, Basis: wallet.DateAccrued}); !sameAmounts(got, 200, 200) {
			t.Fatalf("March 2020 by accrual reads %v, want the proposal and its confirmation", got)
		}

		// The undated deposit accrued when it was recorded, and so did nothing
		// else: by accrual the hour around now holds it alone, by record all five.
		around := time.Now()
		if got := periodOf(t, service, wallet.HistoryRequest{WalletID: account.ID, From: around.Add(-time.Hour), Until: around.Add(time.Hour), Basis: wallet.DateAccrued}); !sameAmounts(got, 100) {
			t.Fatalf("the hour around now by accrual reads %v, want [100]", got)
		}
		if got := periodOf(t, service, wallet.HistoryRequest{WalletID: account.ID, From: around.Add(-time.Hour), Until: around.Add(time.Hour)}); !sameAmounts(got, 1000, 400, 100, 200, 200) {
			t.Fatalf("the hour around now by record reads %v, want every entry", got)
		}

		// The date travels on the events too.
		moved := told.of(wallet.MoneyMoved)
		if len(moved) == 0 || moved[0].AccruedAt == nil || !moved[0].AccruedAt.Equal(january) {
			t.Fatalf("the first movement was told as accruing at %v, want %v", moved[0].AccruedAt, january)
		}
	})
}
