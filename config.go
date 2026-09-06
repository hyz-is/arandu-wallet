package wallet

import (
	"fmt"
	"net/http"

	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/translation"
)

// The defaults for the optional settings. They are constants rather than
// literals inside Config.withDefaults, so the value a reader finds here is the
// value the package uses.
const (
	// DefaultPrefix is where the routes are mounted when Config leaves Prefix
	// empty.
	DefaultPrefix = "/wallet"
	// DefaultPageSize is how many records one page answers with when Config
	// leaves PageSize at zero.
	DefaultPageSize = 25
	// MaxPageSize is the ceiling PageSize is refused above. A page nobody
	// bounded is a page that reads the whole table on the day the table is
	// large.
	MaxPageSize = 200
)

// Config is what the application passes when it wires this package.
//
// A typed struct rather than a map: a misspelled key in a map is a setting that
// silently keeps its default, and the failure shows up as behaviour nobody
// asked for rather than as an error. Here a field that does not exist does not
// compile.
type Config struct {
	// Tenant is the customer a visitor with no session is read as.
	//
	// It is required, and it comes from the application's own configuration --
	// never from the request. A tenant a visitor could name is a visitor who
	// chooses whose rows they read. Everywhere there is a session, the tenant
	// comes from the Grant instead, and this value is not consulted at all.
	Tenant string

	// Prefix is the path the routes are mounted under. Empty means
	// DefaultPrefix.
	Prefix string

	// PageSize is how many records one page answers with. Zero means
	// DefaultPageSize, and anything above MaxPageSize is refused rather than
	// clamped: a number somebody wrote and did not get is worse than a number
	// somebody wrote and was told about.
	PageSize int

	// Rates quotes the rate between two currencies, for the transfers that
	// cross wallets which are not counted the same way.
	//
	// Nil is the ordinary case and not a degraded one: an application whose
	// wallets all hold one currency at one scale never needs a rate, and a
	// transfer that would need one is refused with ErrCurrencyMismatch rather
	// than approximated. A rate comes from outside the process, which is why
	// this is the application's to supply and not this package's to fetch.
	//
	// What the provider supplies is the rate and not the converted amount.
	// Applying it, rounding it and recording it belong to this package, so that
	// every exchange in the application is rounded the same way and leaves the
	// same row behind whatever the provider is.
	Rates RateProvider

	// Fees answers with what a wallet charges to be paid, for the payments
	// between two wallets where somebody charges anything.
	//
	// Nil is the ordinary case and not a degraded one: most applications charge
	// nothing, and one that does knows its own pricing. What the provider
	// supplies is the schedule and not the fee, for the reason Rates supplies a
	// rate and not the converted amount -- the arithmetic, the rounding and the
	// record belong here, so every fee in the application is computed the same
	// way and leaves the same row behind.
	Fees FeeProvider

	// Discounts answers with what one payer is charged less on one payment.
	//
	// Nil is no discount anywhere. What it decides is the application's: who
	// gets one and why is a question about customers, which this package has
	// no way to answer and no business answering.
	Discounts DiscountProvider

	// CSRF issues the token every form on these screens carries.
	//
	// It is required, because every screen here writes: a page rendered without
	// a token is a page whose buttons the application refuses, and finding that
	// out from a form that does nothing is worse than finding it out at boot.
	CSRF *security.CSRF

	// Translator is the application's own catalogue, asked before the one this
	// package ships.
	//
	// It is optional. Nothing is asked of it when it is nil, and the screens are
	// drawn in the locales this package carries -- which is what an application
	// that renders in one language would have got anyway.
	Translator *translation.Translator

	// Listeners are told what the money did, after it did it.
	//
	// Empty is the ordinary case. Each one is called once the write has
	// committed, in the goroutine that made it, so what a listener is told is
	// what happened -- a movement that was rolled back is never announced, and
	// there is no message that would take an announcement back.
	//
	// There is no dispatcher here and no queue. What an application does with an
	// event is the application's, and one that wants the work off the request
	// hands it to whatever it already uses; a queue in this package would be a
	// second one beside the application's, with its own failures to learn.
	Listeners []Listener
}

