package feature_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// A void is the other way a pending operation ends: what was proposed is
// withdrawn, nothing moves, and the proposal can no longer be confirmed.

// void withdraws an operation and answers with whatever the service said.
func void(service *wallet.WalletService, actor security.Subject, operationID, key string) (wallet.Receipt, error) {
	return service.Void(context.Background(), actor, wallet.VoidRequest{
		IdempotencyKey: key, OperationID: operationID, Reason: "the order was cancelled",
	})
}

// engines runs a test against SQLite and, when a server is configured, against
// PostgreSQL, where the writes really interleave.
func engines(t *testing.T, run func(t *testing.T, db *data.DB)) {
	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		run(t, database(t))
	})
	t.Run("postgres", func(t *testing.T) {
		t.Parallel()
		run(t, postgres(t))
	})
}

func TestAVoidWithdrawsAPendingOperationAndMovesNothing(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		told := &recorder{}
		service := wallet.NewWalletService(db, nil, nil, nil, told.listen)
		account := openWallet(t, service, "user-1", "main", 2)
		deposit(t, service, account.ID, "fund", "5.00")
		proposed := pend(t, service, account.ID, "proposed", "10.00")

		voided, err := void(service, staff(), proposed.Operation.ID, "void-1")
		if err != nil {
			t.Fatalf("voiding the pending deposit: %v", err)
		}
		if voided.Operation.Kind != wallet.OperationVoid || voided.Operation.Voids() != proposed.Operation.ID {
			t.Fatalf("the void is a %s voiding %q, want a void of %s", voided.Operation.Kind, voided.Operation.Voids(), proposed.Operation.ID)
		}
		if len(voided.Entries) != 1 || voided.Entries[0].Kind != wallet.EntryWithdraw ||
			voided.Entries[0].Amount != 1000 || bool(voided.Entries[0].Settled) {
			t.Fatalf("the void wrote %+v, want one pending withdrawal of 1000 beside the pending deposit", voided.Entries)
		}
		if voided.Pending() {
			t.Fatal("a void reads as pending, and nothing waits on it")
		}
		if got := balanceOf(t, service, account.ID); got != 500 {
			t.Fatalf("the balance is %d after a void, want the 500 it was", got)
		}
		if got := ledgerOf(t, service, account.ID); got != 500 {
			t.Fatalf("the ledger sums to %d, want 500", got)
		}
		if got := pendingOf(t, service, account.ID); got != 0 {
			t.Fatalf("%d is still proposed after the void, want nothing", got)
		}
		if got := told.of(wallet.MoneyVoided); len(got) != 1 || got[0].Amount != 1000 || got[0].OperationID != voided.Operation.ID {
			t.Fatalf("the void was told as %+v, want one wallet.money_voided of 1000", got)
		}

		again, err := void(service, staff(), proposed.Operation.ID, "void-1")
		assertReplayed(t, voided, again, err)

		if _, err := void(service, staff(), proposed.Operation.ID, "void-2"); !errors.Is(err, wallet.ErrAlreadyVoided) {
			t.Fatalf("a second void answered %v, want ErrAlreadyVoided", err)
		}
		if _, err := confirm(t, service, staff(), proposed.Operation.ID, "confirm-voided"); !errors.Is(err, wallet.ErrAlreadyVoided) {
			t.Fatalf("confirming what was voided answered %v, want ErrAlreadyVoided", err)
		}
		if _, err := confirm(t, service, staff(), voided.Operation.ID, "confirm-the-void"); !errors.Is(err, wallet.ErrNotPending) {
			t.Fatalf("confirming the void itself answered %v, want ErrNotPending", err)
		}
		if _, err := void(service, staff(), voided.Operation.ID, "void-the-void"); !errors.Is(err, wallet.ErrNotVoidable) {
			t.Fatalf("voiding the void answered %v, want ErrNotVoidable", err)
		}
		if _, err := service.Reverse(context.Background(), staff(), wallet.ReverseRequest{
			IdempotencyKey: "reverse-the-void", OperationID: voided.Operation.ID, Reason: "asked",
		}); !errors.Is(err, wallet.ErrNotReversible) {
			t.Fatalf("reversing the void answered %v, want ErrNotReversible", err)
		}
		if got := balanceOf(t, service, account.ID); got != 500 {
			t.Fatalf("the refusals moved the balance to %d", got)
		}
	})
}

