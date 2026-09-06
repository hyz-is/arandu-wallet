package wallet

import (
	"embed"
	"io/fs"
	"strings"
	"time"

	"github.com/arandu-io/framework/foundation"
	"github.com/arandu-io/hesape/view"
)

// The view sources this package hands to the project that installs it.
//
// They are embedded rather than read off disk because what a module publishes
// is a tree it carries, not a directory it points at: the program that writes
// the files is the application's own binary, running somewhere this repository
// does not exist as files. Whatever is handed over has to already be inside the
// program doing the handing.
//
// The archive keeps them under a directory of its own, and the publication says
// where they came from and where they go. The two cannot be one path: an
// application looks for a package's views below a directory named vendor, and
// go mod publishes no file whose path carries a segment with that name -- a
// tree kept at the destination is in the repository and missing from what
// anybody downloads, and the pattern below then matches nothing.
//
//go:embed resources/publish
var viewSources embed.FS

// Where a view is kept, where it is written, and what it is called.
//
// viewSuffix is the extension: it ends in .go so the build tag on the first
// line keeps the compiler out of a file that is markup below the package
// clause.
const (
	// viewRoot is the directory every path in the archive starts with. It is
	// deliberately not the destination: go mod drops every file whose path
	// carries a segment named vendor, at any depth, so a tree kept under
	// resources/views/vendor is present in the repository and absent from the
	// published module -- and the embed above then matches nothing in any
	// project that imports this.
	viewRoot = "resources/publish"
	// viewPrefix is where the same files are written in a project: below the
	// directory an application keeps other people's views in, under this
	// module's own name. Two packages with a view called index are two files
	// under two names there, and neither shadows the other or the
	// application's own.
	viewPrefix = "resources/views/vendor/wallet"
	viewSuffix = ".kyse.go"
)

// compiledRoot is where the view compiler writes the Go it produces, mirroring
// the tree of the sources. It is build output: gitignored, rebuilt on demand,
// and never edited.
const compiledRoot = "storage/framework/views"

// The names the screens are rendered by.
//
// They are constants because each one is written in a handler and derived again
// from a path, and a page rendered by a name nothing registered is a 500 that
// says nothing about which of the two spellings was wrong. ViewNames derives the
// same set from the archive, and a test holds the two together.
const (
	// ViewIndex is the listing of a customer's wallets.
	ViewIndex = "vendor.wallet.index"
	// ViewStatement is one wallet's ledger, with the rates its exchanges were
	// made at and what its payments were charged.
	ViewStatement = "vendor.wallet.statement"
	// ViewOperations is one wallet and what may be done with its money.
	ViewOperations = "vendor.wallet.operations"
)

// Compile-time proof that every screen can be drawn inside the application's
// layout. The layout takes its data through this contract at render time, so a
// page that stopped answering it would be a page that renders as a 500 in
// somebody else's application rather than a build failure in this one.
var (
	_ view.Layout = IndexPageData{}
	_ view.Layout = StatementPageData{}
	_ view.Layout = OperationsPageData{}
)

// WalletRow is one wallet as a screen draws it.
//
// It is a snapshot and not the entity, for the reason Resource is one: a
// template handed the entity draws whatever fields it happens to have,
// including the ones somebody adds later without opening the markup -- and
// TenantID is exactly such a field.
//
// Every amount is text, already at the wallet's own scale, because a screen
// shows a number and does no arithmetic on it. Rendering minor units in the
// markup would put the decimal point in a template, which is the one place it
// can be wrong in a language nobody type-checks.
type WalletRow struct {
	// ID is what a link addresses and a form submits.
	ID string
	// HolderID is whose money this is, as the application named them.
	HolderID string
	// Slug is which of the holder's wallets this is, and Name what a person
	// calls it.
	Slug string
	Name string
	// Currency is what the balance counts.
	Currency string
	// Balance is what it holds, and CreditLimit how far below zero it may go,
	// both as decimals at this wallet's scale.
	Balance     string
	CreditLimit string
	// Negative says the balance is below zero, so a screen can show it as such
	// without comparing text.
	Negative bool
	// Created is when the wallet was opened.
	Created string
}

// EntryRow is one movement as a statement draws it.
type EntryRow struct {
	// ID is the ledger row, and Sequence its position in this wallet's history.
	ID       string
	Sequence string
	// Operation is the request it was part of, and OperationLabel what a person
	// reads in place of the kind of that request.
	Operation      string
	OperationLabel string
	// Direction is what a person reads in place of "in" or "out", and Incoming
	// says which of the two it is so a screen can colour it without comparing
	// text.
	Direction string
	Incoming  bool
	// Amount is how much moved and BalanceAfter what the wallet held once it
	// had, both as decimals at the wallet's scale.
	Amount       string
	BalanceAfter string
	// Settled says the movement counted. A row that did not is what was
	// proposed, and a statement that drew it the same way would be a statement
	// nobody could add up.
	Settled bool
	// Created is when it was written.
	Created string
}

