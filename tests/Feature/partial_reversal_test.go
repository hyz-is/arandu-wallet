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

// Part of an operation given back, as many times as it takes, and never more
// than the operation moved.

// reversePart gives back part of an operation and answers with whatever the
// service said.
func reversePart(service *wallet.WalletService, actor security.Subject, operationID, key, amount string) (wallet.Receipt, error) {
	return service.ReversePartially(context.Background(), actor, wallet.PartialReversalRequest{
		IdempotencyKey: key, OperationID: operationID, Amount: amount, Reason: "part of the order came back",
	})
}

func TestPartOfADepositIsGivenBackUntilNothingIsLeft(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), nil, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	funded := deposit(t, service, account.ID, "fund", "10.00")

	first, err := reversePart(service, staff(), funded.Operation.ID, "part-1", "3.00")
	if err != nil {
		t.Fatalf("giving back 3.00 of 10.00: %v", err)
	}
	if first.Operation.Kind != wallet.OperationPartialReversal || first.Operation.Reverses() != funded.Operation.ID {
		t.Fatalf("the partial reversal is a %s reversing %q, want one of %s", first.Operation.Kind, first.Operation.Reverses(), funded.Operation.ID)
	}
	if len(first.Entries) != 1 || first.Entries[0].Kind != wallet.EntryWithdraw || first.Entries[0].Amount != 300 {
		t.Fatalf("the partial reversal wrote %+v, want one withdrawal of 300", first.Entries)
	}
	if got := balanceOf(t, service, account.ID); got != 700 {
		t.Fatalf("the balance is %d, want 700", got)
	}

	again, err := reversePart(service, staff(), funded.Operation.ID, "part-1", "3.00")
	assertReplayed(t, first, again, err)
	if _, err := reversePart(service, staff(), funded.Operation.ID, "part-1", "4.00"); !errors.Is(err, wallet.ErrOperationConflict) {
		t.Fatalf("the same key for another amount answered %v, want ErrOperationConflict", err)
	}

	if _, err := reversePart(service, staff(), funded.Operation.ID, "part-too-much", "7.01"); !errors.Is(err, wallet.ErrExceedsReversible) {
		t.Fatalf("asking past what is left answered %v, want ErrExceedsReversible", err)
	}
	if _, err := reversePart(service, staff(), funded.Operation.ID, "part-2", "7.00"); err != nil {
		t.Fatalf("giving back the 7.00 left: %v", err)
	}
	if _, err := reversePart(service, staff(), funded.Operation.ID, "part-3", "0.01"); !errors.Is(err, wallet.ErrExceedsReversible) {
		t.Fatalf("giving back past the whole answered %v, want ErrExceedsReversible", err)
	}
	if got := balanceOf(t, service, account.ID); got != 0 {
		t.Fatalf("the balance is %d after everything was given back, want 0", got)
	}
	if got := ledgerOf(t, service, account.ID); got != 0 {
		t.Fatalf("the ledger sums to %d, want 0", got)
	}

	// What was given back is on the operation, and closes it to a whole
	// reversal: undoing it whole would give the parts back twice.
	original, err := service.OperationByKey(ctx, staff(), "fund")
	if err != nil {
		t.Fatal(err)
	}
	if original.Operation.ReversedAmount != 1000 {
		t.Fatalf("the operation says %d was given back, want 1000", original.Operation.ReversedAmount)
	}
	if _, err := service.Reverse(ctx, staff(), wallet.ReverseRequest{
		IdempotencyKey: "whole", OperationID: funded.Operation.ID, Reason: "asked",
	}); !errors.Is(err, wallet.ErrPartiallyReversed) {
		t.Fatalf("reversing whole what was reversed in part answered %v, want ErrPartiallyReversed", err)
	}
}

func TestPartOfATransferGoesBackFromThePayeeToThePayer(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		service := wallet.NewWalletService(db, nil, nil, nil)
		payer := openWallet(t, service, "user-1", "main", 2)
		payee := openWallet(t, service, "user-2", "main", 2)
		deposit(t, service, payer.ID, "fund", "10.00")
		paid, err := payment(t, service, payer.ID, payee.ID, "pay", "10.00")
		if err != nil {
			t.Fatal(err)
		}

		if _, err := reversePart(service, staff(), paid.Operation.ID, "back-1", "4.00"); err != nil {
			t.Fatalf("giving back 4.00 of the payment: %v", err)
		}
		if got := balanceOf(t, service, payer.ID); got != 400 {
			t.Fatalf("the payer holds %d, want 400", got)
		}
		if got := balanceOf(t, service, payee.ID); got != 600 {
			t.Fatalf("the payee holds %d, want 600", got)
		}
		if _, err := reversePart(service, staff(), paid.Operation.ID, "back-2", "6.01"); !errors.Is(err, wallet.ErrExceedsReversible) {
			t.Fatalf("giving back more than the 6.00 left answered %v, want ErrExceedsReversible", err)
		}
		if _, err := reversePart(service, person("user-1"), paid.Operation.ID, "back-3", "1.00"); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("the payer giving their own money back answered %v, want ErrForbidden", err)
		}
	})
}

