package wallet

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// MaxMetaBytes is the largest a movement's metadata may be once written.
//
// A bound rather than none, because the column travels with every row of a
// ledger that only grows: a caller that attached a document to each of ten
// thousand movements would have written a document store nobody chose, inside
// the table a statement is paged from.
const MaxMetaBytes = 4096

// MaxMetaKeys is how many names one metadata value may carry.
const MaxMetaKeys = 32

// The refusals about metadata. They are separate values because each is a
// different thing to fix: too much of it is the caller's payload, and a column
// that cannot be read is the row.
var (
	// ErrMetaTooLarge is returned when metadata does not fit in the column.
	ErrMetaTooLarge = fmt.Errorf("wallet: the metadata is larger than %d bytes once written", MaxMetaBytes)
	// ErrMetaTooManyKeys is returned when metadata carries more names than a
	// movement may.
	ErrMetaTooManyKeys = fmt.Errorf("wallet: the metadata carries more than %d names", MaxMetaKeys)
	// ErrMetaUnreadable is returned when the column holds something that is not
	// the object this package writes.
	ErrMetaUnreadable = fmt.Errorf("wallet: the metadata column does not hold an object of names and text")
)

// Meta is what the application attaches to a movement: its own facts about
// what the money was for.
//
// Names to text, and never to numbers or nested values. A JSON number read back
// in Go is a float64, and a float is the one thing this package keeps away from
// money -- so an amount, a rate or a quantity written here would come back as
// something that no longer adds up, in the row that exists to explain a
// movement. An application with a structure to attach writes it as text under
// one name, where it is the application's to parse and this package's only to
// carry.
//
// Nothing here is read by this package. It is not indexed, not searched and not
// compared: it is what a receipt shows and what an export carries, and every
// decision about the money is made from the columns beside it.
type Meta map[string]string

// Validate reports why this metadata cannot be stored, and nil when it can.
func (m Meta) Validate() error {
	if len(m) == 0 {
		return nil
	}
	if len(m) > MaxMetaKeys {
		return fmt.Errorf("%w: it carries %d", ErrMetaTooManyKeys, len(m))
	}
	encoded, err := m.encode()
	if err != nil {
		return err
	}
	if len(encoded) > MaxMetaBytes {
		return fmt.Errorf("%w: it is %d", ErrMetaTooLarge, len(encoded))
	}
	return nil
}

// Names are the names this metadata carries, sorted.
//
// Sorted because a screen and an export both read them in order, and a Go map
// has none: two renderings of one row would otherwise differ in the order of
// their own lines.
func (m Meta) Names() []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// encode writes the metadata as the text the column holds.
func (m Meta) encode() (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	// The encoder sorts a map's names, so one value is one string whoever
	// writes it: a row that could be written two ways is a row two exports
	// disagree about.
	body, err := json.Marshal(map[string]string(m))
	if err != nil {
		return "", fmt.Errorf("wallet: writing the metadata: %w", err)
	}
	return string(body), nil
}

// Value writes the metadata as the text the column holds, and the empty string
// where there is none.
//
// Text rather than a document type: the engines spell one differently and only
// some of them have it, and nothing in this package reads what is inside. A
// column an application wants to query is a column it adds to a table of its
// own, against rows it owns.
func (m Meta) Value() (driver.Value, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m.encode()
}

// Scan reads the metadata back from whatever an engine answers with.
//
// An empty column is no metadata rather than an empty object, because that is
// what every row written before this column existed holds, and a movement
// nobody attached anything to is not a movement with an empty note on it.
func (m *Meta) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*m = nil
		return nil
	case []byte:
		return m.scanText(string(v))
	case string:
		return m.scanText(v)
	}
	return fmt.Errorf("%w: it answered with a %T", ErrMetaUnreadable, value)
}

// scanText reads the metadata an engine answered as text.
func (m *Meta) scanText(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		*m = nil
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return fmt.Errorf("%w: %v", ErrMetaUnreadable, err)
	}
	if len(out) == 0 {
		*m = nil
		return nil
	}
	*m = out
	return nil
}

// metaFrom reads the metadata a request wrote under one field.
//
// The form spells it the way a browser sends a group of inputs: meta[colour],
// meta[order], one field per name. It is read out of the parsed form rather
// than asked for by name, because the names belong to the application and this
// package has never heard of them -- asking for one would be asking the caller
// to declare here what it already declared in its own markup.
//
// An empty value is kept. A form that submits an input somebody left blank has
// said something about that name, and dropping it would make the row disagree
// with the screen it came from.
func metaFrom(values url.Values, field string) Meta {
	prefix := field + "["
	out := Meta{}
	for key, carried := range values {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, "]") {
			continue
		}
		name := key[len(prefix) : len(key)-1]
		if name == "" || len(carried) == 0 {
			continue
		}
		out[name] = carried[0]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