// ConversionRow is the rate one operation applied, as a statement draws it.
type ConversionRow struct {
	// Operation is the exchange it belongs to.
	Operation string
	// From and To are the two sides, each with its own currency and scale.
	From string
	To   string
	// Rate is the exact fraction it was made at, and QuotedAt when that
	// fraction was obtained.
	Rate     string
	QuotedAt string
	// Exact says the rate divided evenly, and Remainder is what was left over
	// when it did not.
	Exact     bool
	Remainder string
}

// ChargeRow is what one operation charged beyond the money it moved, as a
// statement draws it.
type ChargeRow struct {
	// Operation is the payment it belongs to.
	Operation string
	// Requested is what the caller asked to move, Discount what the payer was
	// charged less, Base what the fee was computed from and Fee what was taken.
	Requested string
	Discount  string
	Base      string
	Fee       string
	// FeeWallet is where the fee went, and Deductible says who paid it.
	FeeWallet  string
	Deductible bool
}

// PurchaseRow is one line of a basket as a screen draws it.
type PurchaseRow struct {
	// ID is what a refund names.
	ID string
	// Kind is what a person reads in place of bought, given or refunded, and
	// Refunded says whether this row is one that gave something back.
	Kind     string
	Refunded bool
	// Product is what was bought, as the application names it, and Quantity how
	// many.
	Product  string
	Quantity string
	// Receiver is the wallet that was paid.
	Receiver string
	// Paid is what left the payer for this line and Fee what was charged on it.
	Paid string
	Fee  string
	// Created is when it was written.
	Created string
}

// IndexPageData is what the listing screen is handed.
type IndexPageData struct {
	view.Page

	// Prefix is where this module answers, so the markup composes its own
	// addresses instead of hard-coding one the configuration can change.
	Prefix string
	// Labels are the sentences this screen draws, resolved for the locale the
	// request asked for.
	Labels Labels
	// Holder is the holder the listing was narrowed to, echoed back into the
	// field so the box still says what is being looked at.
	Holder string
	// Rows are the wallets, and Next is the cursor of the following page, empty
	// on the last one.
	Rows []WalletRow
	Next string
}

// StatementPageData is what the ledger screen is handed.
type StatementPageData struct {
	view.Page

	Prefix string
	Labels Labels
	// Wallet is whose ledger this is.
	Wallet WalletRow
	// Rows are the movements, oldest first.
	Rows []EntryRow
	// Conversions are the rates the operations on this page applied, and
	// Charges what they charged. Both go beside the movements and not inside
	// them, because each belongs to an operation and an operation writes an
	// entry on two or three wallets -- repeating one on every line would be
	// repeating one fact until two copies of it could differ.
	Conversions []ConversionRow
	Charges     []ChargeRow
	// Next is the cursor of the following page, empty on the last one.
	Next string
}

// OperationsPageData is what the screen that moves one wallet's money is
// handed.
type OperationsPageData struct {
	view.Page

	Prefix string
	Labels Labels
	// Wallet is the one being operated on.
	Wallet WalletRow
	// Purchases are the newest lines it bought, so that the identifier a refund
	// names is on the screen the refund is asked from.
	Purchases []PurchaseRow
	// MaySetCredit says whether the person reading may change how far below
	// zero this wallet goes. It is answered by the policy before the page is
	// drawn, so a control nobody may use is not drawn at all -- a button that
	// answers 403 is a button that teaches somebody the page is broken.
	MaySetCredit bool
}

// FormState is what a kyse input asks for its message and for what was typed.
//
// It exists because the component library asks for FieldError and the page the
// framework carries answers First. One adapter, in one place, rather than the
// same three lines on every screen -- and it is a type rather than a method on
// each page so that a screen added later cannot forget to write it.
type FormState struct{ view.Page }

// FieldError is the first message for an input, and empty for an input nothing
// rejected.
func (f FormState) FieldError(name string) string { return f.First(name) }

// Form is the state the inputs of this screen read.
func (d IndexPageData) Form() FormState { return FormState{Page: d.Page} }

// Form is the state the inputs of this screen read.
func (d StatementPageData) Form() FormState { return FormState{Page: d.Page} }

// Form is the state the inputs of this screen read.
func (d OperationsPageData) Form() FormState { return FormState{Page: d.Page} }

