package unit_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/arandu-io/hesape/translation"

	wallet "github.com/hyz-is/arandu-wallet"
)

// The catalogue this package ships, and the two properties that make it worth
// shipping.
//
// The first is that every locale says the same things: a locale added with half
// the lines is a screen that draws its own keys in that language, and nothing
// else says so. The second is that nothing this package can render is missing a
// line -- the kinds an operation can be are a closed set written in Go, so a
// kind added without a sentence is a statement with a bare identifier in it.

func TestEveryLocaleShipsEveryLine(t *testing.T) {
	t.Parallel()

	locales := wallet.Locales()
	if len(locales) < 2 {
		t.Fatalf("the package ships %d locale(s), so a missing line in one of them would be invisible", len(locales))
	}
	if !slices.Contains(locales, wallet.FallbackLocale) {
		t.Fatalf("the fallback locale %q ships no catalogue, so a key missing everywhere else would draw as itself",
			wallet.FallbackLocale)
	}

	// The set of keys, not the sentences.
	reference := keysOf(t, wallet.Lines(wallet.FallbackLocale))
	if len(reference) == 0 {
		t.Fatal("the fallback locale holds no line, so every check below would pass by having nothing to read")
	}
	for _, locale := range locales {
		got := keysOf(t, wallet.Lines(locale))
		if len(got) == 0 {
			t.Errorf("%s ships no line at all", locale)
			continue
		}
		for _, key := range reference {
			if !slices.Contains(got, key) {
				t.Errorf("%s is missing %s", locale, key)
			}
		}
		for _, key := range got {
			if !slices.Contains(reference, key) {
				t.Errorf("%s ships %s, which %s does not, so nothing reads it as a fallback",
					locale, key, wallet.FallbackLocale)
			}
		}
	}
}

func TestEveryKindThePackageRendersHasASentence(t *testing.T) {
	t.Parallel()

	keys := keysOf(t, wallet.Lines(wallet.FallbackLocale))
	for _, kind := range []wallet.OperationKind{
		wallet.OperationDeposit, wallet.OperationWithdraw, wallet.OperationTransfer,
		wallet.OperationExchange, wallet.OperationReversal, wallet.OperationConfirmation,
		wallet.OperationPurchase, wallet.OperationRefund,
	} {
		want := wallet.TranslationGroup + ".kind." + string(kind)
		if !slices.Contains(keys, want) {
			t.Errorf("the operation kind %q has no sentence at %s", kind, want)
		}
	}
	for _, kind := range []wallet.EntryKind{wallet.EntryDeposit, wallet.EntryWithdraw} {
		want := wallet.TranslationGroup + ".direction." + string(kind)
		if !slices.Contains(keys, want) {
			t.Errorf("the direction %q has no sentence at %s", kind, want)
		}
	}
	for _, kind := range []wallet.PurchaseKind{
		wallet.PurchasePaid, wallet.PurchaseGift, wallet.PurchaseRefund,
	} {
		want := wallet.TranslationGroup + ".purchase." + string(kind)
		if !slices.Contains(keys, want) {
			t.Errorf("the purchase kind %q has no sentence at %s", kind, want)
		}
	}
}

func TestALocaleTheApplicationDidNotWriteFallsBackRatherThanBlank(t *testing.T) {
	t.Parallel()

	labels := module(t).Labels("fr")
	if got := labels.T("screen.index_title"); got == "" || strings.Contains(got, "screen.") {
		t.Fatalf("a locale this package does not ship drew %q, want the fallback's sentence", got)
	}

	// And a key nothing has anywhere comes back as itself, which is the one
	// answer that cannot be mistaken for a translation.
	if got := labels.T("screen.nothing_writes_this"); got != "wallet.screen.nothing_writes_this" {
		t.Fatalf("a key with no line drew %q, want the key", got)
	}
}

func TestTheCatalogueIsShippedAndNeverPublished(t *testing.T) {
	t.Parallel()

	// A view is meant to be edited, so the project takes ownership of the file.
	// A sentence is meant to keep up with the code that produces it, so a copy
	// in every project is a copy that goes stale without saying so.
	for _, path := range wallet.PublishedPaths() {
		if strings.Contains(path, "resources/lang") {
			t.Errorf("%s is published, and a copied catalogue goes stale one release at a time", path)
		}
	}
}

// keysOf is the keys of a set of lines, sorted.
func keysOf(t *testing.T, lines translation.Lines) []string {
	t.Helper()

	keys := make([]string, 0, len(lines))
	for key := range lines {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
