// Package frankfurter quotes exchange rates for a wallet, from the Frankfurter
// service.
//
// It is an implementation of wallet.RateProvider and nothing else. What a rate
// does to an amount -- the multiplication, the scale, the one rounding rule, the
// row that records it -- stays in the wallet package, which is what makes two
// conversions of the same amount at the same rate the same number whatever
// quoted it. This only answers the question "what is one of these worth in
// those", exactly, as the fraction it was published as.
//
// # Where the quote happens, and why that matters
//
// The wallet asks for a rate before it opens the transaction that moves the
// money: WalletService.Transfer calls its converter and only afterwards commits.
// So a provider that takes a second to answer holds no row lock and widens no
// window -- it delays one caller and nothing else.
//
// That is a property of the caller and not of this package, which is why it is
// written down here as well: moving the quote inside the transaction would make
// every slow answer a lock held across a network call, on the rows two other
// payments are queueing for. Nothing here should be changed to make that
// possible, and Timeout below is the second line of defence rather than the
// first.
//
// # What it talks to
//
// api.frankfurter.dev, which republishes the European Central Bank's daily
// reference rates. It needs no key, which is why it is the one wired here: a
// provider whose simple case requires an account is a provider an application
// cannot try. The rates are the ECB's and carry the ECB's terms; the service is
// somebody else's and can stop, which is what ErrRateProviderUnavailable is
// for.
//
// It publishes one set of figures per working day, so a quote is a day's
// quote. Anything that needs the rate of a moment rather than of a day needs a
// different provider, and this one says so rather than interpolating.
package frankfurter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/arandu-io/framework/security"
	"github.com/arandu-io/hesape/cache"

	wallet "github.com/hyz-is/arandu-wallet"
)

// DefaultEndpoint is the service this package quotes from.
const DefaultEndpoint = "https://api.frankfurter.dev/v1"

// The bounds on how long one quote may take.
//
// A deadline is not optional here and there is no way to ask for none. The
// wallet quotes before it opens its transaction, so a slow answer costs one
// caller rather than a row lock -- but "costs one caller" is only true while
// something ends the wait, and a request with no deadline ends when the far end
// decides to answer.
const (
	// DefaultTimeout is how long a quote may take when Config says nothing.
	DefaultTimeout = 5 * time.Second
	// MaxTimeout is the longest deadline this package will accept. A rate that
	// takes longer than this to fetch is a rate the caller should be told about
	// rather than waited for.
	MaxTimeout = 30 * time.Second
)

// DefaultTTL is how long a quote is kept when Config says nothing.
//
// An hour. The figures behind it change once a working day, so an hour is short
// enough that a new day's rate is picked up within one and long enough that a
// busy application asks a few dozen times a day rather than once a payment.
const DefaultTTL = time.Hour

// MaxFractionDigits is how many digits after the point a published rate may
// carry.
//
// Nine, because the rate is held as a fraction over ten to this power and the
// wallet bounds that denominator at a billion. The figures this quotes from
// carry five, so the limit is not one anybody meets; it is here so that a
// service that widened its output is refused rather than silently rounded.
const MaxFractionDigits = 9

// Config is what an application passes when it wires this provider.
//
// A typed struct and not a map, for the reason the wallet's own Config is one:
// a misspelled key in a map is a setting that silently keeps its default.
type Config struct {
	// Endpoint is the service to ask. Empty means DefaultEndpoint.
	//
	// It is here so that an application can point this at its own mirror of the
	// same service, which is what somebody who cannot reach the public one from
	// their network does. It is not a way to point it at a different service:
	// what is parsed is this service's answer.
	Endpoint string

	// Client is the HTTP client to ask with. Nil means one of this package's
	// own, with Timeout on it.
	//
	// It is a parameter because an application that already has a client with
	// its instrumentation, its proxy and its certificates on it should be using
	// that one. Whatever it is, every request still carries the deadline below:
	// a client whose own timeout is longer does not lengthen this.
	Client *http.Client

	// Timeout is how long one quote may take. Zero means DefaultTimeout, and
	// anything above MaxTimeout is refused rather than clamped -- a number
	// somebody wrote and did not get is worse than a number somebody wrote and
	// was told about.
	Timeout time.Duration

	// Cache is where a quote is kept between calls, and nil is no cache.
	//
	// It is the application's own cache rather than a map in here, and that is
	// deliberate: a second cache is a second thing to size, to expire and to
	// clear, and an application that has already chosen one has chosen it. It
	// is keyed per tenant, because the Repository keys everything per tenant --
	// which costs a fetch per customer for a public figure, and buys a
	// negotiated rate table not leaking out of the customer it belongs to.
	Cache *cache.Repository

	// TTL is how long a quote is kept. Zero means DefaultTTL. It is ignored
	// when Cache is nil.
	TTL time.Duration
}

