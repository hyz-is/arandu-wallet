package wallet

import (
	"strings"
	"testing"
)

// The addresses a screen links to, built where the view compiler can accept
// them: whole, in Go, before the page is drawn.
//
// These tests live beside the code because what they ask is unexported. The
// feature suite reads the same addresses off the handlers' answers, and holds
// that each one is where the old markup pointed; this file holds the two things
// that suite cannot reach -- what happens to an identifier that is not made of
// letters, digits and dashes, and what a name that is not a route answers.

// TestEveryRouteAddressIsItsSuffixUnderThePrefix fills every route the module
// registers and requires the prefix followed by the suffix, with each parameter
// in its place. An identifier of the shape the package generates is written as
// it is, which is what keeps a republished screen linking where it did.
func TestEveryRouteAddressIsItsSuffixUnderThePrefix(t *testing.T) {
	t.Parallel()

	const prefix = "/money"
	const id = "8ca456de-dd2c-4ec2-a827-4f9d4939377d"
	for _, route := range routePatterns {
		var params []string
		want := route.suffix
		for _, segment := range strings.Split(route.suffix, "/") {
			if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
				params = append(params, id)
				want = strings.Replace(want, segment, id, 1)
			}
		}
		if got := routeAddress(prefix, route.name, params...); got != prefix+want {
			t.Errorf("%s is linked as %q, want %q", route.name, got, prefix+want)
		}
	}
}

// TestARouteAddressEscapesEachParameterAsOneSegment is the reason the address
// is built here and not concatenated: an identifier is data, and a slash, a
// question mark or a hash in one would end its segment and send the link to
// another route, a query, or a fragment.
func TestARouteAddressEscapesEachParameterAsOneSegment(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ name, id, want string }{
		{"wallet.show", "a/b", "/wallet/a%2Fb"},
		{"wallet.entries", "a?b", "/wallet/a%3Fb/entries"},
		{"wallet.deposit", "a#b", "/wallet/a%23b/deposits"},
		{"wallet.show", "a b", "/wallet/a%20b"},
		{"wallet.show", "../closure", "/wallet/..%2Fclosure"},
	} {
		if got := routeAddress("/wallet", c.name, c.id); got != c.want {
			t.Errorf("%s with %q is linked as %q, want %q", c.name, c.id, got, c.want)
		}
	}
}

// TestARouteAddressInventsNothing holds the refusal: a name that is not a route,
// or parameters that do not fit its suffix, answer nothing rather than a guess.
func TestARouteAddressInventsNothing(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		params []string
	}{
		{"wallet.nowhere", nil},
		{"wallet.show", nil},
		{"wallet.index", []string{"extra"}},
		{"wallet.named", []string{"only-the-holder"}},
	} {
		if got := routeAddress("/wallet", c.name, c.params...); got != "" {
			t.Errorf("%s with %q is linked as %q, want nothing", c.name, c.params, got)
		}
	}
}

// TestTheNextPageCarriesItsQueryEscaped holds the two cursor links. The holder
// is whatever somebody typed into the search box, so an ampersand in it would
// otherwise start a parameter of its own; a cursor is an identifier, and is
// written as it is.
func TestTheNextPageCarriesItsQueryEscaped(t *testing.T) {
	t.Parallel()

	const cursor = "28d81fe7-d57b-4751-9f7f-75ffbd61ef69"
	for _, c := range []struct{ label, got, want string }{
		{"a listing narrowed to nobody", nextListingURL("/money", "", cursor), "/money?holder_id=&cursor=" + cursor},
		{"a listing narrowed to a holder", nextListingURL("/money", "alice", cursor), "/money?holder_id=alice&cursor=" + cursor},
		{"a holder with an ampersand", nextListingURL("/money", "a&b=c #d", cursor), "/money?holder_id=a%26b%3Dc+%23d&cursor=" + cursor},
		{"the last page of a listing", nextListingURL("/money", "alice", ""), ""},
		{"a ledger", nextStatementURL("/money", "w-1", cursor), "/money/w-1/entries?cursor=" + cursor},
		{"the last page of a ledger", nextStatementURL("/money", "w-1", ""), ""},
	} {
		if c.got != c.want {
			t.Errorf("%s links its next page as %q, want %q", c.label, c.got, c.want)
		}
	}
}

// TestAWalletRowCarriesItsTwoAddresses holds the row every screen reads its
// links from, and the empty row a missing wallet becomes.
func TestAWalletRowCarriesItsTwoAddresses(t *testing.T) {
	t.Parallel()

	row := walletRow("/money", &Wallet{ID: "w-1"})
	if row.URL != "/money/w-1" || row.StatementURL != "/money/w-1/entries" {
		t.Errorf("the row links to %q and %q, want /money/w-1 and /money/w-1/entries", row.URL, row.StatementURL)
	}
	if empty := walletRow("/money", nil); empty != (WalletRow{}) {
		t.Errorf("a missing wallet is drawn as %+v, want the empty row", empty)
	}
}

// TestAFormKeyNamesExactlyOneForm holds the part of a form key the feature suite
// cannot reach: the parts are signed with their lengths, so a route and a target
// that run into each other differently are two keys, and a page with no token
// mints none.
func TestAFormKeyNamesExactlyOneForm(t *testing.T) {
	t.Parallel()

	const token = "nonce.1791600050.signature"
	if formKey(token, "wallet.deposit", "ab") == formKey(token, "wallet.deposita", "b") {
		t.Error("two forms whose parts concatenate to the same bytes were given one key")
	}
	if formKey(token, "wallet.refund", "a", "b") == formKey(token, "wallet.refund", "ab") {
		t.Error("a refund of two lines and a refund of one line named by both were given one key")
	}
	if formKey(token, "wallet.deposit", "w1") != formKey(token, "wallet.deposit", "w1") {
		t.Error("one form on one page was given two keys, so a second submission of it would move money again")
	}
	if got := formKey("", "wallet.deposit", "w1"); got != "" {
		t.Errorf("a page drawn without a token minted %q, which would be one key for everybody", got)
	}
	if key := formKey(token, "wallet.deposit", "w1"); len(key) > maxIdempotencyKeyLen {
		t.Errorf("a form key is %d bytes and the column holds %d", len(key), maxIdempotencyKeyLen)
	}
}
