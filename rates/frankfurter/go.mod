// The rate provider, as a module of its own.
//
// It has its own go.mod because it is the only part of this repository that
// talks to a network, and in Go there is no optional dependency: a package that
// held an HTTP client would put one in the build of everybody who installed the
// wallet, whether or not their money ever crosses a currency. Split out, the
// parent goes on declaring network = false and means it, and this declares its
// own.
module github.com/hyz-is/arandu-wallet/rates/frankfurter

go 1.26

require (
	github.com/arandu-io/framework v0.46.0
	github.com/arandu-io/hesape v0.25.2
	github.com/hyz-is/arandu-wallet v0.4.0
)

require (
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

// Until the parent version this needs is on the proxy. It names the five
// failures a provider reports with, which is what this wraps, and a submodule
// cannot require a version that does not exist yet.
replace github.com/hyz-is/arandu-wallet => ../..