// Provider quotes rates from Frankfurter.
//
// Build it with New. A zero Provider has no client and no endpoint and answers
// nothing, which is what a struct literal deserves here: what this reaches is
// outside the process, and the one place that is decided should be the one place
// that builds it.
type Provider struct {
	endpoint string
	client   *http.Client
	timeout  time.Duration
	cache    *cache.Repository
	ttl      time.Duration
}

// Compile-time proof that this answers the seam the wallet declares. A provider
// that drifted off it would fail where somebody wired it rather than here.
var _ wallet.RateProvider = (*Provider)(nil)

// New wires the provider, or reports why it cannot be built.
//
// It returns an error rather than panicking, for the reason wallet.New does:
// everything it refuses is a wiring mistake, and a wiring mistake found where
// the application is assembled costs one restart.
func New(cfg Config) (*Provider, error) {
	if cfg.Timeout < 0 || cfg.Timeout > MaxTimeout {
		return nil, fmt.Errorf("frankfurter: Config.Timeout is %s, and it has to be between zero and %s",
			cfg.Timeout, MaxTimeout)
	}
	if cfg.TTL < 0 {
		return nil, fmt.Errorf("frankfurter: Config.TTL is %s, and a quote cannot be kept for a negative time", cfg.TTL)
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if _, err := url.Parse(endpoint); err != nil {
		return nil, fmt.Errorf("frankfurter: Config.Endpoint is not an address: %w", err)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ttl := cfg.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	return &Provider{
		endpoint: strings.TrimRight(endpoint, "/"),
		client:   client,
		timeout:  timeout,
		cache:    cfg.Cache,
		ttl:      ttl,
	}, nil
}

// Rate returns the rate that converts from into to.
//
// The pair is answered as the exact fraction the service published: the decimal
// it wrote is read digit by digit into a numerator over a power of ten, so
// 5.4321 is 54321/10000 and not a float that is nearly that. Nothing here
// multiplies anything -- what a rate does to an amount belongs to the wallet,
// under its one rounding rule.
//
// A pair of the same currency is one to one, answered without asking anybody.
// It is not a special case so much as the absence of one: there is no rate to
// look up, and a service that answered a number other than one for it would be
// answering about something else.
//
// Every failure it reports wraps one of the wallet's five, so a caller tells a
// pair nobody quotes from a service that is down without reading a sentence.
func (p *Provider) Rate(ctx context.Context, g security.Grant, from, to wallet.Currency) (wallet.Rate, error) {
	if from == "" || to == "" {
		return wallet.Rate{}, fmt.Errorf("%w: it was asked for %q into %q",
			wallet.ErrRateRequestRefused, from, to)
	}
	if from == to {
		return wallet.Rate{
			From: from, To: to, Numerator: 1, Denominator: 1, QuotedAt: time.Now().UTC(),
		}, nil
	}

	if quoted, found, err := p.cached(ctx, g, from, to); err != nil {
		return wallet.Rate{}, err
	} else if found {
		return quoted, nil
	}

	quoted, err := p.fetch(ctx, from, to)
	if err != nil {
		return wallet.Rate{}, err
	}
	if err := p.keep(ctx, g, quoted); err != nil {
		return wallet.Rate{}, err
	}
	return quoted, nil
}

// quote is a rate as it is kept between calls.
//
// The fraction and not the decimal, so that what comes back out of a cache is
// the same value that went in: a decimal re-parsed is a second chance to read
// it differently, and a rate that changed on the way through a cache is a
// conversion nobody can reproduce.
type quote struct {
	Numerator   int64     `json:"numerator"`
	Denominator int64     `json:"denominator"`
	QuotedAt    time.Time `json:"quoted_at"`
}

// cacheKey names one pair in the cache.
func cacheKey(from, to wallet.Currency) string {
	return "wallet.rate." + string(from) + "." + string(to)
}

// cached reads a kept quote, and reports that there was none.
//
// A cache that cannot be read is reported rather than stepped around. It is the
// provider's own storage failing, which is a different thing from the service
// being down and has its own value; an application that would rather fetch
// anyway wires no cache, which is the setting that says so.
func (p *Provider) cached(ctx context.Context, g security.Grant, from, to wallet.Currency) (wallet.Rate, bool, error) {
	if p.cache == nil {
		return wallet.Rate{}, false, nil
	}
	kept, err := cache.Get[*quote](ctx, p.cache, g, cacheKey(from, to))
	switch {
	case errors.Is(err, cache.ErrNotFound):
		// Nothing kept, which is the ordinary answer and not a failure. It is
		// told apart from a cache that could not be read, because the second is
		// worth reporting and the first is what happens on every first call.
		return wallet.Rate{}, false, nil
	case err != nil:
		return wallet.Rate{}, false, fmt.Errorf("%w: reading the kept quote for %s/%s: %w",
			wallet.ErrRateCacheFailed, from, to, err)
	case kept == nil:
		return wallet.Rate{}, false, nil
	}
	return wallet.Rate{
		From: from, To: to,
		Numerator:   kept.Numerator,
		Denominator: kept.Denominator,
		QuotedAt:    kept.QuotedAt,
	}, true, nil
}

// keep stores a quote for the next caller.
func (p *Provider) keep(ctx context.Context, g security.Grant, rate wallet.Rate) error {
	if p.cache == nil {
		return nil
	}
	err := p.cache.Put(ctx, g, cacheKey(rate.From, rate.To), &quote{
		Numerator:   rate.Numerator,
		Denominator: rate.Denominator,
		QuotedAt:    rate.QuotedAt,
	}, p.ttl)
	if err != nil {
		return fmt.Errorf("%w: keeping the quote for %s/%s: %w",
			wallet.ErrRateCacheFailed, rate.From, rate.To, err)
	}
	return nil
}

// answer is what the service replies with.
//
// The rates are json.Number and not float64, and that is the whole reason this
// type is written out rather than decoded into a map of floats. A float64 of
// 5.4321 is not 5.4321, and every amount converted through it would inherit the
// difference -- in the one table where a difference is money.
type answer struct {
	Base  string                 `json:"base"`
	Date  string                 `json:"date"`
	Rates map[string]json.Number `json:"rates"`
}

// fetch asks the service for one pair.
func (p *Provider) fetch(ctx context.Context, from, to wallet.Currency) (wallet.Rate, error) {
	// The deadline is this package's and is applied to whatever client the
	// application handed over, so a client with a longer timeout of its own does
	// not lengthen a quote.
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	address := fmt.Sprintf("%s/latest?base=%s&symbols=%s",
		p.endpoint, url.QueryEscape(string(from)), url.QueryEscape(string(to)))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return wallet.Rate{}, fmt.Errorf("%w: %s/%s: %w", wallet.ErrRateRequestRefused, from, to, err)
	}
	request.Header.Set("Accept", "application/json")

	response, err := p.client.Do(request)
	if err != nil {
		return wallet.Rate{}, fmt.Errorf("%w: %s/%s: %w", wallet.ErrRateProviderUnavailable, from, to, err)
	}
	defer func() { _ = response.Body.Close() }()

	if err := statusOf(response.StatusCode, from, to); err != nil {
		return wallet.Rate{}, err
	}

	// Bounded, because what is on the far end is somebody else's service and a
	// body nobody bounded is a body that decides how much memory this process
	// uses.
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return wallet.Rate{}, fmt.Errorf("%w: reading the answer for %s/%s: %w",
			wallet.ErrRateProviderUnavailable, from, to, err)
	}

	var payload answer
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return wallet.Rate{}, fmt.Errorf("%w: the answer for %s/%s is not the shape this service publishes: %w",
			wallet.ErrRateProviderUnavailable, from, to, err)
	}

	published, quoted := payload.Rates[string(to)]
	if !quoted {
		return wallet.Rate{}, fmt.Errorf("%w: %s into %s", wallet.ErrRatePairUnknown, from, to)
	}

	numerator, denominator, err := fraction(published.String())
	if err != nil {
		return wallet.Rate{}, fmt.Errorf("%w: %s into %s was published as %q: %w",
			wallet.ErrRateProviderUnavailable, from, to, published.String(), err)
	}

	at, err := time.Parse(time.DateOnly, payload.Date)
	if err != nil {
		return wallet.Rate{}, fmt.Errorf("%w: the answer for %s/%s is dated %q: %w",
			wallet.ErrRateProviderUnavailable, from, to, payload.Date, err)
	}

	rate := wallet.Rate{
		From: from, To: to,
		Numerator:   numerator,
		Denominator: denominator,
		QuotedAt:    at.UTC(),
	}
	// Checked here rather than left to the caller, so that a rate this package
	// cannot stand behind never becomes one the wallet has to refuse later --
	// by which time the failure reads as the wallet's.
	if err := rate.Validate(); err != nil {
		return wallet.Rate{}, fmt.Errorf("%w: %s into %s: %w", wallet.ErrRateProviderUnavailable, from, to, err)
	}
	return rate, nil
}