// Validate reports what the configuration cannot be used with.
//
// It is called by New, so an application with a setting that cannot work fails
// where it is wired rather than on the first request that needed it.
func (c Config) Validate() error {
	if c.Tenant == "" {
		return fmt.Errorf("wallet: Config.Tenant is required: a visitor with no session has to be read as some customer, and it cannot be one the request names")
	}
	// The same rule the framework applies to every tenant it accepts. A tenant
	// is concatenated into a storage path, a cache key and a lock name, so one
	// carrying a separator lands in another tenant's namespace.
	if !security.ValidTenant(c.Tenant) {
		return fmt.Errorf("wallet: Config.Tenant is %q, which cannot be a tenant: lowercase letters, digits, - and _, up to 64 characters", c.Tenant)
	}
	if c.Prefix != "" && c.Prefix[0] != '/' {
		return fmt.Errorf("wallet: Config.Prefix is %q and has to start with /", c.Prefix)
	}
	if c.Prefix != "" {
		if err := validateRoutePrefix(c.Prefix); err != nil {
			return err
		}
	}
	if c.PageSize < 0 || c.PageSize > MaxPageSize {
		return fmt.Errorf("wallet: Config.PageSize is %d, and has to be between 0 and %d, where 0 means %d", c.PageSize, MaxPageSize, DefaultPageSize)
	}
	if c.CSRF == nil {
		return fmt.Errorf("wallet: Config.CSRF is required: every screen this module draws moves money, and a form with no token is a form the application refuses")
	}
	return nil
}

// validateRoutePrefix asks the standard library to parse the exact patterns
// the module will register. Its parser is not exported and reports invalid
// patterns by panic, so the throwaway mux turns that boot-time panic into the
// configuration error New promises.
func validateRoutePrefix(prefix string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("wallet: Config.Prefix %q cannot be registered as a route path", prefix)
		}
	}()

	handler := http.NotFoundHandler()
	mux := http.NewServeMux()
	for _, pattern := range routePatterns {
		mux.Handle(pattern.method+" "+prefix+pattern.suffix, handler)
	}
	return nil
}

// routePattern is one address this module registers, without the prefix and
// without the handler.
type routePattern struct {
	method string
	suffix string
	name   string
}

// routePatterns is every address this module answers, and it is the only list
// of them.
//
// It lives here because this is the file that has to prove a configured prefix
// can carry all of them, and Module.Routes reads it too, attaching a handler by
// name. Two lists would be two things to keep in step, and the one that fell
// behind would be the one nobody checked -- a prefix accepted at boot for a set
// of routes that is not the set registered.
var routePatterns = []routePattern{
	{http.MethodGet, "", "wallet.index"},
	{http.MethodPost, "", "wallet.store"},
	{http.MethodGet, "/{id}", "wallet.show"},
	{http.MethodGet, "/holders/{holder}/{slug}", "wallet.named"},
	{http.MethodGet, "/{id}/entries", "wallet.entries"},
	{http.MethodPut, "/{id}/credit", "wallet.credit"},
	{http.MethodPut, "/{id}/description", "wallet.describe"},
	{http.MethodPut, "/{id}/closure", "wallet.close"},
	{http.MethodDelete, "/{id}/closure", "wallet.reopen"},
	{http.MethodPost, "/{id}/deposits", "wallet.deposit"},
	{http.MethodPost, "/{id}/withdrawals", "wallet.withdraw"},
	{http.MethodPost, "/{id}/transfers", "wallet.transfer"},
	{http.MethodGet, "/{id}/purchases", "wallet.purchases"},
	{http.MethodPost, "/operations/{operation}/reversals", "wallet.reverse"},
	{http.MethodPost, "/operations/{operation}/confirmations", "wallet.confirm"},
	{http.MethodPost, "/purchases/refunds", "wallet.refund"},
}

// withDefaults returns the configuration with the optional fields filled in.
//
// It runs after Validate and never before: filling a default in first would
// hide the value somebody actually wrote from the check that would have refused
// it.
func (c Config) withDefaults() Config {
	if c.Prefix == "" {
		c.Prefix = DefaultPrefix
	}
	if c.PageSize == 0 {
		c.PageSize = DefaultPageSize
	}
	return c
}