func TestOnlyWhatNeverCountedIsVoided(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	payer := openWallet(t, service, "user-1", "main", 2)
	payee := openWallet(t, service, "user-2", "main", 2)
	funded := deposit(t, service, payer.ID, "fund", "10.00")

	if _, err := void(service, staff(), funded.Operation.ID, "void-settled"); !errors.Is(err, wallet.ErrNotPending) {
		t.Fatalf("voiding a deposit that counted answered %v, want ErrNotPending", err)
	}

	proposed := pend(t, service, payer.ID, "proposed", "1.00")
	if _, err := confirm(t, service, staff(), proposed.Operation.ID, "confirmed"); err != nil {
		t.Fatal(err)
	}
	if _, err := void(service, staff(), proposed.Operation.ID, "void-confirmed"); !errors.Is(err, wallet.ErrAlreadyConfirmed) {
		t.Fatalf("voiding what was confirmed answered %v, want ErrAlreadyConfirmed", err)
	}

	// Money that left and has not arrived: the side that waits is not withdrawn
	// alone, because the side that moved would have nowhere to go.
	held, err := service.Transfer(ctx, staff(), wallet.TransferRequest{
		IdempotencyKey: "held", FromWalletID: payer.ID, ToWalletID: payee.ID, Amount: "2.00",
		Deposit: wallet.Leg{Pending: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := void(service, staff(), held.Operation.ID, "void-half"); !errors.Is(err, wallet.ErrNotVoidable) {
		t.Fatalf("voiding a transfer half of which counted answered %v, want ErrNotVoidable", err)
	}

	if _, err := void(service, staff(), "no-such-operation", "void-nothing"); !errors.Is(err, wallet.ErrNotFound) {
		t.Fatalf("voiding an operation that does not exist answered %v, want ErrNotFound", err)
	}
}

func TestVoidingIsTheOperators(t *testing.T) {
	t.Parallel()

	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	proposed := pend(t, service, account.ID, "proposed", "10.00")

	if _, err := void(service, person("user-1"), proposed.Operation.ID, "void-mine"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the holder voiding their own proposal answered %v, want ErrForbidden", err)
	}
	if _, err := void(service, staff(), proposed.Operation.ID, "void-staff"); err != nil {
		t.Fatalf("the operator was refused the void: %v", err)
	}
}

func TestAVoidAndAConfirmationRacingCannotBothSucceed(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		service := wallet.NewWalletService(db, nil, nil, nil)
		account := openWallet(t, service, "user-1", "main", 2)

		const proposals = 8
		for round := range proposals {
			proposed := pend(t, service, account.ID, fmt.Sprintf("proposed-%d", round), "1.00")

			var (
				wg        sync.WaitGroup
				voidErr   error
				confirmed error
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, voidErr = void(service, staff(), proposed.Operation.ID, fmt.Sprintf("void-%d", round))
			}()
			go func() {
				defer wg.Done()
				_, confirmed = service.Confirm(context.Background(), staff(), wallet.ConfirmRequest{
					IdempotencyKey: fmt.Sprintf("confirm-%d", round), OperationID: proposed.Operation.ID,
				})
			}()
			wg.Wait()

			switch {
			case voidErr == nil && confirmed == nil:
				t.Fatalf("round %d: the proposal was both voided and confirmed", round)
			case voidErr == nil && !errors.Is(confirmed, wallet.ErrAlreadyVoided):
				t.Fatalf("round %d: the void won and the confirmation answered %v, want ErrAlreadyVoided", round, confirmed)
			case confirmed == nil && !errors.Is(voidErr, wallet.ErrAlreadyConfirmed):
				t.Fatalf("round %d: the confirmation won and the void answered %v, want ErrAlreadyConfirmed", round, voidErr)
			case voidErr != nil && confirmed != nil:
				t.Fatalf("round %d: neither won: void %v, confirmation %v", round, voidErr, confirmed)
			}
		}

		balance := balanceOf(t, service, account.ID)
		if ledger := ledgerOf(t, service, account.ID); ledger != balance {
			t.Fatalf("the ledger sums to %d and the balance is %d", ledger, balance)
		}
		if pending := pendingOf(t, service, account.ID); pending != 0 {
			t.Fatalf("%d is still proposed after every proposal was voided or confirmed", pending)
		}
	})
}