// statusOf turns what the service answered with into one of the wallet's five,
// and nil where it answered normally.
//
// The mapping is by what the caller can do about it, which is why 404 and 422
// are the pair being unknown rather than a failure: this service answers both
// when it does not publish a currency, and neither is worth retrying.
func statusOf(status int, from, to wallet.Currency) error {
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusNotFound, status == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s into %s", wallet.ErrRatePairUnknown, from, to)
	case status == http.StatusBadRequest:
		return fmt.Errorf("%w: %s into %s was refused as malformed", wallet.ErrRateRequestRefused, from, to)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s into %s was rate limited", wallet.ErrRateProviderUnavailable, from, to)
	}
	return fmt.Errorf("%w: %s into %s answered %d", wallet.ErrRateProviderUnavailable, from, to, status)
}

// fraction reads a published decimal into the exact fraction it spells.
//
// Digit by digit, and never through a float: "5.4321" is 54321 over 10000, and
// that is the number that was published. Reading it as a float64 first would
// make it 5.4320999999999998 and every amount converted through it would be
// wrong by the difference -- invisibly, and in the direction of whoever the
// rounding favours.
//
// The denominator is a power of ten and is bounded by MaxFractionDigits, which
// keeps it inside the bound the wallet puts on a rate's denominator. A published
// figure with more digits than that is refused rather than truncated: truncating
// a rate is choosing a different one.
func fraction(published string) (int64, int64, error) {
	text := strings.TrimSpace(published)
	if text == "" {
		return 0, 0, errors.New("it is empty")
	}
	if strings.ContainsAny(text, "eE") {
		return 0, 0, fmt.Errorf("%q is in exponent notation, which this reads no digits of", text)
	}
	if strings.HasPrefix(text, "-") {
		return 0, 0, fmt.Errorf("%q is negative, and a rate is a positive fraction", text)
	}
	text = strings.TrimPrefix(text, "+")

	whole, decimals, _ := strings.Cut(text, ".")
	if len(decimals) > MaxFractionDigits {
		return 0, 0, fmt.Errorf("%q carries %d digits after the point and the most is %d",
			text, len(decimals), MaxFractionDigits)
	}

	var numerator int64
	digits := whole + decimals
	if digits == "" {
		return 0, 0, fmt.Errorf("%q has no digits", text)
	}
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, 0, fmt.Errorf("%q is not a decimal number", text)
		}
		// The bound on the digits is what keeps this inside an int64, and the
		// check is here rather than left to it so that a longer figure fails
		// loudly instead of wrapping into a rate of the wrong sign.
		if numerator > (1<<62)/10 {
			return 0, 0, fmt.Errorf("%q does not fit in the range a rate is held in", text)
		}
		numerator = numerator*10 + int64(c-'0')
	}

	denominator := int64(1)
	for range decimals {
		denominator *= 10
	}
	if numerator == 0 {
		return 0, 0, fmt.Errorf("%q is zero, and a rate of zero converts every amount to nothing", text)
	}
	return numerator, denominator, nil
}
