package feature_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/arandu-io/framework/security"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What a rate provider could not do, told apart by the caller.
//
// The values are this package's, so an application tests them against the
// package it already imports rather than against whichever provider it happens
// to be wired to. What is asserted here is that they survive the journey: the
// service names the pair it was quoting and wraps what the provider said, so
// errors.Is still answers at the far end.

// refusingRate answers every call with one wrapped failure.
type refusingRate struct{ because error }

func (p refusingRate) Rate(_ context.Context, _ security.Grant, from, to wallet.Currency) (wallet.Rate, error) {
	return wallet.Rate{}, fmt.Errorf("acme rates: %s/%s: %w", from, to, p.because)
}

func TestWhatAQuoteProviderCouldNotDoReachesTheCaller(t *testing.T) {
	t.Parallel()

	for _, because := range []error{
		wallet.ErrRatePairUnknown,
		wallet.ErrRateProviderUnavailable,
		wallet.ErrRateMomentUnsupported,
		wallet.ErrRateCacheFailed,
		wallet.ErrRateRequestRefused,
	} {
		t.Run(because.Error(), func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			service := wallet.NewWalletService(database(t), refusingRate{because}, nil, nil)
			source := openIn(t, service, "user-1", "main", "USD", 2)
			target := openIn(t, service, "user-2", "main", "BRL", 2)
			deposit(t, service, source.ID, "opening", "100.00")

			_, err := service.Transfer(ctx, staff(), wallet.TransferRequest{
				IdempotencyKey: "exchange-1",
				FromWalletID:   source.ID,
				ToWalletID:     target.ID,
				Amount:         "10.00",
			})
			if !errors.Is(err, because) {
				t.Fatalf("the transfer answered %v, and the provider said %v", err, because)
			}

			// And nothing moved on either side.
			if got := balanceOf(t, service, source.ID); got != 10000 {
				t.Errorf("the source holds %d, want 10000", got)
			}
			if got := balanceOf(t, service, target.ID); got != 0 {
				t.Errorf("the target holds %d, want 0", got)
			}
		})
	}
}

// TestAnUnclassifiedQuoteFailureTravelsOutUnchanged holds the other half: a
// provider that wraps none of the five is not guessed at.
func TestAnUnclassifiedQuoteFailureTravelsOutUnchanged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := wallet.NewWalletService(database(t), unavailableRate{}, nil, nil)
	source := openIn(t, service, "user-1", "main", "USD", 2)
	target := openIn(t, service, "user-2", "main", "BRL", 2)
	deposit(t, service, source.ID, "opening", "100.00")

	_, err := service.Transfer(ctx, staff(), wallet.TransferRequest{
		IdempotencyKey: "exchange-1", FromWalletID: source.ID, ToWalletID: target.ID, Amount: "10.00",
	})
	if !errors.Is(err, errRateUnavailable) {
		t.Fatalf("the transfer answered %v, want the provider's own error", err)
	}
	for _, sentinel := range []error{
		wallet.ErrRatePairUnknown,
		wallet.ErrRateProviderUnavailable,
		wallet.ErrRateMomentUnsupported,
		wallet.ErrRateCacheFailed,
		wallet.ErrRateRequestRefused,
	} {
		if errors.Is(err, sentinel) {
			t.Errorf("an unclassified failure was read as %v", sentinel)
		}
	}
}