func TestWhatCannotBeSplitIsNotReversedInPart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), &fixedRate{numerator: 1, denominator: 5}, nil, nil)
	account := openWallet(t, service, "user-1", "main", 2)
	dollars := openIn(t, service, "user-1", "usd", "USD", 2)
	funded := deposit(t, service, account.ID, "fund", "10.00")

	exchanged, err := payment(t, service, account.ID, dollars.ID, "exchange", "5.00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reversePart(service, staff(), exchanged.Operation.ID, "split-exchange", "1.00"); !errors.Is(err, wallet.ErrNotPartiallyReversible) {
		t.Fatalf("giving back part of an exchange answered %v, want ErrNotPartiallyReversible", err)
	}

	part, err := reversePart(service, staff(), funded.Operation.ID, "part", "1.00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reversePart(service, staff(), part.Operation.ID, "part-of-part", "0.50"); !errors.Is(err, wallet.ErrNotReversible) {
		t.Fatalf("giving back part of a partial reversal answered %v, want ErrNotReversible", err)
	}

	whole := deposit(t, service, account.ID, "whole", "2.00")
	if _, err := service.Reverse(ctx, staff(), wallet.ReverseRequest{
		IdempotencyKey: "reverse-whole", OperationID: whole.Operation.ID, Reason: "asked",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reversePart(service, staff(), whole.Operation.ID, "part-of-reversed", "1.00"); !errors.Is(err, wallet.ErrAlreadyReversed) {
		t.Fatalf("giving back part of what was reversed whole answered %v, want ErrAlreadyReversed", err)
	}

	proposed := pend(t, service, account.ID, "proposed", "1.00")
	if _, err := reversePart(service, staff(), proposed.Operation.ID, "part-of-pending", "0.50"); !errors.Is(err, wallet.ErrNotSettled) {
		t.Fatalf("giving back part of what never counted answered %v, want ErrNotSettled", err)
	}
}

func TestPartialReversalsRacingNeverGiveBackMoreThanMoved(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		service := wallet.NewWalletService(db, nil, nil, nil)
		account := openWallet(t, service, "user-1", "main", 2)
		funded := deposit(t, service, account.ID, "fund", "10.00")
		deposit(t, service, account.ID, "more", "100.00")

		// Twelve parts of 2.00 against 10.00: exactly five fit.
		const askers = 12
		var (
			start, done sync.WaitGroup
			mu          sync.Mutex
			succeeded   int
			other       []error
		)
		start.Add(1)
		done.Add(askers)
		for i := range askers {
			go func() {
				defer done.Done()
				start.Wait()
				_, err := reversePart(service, staff(), funded.Operation.ID, fmt.Sprintf("part-%d", i), "2.00")
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					succeeded++
				case errors.Is(err, wallet.ErrExceedsReversible):
				default:
					other = append(other, err)
				}
			}()
		}
		start.Done()
		done.Wait()

		for _, err := range other {
			t.Errorf("a partial reversal failed for a reason other than the cap: %v", err)
		}
		if succeeded != 5 {
			t.Fatalf("%d partial reversals of 2.00 succeeded against 10.00, want exactly 5", succeeded)
		}
		if got := balanceOf(t, service, account.ID); got != 10000 {
			t.Fatalf("the balance is %d, want the 10000 deposited apart from what was given back", got)
		}
		if ledger := ledgerOf(t, service, account.ID); ledger != 10000 {
			t.Fatalf("the ledger sums to %d, want 10000", ledger)
		}
	})
}

// reversePartForced gives back part of an operation with the floor lowered,
// and answers with whatever the service said.
func reversePartForced(service *wallet.WalletService, actor security.Subject, operationID, key, amount string) (wallet.Receipt, error) {
	return service.ReversePartially(context.Background(), actor, wallet.PartialReversalRequest{
		IdempotencyKey: key, OperationID: operationID, Amount: amount, Force: true,
		Reason: "refund owed whatever the payee spent",
	})
}

