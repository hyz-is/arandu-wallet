package unit_test

import (
	"strings"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What a wallet is called when nobody wrote a slug.
//
// The fold is deliberately small: it keeps letters and digits, lowers the ASCII
// ones, and turns every run of anything else into a single hyphen. What it
// refuses to do is translate -- a table that mapped one script into another
// would be a table this package would have to keep correct for every language
// its users write in, and getting it wrong renames somebody's wallet.
func TestSlugifyKeepsWhatTheCallerWrote(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"main", "Bonus", "a_b", "already-a-slug", "ünïcode"} {
		if got := wallet.Slugify(slug, "Something Else Entirely"); got != slug {
			t.Errorf("Slugify(%q, ...) = %q, and a slug somebody wrote is their key", slug, got)
		}
	}
}

func TestSlugifyDerivesFromTheName(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ name, want string }{
		{"Main", "main"},
		{"Store Credit", "store-credit"},
		{"  Store   Credit  ", "store-credit"},
		{"Cashback 2026", "cashback-2026"},
		{"R$ Reais", "r-reais"},
		{"--Main--", "main"},
		{"a/b\\c", "a-b-c"},
		{"", ""},
		{"!!!", ""},
	} {
		if got := wallet.Slugify("", c.name); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestSlugifyStopsAtTheColumnWidth holds the one thing a derived slug must not
// do: be longer than the column it is written into, or end on the hyphen the
// truncation left.
func TestSlugifyStopsAtTheColumnWidth(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("word ", 40)
	got := wallet.Slugify("", long)
	if len(got) > 64 {
		t.Fatalf("a derived slug is %d characters long", len(got))
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("a derived slug ends on the hyphen the truncation left: %q", got)
	}
}