// walletRow snapshots one wallet for a screen.
func walletRow(record *Wallet) WalletRow {
	if record == nil {
		return WalletRow{}
	}
	return WalletRow{
		ID:          record.ID,
		HolderID:    record.HolderID,
		Slug:        record.Slug,
		Name:        record.Name,
		Currency:    string(record.Currency),
		Balance:     record.Balance.Format(record.DecimalPlaces),
		CreditLimit: record.CreditLimit.Format(record.DecimalPlaces),
		Negative:    record.Balance < 0,
		Created:     record.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// walletRows snapshots a listing for a screen.
func walletRows(records []*Wallet) []WalletRow {
	out := make([]WalletRow, 0, len(records))
	for _, record := range records {
		out = append(out, walletRow(record))
	}
	return out
}

// statementRows snapshots a page of one wallet's ledger for a screen.
//
// It takes the statement rather than the entries, because an entry is not
// readable on its own: the scale it is written at belongs to the wallet, and
// what it was part of belongs to the operation. Both travel in the statement,
// so this is one argument instead of three that could disagree.
func statementRows(labels Labels, statement Statement) []EntryRow {
	places := statement.Wallet.DecimalPlaces
	out := make([]EntryRow, 0, len(statement.Entries))
	for _, entry := range statement.Entries {
		if entry == nil {
			continue
		}
		out = append(out, EntryRow{
			ID:             entry.ID,
			Sequence:       formatInt(entry.Sequence),
			Operation:      entry.OperationID,
			OperationLabel: labels.Operation(statement.Operations[entry.OperationID].Kind),
			Direction:      labels.Entry(entry.Kind),
			Incoming:       entry.Kind == EntryDeposit,
			Amount:         entry.Amount.Format(places),
			BalanceAfter:   entry.BalanceAfter.Format(places),
			Settled:        bool(entry.Settled),
			Created:        entry.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// conversionRows snapshots the rates a page of movements was made at, in the
// order the movements name them so a page reads the same twice.
func conversionRows(statement Statement) []ConversionRow {
	out := []ConversionRow{}
	seen := make(map[string]bool, len(statement.Entries))
	for _, entry := range statement.Entries {
		if entry == nil || seen[entry.OperationID] {
			continue
		}
		seen[entry.OperationID] = true
		record, ok := statement.Conversions[entry.OperationID]
		if !ok {
			continue
		}
		out = append(out, ConversionRow{
			Operation: record.OperationID,
			From:      record.From().String(),
			To:        record.To().String(),
			Rate:      record.Rate().String(),
			QuotedAt:  record.QuotedAt.UTC().Format(time.RFC3339),
			Exact:     record.Exact(),
			Remainder: formatInt(record.RemainderNumerator) + "/" + formatInt(record.RemainderDenominator),
		})
	}
	return out
}

// chargeRows snapshots what a page of movements was charged, in the order the
// movements name them.
func chargeRows(statement Statement) []ChargeRow {
	out := []ChargeRow{}
	seen := make(map[string]bool, len(statement.Entries))
	for _, entry := range statement.Entries {
		if entry == nil || seen[entry.OperationID] {
			continue
		}
		seen[entry.OperationID] = true
		record, ok := statement.Charges[entry.OperationID]
		if !ok {
			continue
		}
		out = append(out, ChargeRow{
			Operation:  record.OperationID,
			Requested:  record.Requested().String(),
			Discount:   record.Discount.Format(record.DecimalPlaces),
			Base:       record.Base().String(),
			Fee:        record.Fee().String(),
			FeeWallet:  record.FeeWalletID,
			Deductible: bool(record.FeeDeductible),
		})
	}
	return out
}

// purchaseRows snapshots what a wallet bought for a screen.
func purchaseRows(labels Labels, records []*Purchase) []PurchaseRow {
	out := make([]PurchaseRow, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		out = append(out, PurchaseRow{
			ID:       record.ID,
			Kind:     labels.Purchase(record.Kind),
			Refunded: record.Kind == PurchaseRefund,
			Product:  record.ProductKey,
			Quantity: formatInt(int64(record.Quantity)),
			Receiver: record.ReceiverWalletID,
			Paid:     record.PaidAmount.Format(record.DecimalPlaces),
			Fee:      record.FeeAmount.Format(record.DecimalPlaces),
			Created:  record.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// formatInt is a whole number as a screen shows it.
//
// Here rather than in the markup, because a template that formatted numbers
// would be a second place a number is turned into text and the two would drift.
func formatInt(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	digits := ""
	for value != 0 {
		digit := value % 10
		if digit < 0 {
			digit = -digit
		}
		digits = string(rune('0'+digit)) + digits
		value /= 10
	}
	if negative {
		return "-" + digits
	}
	return digits
}

// Publishes declares the files this package offers, each at the path it takes
// relative to the root of the project.
//
// One tree, under one tag. A publication may carry a page, a component, a
// configuration file, a migration, a catalogue of sentences or an asset, and
// this package offers the first of those and nothing else. Each absence is a
// decision rather than an omission:
//
//   - configuration is the Config struct New is handed, checked by the compiler
//     and validated before the module exists. A file copied into the project
//     beside it would be a second place to say the same thing, and only one of
//     the two could be the one the code reads.
//   - a stylesheet, a script or any other asset is registered with the view
//     layer and served from an address derived from its own bytes. Copying one
//     into the project would put a second copy of those bytes under a second
//     address, and a page can only reference one of them.
//   - translations are overridden by writing the lines the application wants
//     into its own vendor tree, which the catalogue loader already reads. A
//     copy of every line this package ships is a copy that goes stale, and it
//     goes stale without saying so.
//   - migrations are declared and collected, never copied. A copy in the
//     project's own migration directory is found by the runner as well, so one
//     schema change applies twice under two names.
//
// What is left is the markup, and it is here for the reason the others are not:
// it is the one thing the project is expected to edit. A package cannot know
// what a screen should say in a product it has never seen.
func (m *Module) Publishes() []foundation.Publication {
	// From and To are what keep the archive and the destination two different
	// paths, and two is what they have to be: the destination carries a segment
	// named vendor, and go mod publishes no file whose path does.
	return []foundation.Publication{{
		Tag:   foundation.PublishView,
		Files: viewSources,
		From:  viewRoot,
		To:    viewPrefix,
	}}
}

// PublishedPaths are where this package's views are written, each relative to
// the root of the application, sorted.
//
// They are the destination rather than the archive, so that what the module
// refuses at boot and what a project ends up holding are one list.
func PublishedPaths() []string { return append([]string(nil), publishedPaths...) }

// ViewNames are the names the published views are rendered by, sorted.
//
// The name is the path a view is written at, below the project's view
// directory, with its separators turned into dots -- which is what the view
// compiler writes into the registration call. It is derived from the same
// archive the publication carries, so a view that was renamed cannot keep an
// old name here.
func ViewNames() []string { return append([]string(nil), viewNames...) }

// ViewPackages are the directories the compiled views land in, each relative to
// the root of the application, sorted and without repeats.
//
// Importing one is what puts its views in the binary: a compiled view calls the
// registry from init(), and a package nothing imports is not linked at all. The
// import is named rather than written into bootstrap/app.go, because one line
// somebody reads beats a file that changed while they were not looking.
func ViewPackages() []string {
	var out []string
	seen := make(map[string]bool)
	for _, path := range publishedPaths {
		// The paths already carry the destination, so what is cut off is the
		// directory a project keeps its views in, not the archive's root.
		dir := compiledRoot + strings.TrimPrefix(path[:strings.LastIndexByte(path, '/')], "resources/views")
		if seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

// The archive, read once at load.
//
// A failure here is a broken binary rather than a condition to recover from:
// the files are compiled in, so either they are all there or the build that
// produced this program was wrong.
var publishedPaths, viewNames = readArchive()

func readArchive() (paths, names []string) {
	err := fs.WalkDir(viewSources, viewRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, viewSuffix) {
			return nil
		}
		paths = append(paths, publishedPath(path))
		names = append(names, viewName(path))
		return nil
	})
	if err != nil {
		panic("wallet: reading the embedded views: " + err.Error())
	}
	if len(paths) == 0 {
		panic("wallet: the embedded view directory holds no view, so every name this package renders would be missing and nothing would say so")
	}
	return paths, names
}

// publishedPath turns an archive path into the path the file is written at.
//
// The archive keeps the views under a directory with no vendor segment, because
// go mod publishes no path that has one. The destination has one, because that
// is where an application looks for a package's views.
func publishedPath(path string) string {
	return viewPrefix + strings.TrimPrefix(path, viewRoot)
}

// viewName turns an archive path into the name the view is registered under.
//
// The name comes from where the file is written and not from where it is kept:
// the view compiler reads the project's tree, and the registration call it
// writes says the path it found there.
//
//	resources/publish/index.kyse.go -> vendor.wallet.index
func viewName(path string) string {
	name := strings.TrimPrefix(publishedPath(path), "resources/views/")
	name = strings.TrimSuffix(name, viewSuffix)
	return strings.ReplaceAll(name, "/", ".")
}