func TestAForcedPartialReversalGoesPastWhatThePayeeStillHolds(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		ctx := context.Background()
		service := wallet.NewWalletService(db, nil, nil, nil)
		payer := openWallet(t, service, "user-1", "main", 2)
		payee := openWallet(t, service, "user-2", "main", 2)
		deposit(t, service, payer.ID, "fund", "10.00")
		paid, err := payment(t, service, payer.ID, payee.ID, "pay", "10.00")
		if err != nil {
			t.Fatal(err)
		}
		// The payee spends most of what arrived before the refund is asked.
		if err := withdraw(t, service, staff(), payee.ID, "spent", "8.00", false); err != nil {
			t.Fatal(err)
		}

		// Unforced, the wallet the money leaves is held to its balance, and the
		// refusal leaves nothing written: no money, and nothing counted against
		// the cap.
		if _, err := reversePart(service, staff(), paid.Operation.ID, "back-unforced", "4.00"); !errors.Is(err, wallet.ErrInsufficientFunds) {
			t.Fatalf("giving back 4.00 from a payee holding 2.00 answered %v, want ErrInsufficientFunds", err)
		}
		if got := balanceOf(t, service, payee.ID); got != 200 {
			t.Fatalf("the refused partial reversal left the payee with %d, want 200", got)
		}
		original, err := service.OperationByKey(ctx, staff(), "pay")
		if err != nil {
			t.Fatal(err)
		}
		if original.Operation.ReversedAmount != 0 {
			t.Fatalf("the refused partial reversal counted %d against the cap, want 0", original.Operation.ReversedAmount)
		}

		// A holder asking for force is refused, and nothing moves.
		if _, err := reversePartForced(service, person("user-2"), paid.Operation.ID, "back-holder", "4.00"); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("a holder forcing a partial reversal answered %v, want ErrForbidden", err)
		}

		forced, err := reversePartForced(service, staff(), paid.Operation.ID, "back-forced", "4.00")
		if err != nil {
			t.Fatalf("the operator forcing 4.00 back: %v", err)
		}
		if got := balanceOf(t, service, payee.ID); got != -200 {
			t.Fatalf("the payee holds %d after the forced partial reversal, want -200", got)
		}
		if got := balanceOf(t, service, payer.ID); got != 400 {
			t.Fatalf("the payer holds %d, want 400", got)
		}
		again, err := reversePartForced(service, staff(), paid.Operation.ID, "back-forced", "4.00")
		assertReplayed(t, forced, again, err)

		// Force lowers the floor and leaves the cap: 6.00 is what is left of
		// the payment, and not a minor unit more comes back.
		if _, err := reversePartForced(service, staff(), paid.Operation.ID, "back-too-much", "6.01"); !errors.Is(err, wallet.ErrExceedsReversible) {
			t.Fatalf("forcing past what is left answered %v, want ErrExceedsReversible", err)
		}
		if _, err := reversePartForced(service, staff(), paid.Operation.ID, "back-rest", "6.00"); err != nil {
			t.Fatalf("forcing back the 6.00 left: %v", err)
		}
		if got := balanceOf(t, service, payee.ID); got != -800 {
			t.Fatalf("the payee holds %d after everything came back, want -800", got)
		}
		for _, id := range []string{payer.ID, payee.ID} {
			if ledger, balance := ledgerOf(t, service, id), balanceOf(t, service, id); ledger != balance {
				t.Fatalf("wallet %s: the ledger sums to %d and the balance says %d", id, ledger, balance)
			}
		}
	})
}

func TestForcedPartialReversalsRacingStillStopAtTheCap(t *testing.T) {
	t.Parallel()

	engines(t, func(t *testing.T, db *data.DB) {
		service := wallet.NewWalletService(db, nil, nil, nil)
		payer := openWallet(t, service, "user-1", "main", 2)
		payee := openWallet(t, service, "user-2", "main", 2)
		deposit(t, service, payer.ID, "fund", "10.00")
		paid, err := payment(t, service, payer.ID, payee.ID, "pay", "10.00")
		if err != nil {
			t.Fatal(err)
		}
		if err := withdraw(t, service, staff(), payee.ID, "spent", "10.00", false); err != nil {
			t.Fatal(err)
		}

		// Twelve forced parts of 2.00 against 10.00 from an empty payee: the
		// floor is out of the way, and the cap alone lets exactly five through.
		const askers = 12
		var (
			start, done sync.WaitGroup
			mu          sync.Mutex
			succeeded   int
			other       []error
		)
		start.Add(1)
		done.Add(askers)
		for i := range askers {
			go func() {
				defer done.Done()
				start.Wait()
				_, err := reversePartForced(service, staff(), paid.Operation.ID, fmt.Sprintf("part-%d", i), "2.00")
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					succeeded++
				case errors.Is(err, wallet.ErrExceedsReversible):
				default:
					other = append(other, err)
				}
			}()
		}
		start.Done()
		done.Wait()

		for _, err := range other {
			t.Errorf("a forced partial reversal failed for a reason other than the cap: %v", err)
		}
		if succeeded != 5 {
			t.Fatalf("%d forced partial reversals of 2.00 succeeded against 10.00, want exactly 5", succeeded)
		}
		if got := balanceOf(t, service, payee.ID); got != -1000 {
			t.Fatalf("the payee holds %d, want -1000", got)
		}
		if got := balanceOf(t, service, payer.ID); got != 1000 {
			t.Fatalf("the payer holds %d, want 1000", got)
		}
		if ledger := ledgerOf(t, service, payee.ID); ledger != -1000 {
			t.Fatalf("the payee's ledger sums to %d, want -1000", ledger)
		}
	})
}
