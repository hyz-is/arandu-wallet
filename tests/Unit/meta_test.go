package unit_test

import (
	"errors"
	"strings"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// What the metadata column holds, and what it refuses.
//
// Nothing in the package reads what is inside it, so the properties that matter
// are the ones about carrying: it comes back exactly as it went in, it is one
// string whoever writes it, and a value the column cannot hold is refused
// before a movement is recorded rather than by the write that would have stored
// it.

func TestMetadataComesBackExactlyAsItWentIn(t *testing.T) {
	t.Parallel()

	for _, meta := range []wallet.Meta{
		nil,
		{},
		{"one": "1"},
		{"invoice": "INV-7", "note": `a "quoted" line, with a comma`},
		{"unicode": "conversão à vista", "empty": ""},
	} {
		written, err := meta.Value()
		if err != nil {
			t.Fatalf("writing %v: %v", meta, err)
		}

		var read wallet.Meta
		if err := read.Scan(written); err != nil {
			t.Fatalf("reading %v back: %v", meta, err)
		}
		if len(read) != len(meta) {
			t.Fatalf("%v came back as %v", meta, read)
		}
		for name, line := range meta {
			if read[name] != line {
				t.Errorf("%q came back as %q, want %q", name, read[name], line)
			}
		}
	}
}

func TestMetadataIsOneStringWhoeverWritesIt(t *testing.T) {
	t.Parallel()

	// A Go map has no order, so the same value written twice would be two
	// strings if the encoding followed the map. Two exports of one row that
	// differ byte for byte are two rows as far as anything comparing them is
	// concerned.
	meta := wallet.Meta{"z": "last", "a": "first", "m": "middle", "b": "second"}
	first, err := meta.Value()
	if err != nil {
		t.Fatalf("writing: %v", err)
	}
	for range 32 {
		again, err := meta.Value()
		if err != nil {
			t.Fatalf("writing: %v", err)
		}
		if again != first {
			t.Fatalf("the same metadata was written as %q and as %q", first, again)
		}
	}

	names := meta.Names()
	if strings.Join(names, ",") != "a,b,m,z" {
		t.Fatalf("the names read %v, want them sorted", names)
	}
}

func TestNoMetadataAndEmptyMetadataAreTheSameAbsence(t *testing.T) {
	t.Parallel()

	// The column defaults to the empty string on every row written before it
	// existed, and a movement nobody attached anything to holds that too. An
	// empty object read back as an empty map would say that somebody attached
	// nothing, which is a different statement from saying nothing.
	var read wallet.Meta
	for _, stored := range []any{nil, "", []byte(""), "   ", "{}"} {
		if err := read.Scan(stored); err != nil {
			t.Fatalf("reading %v: %v", stored, err)
		}
		if read != nil {
			t.Errorf("%v came back as %v, want nothing", stored, read)
		}
	}
}

func TestAMetadataColumnThatCannotBeReadIsRefused(t *testing.T) {
	t.Parallel()

	var read wallet.Meta
	for _, stored := range []any{"not json", `["a"]`, `{"n":1}`, 42} {
		if err := read.Scan(stored); !errors.Is(err, wallet.ErrMetaUnreadable) {
			t.Errorf("reading %v answered %v, want ErrMetaUnreadable", stored, err)
		}
	}
}

func TestMetadataBeyondWhatAMovementMayCarryIsRefused(t *testing.T) {
	t.Parallel()

	big := wallet.Meta{"blob": strings.Repeat("x", wallet.MaxMetaBytes)}
	if err := big.Validate(); !errors.Is(err, wallet.ErrMetaTooLarge) {
		t.Errorf("a payload past the column answered %v, want ErrMetaTooLarge", err)
	}

	many := wallet.Meta{}
	for i := range wallet.MaxMetaKeys + 1 {
		many[string(rune('a'+i%26))+strings.Repeat("x", i)] = "1"
	}
	if err := many.Validate(); !errors.Is(err, wallet.ErrMetaTooManyKeys) {
		t.Errorf("more names than a movement may carry answered %v, want ErrMetaTooManyKeys", err)
	}

	// And a value that fits is not refused, so the bound is a bound rather than
	// a refusal of everything.
	if err := (wallet.Meta{"note": "fine"}).Validate(); err != nil {
		t.Errorf("an ordinary value was refused: %v", err)
	}
}
