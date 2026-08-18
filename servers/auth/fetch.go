package auth

import (
	"net/http"
	"time"
)

// authFetchTimeout bounds every outbound request this package makes to an
// identity provider — OIDC discovery, JWKS, and RFC 7662 introspection.
//
// Without it these all ride on http.DefaultClient, which has no timeout at all:
// a provider that accepts the connection and then never answers hangs the
// caller forever. At startup that meant `vinculum serve` hanging with no
// diagnostic; per request it means a handler goroutine pinned indefinitely.
//
// Ten seconds is the same order of magnitude the sql client uses for its
// connection check, rather than the shorter redis ping, because an identity
// provider is commonly a further network hop away and its discovery document
// may be served by a cold path.
//
// A var rather than a const only so tests can shorten it; nothing outside this
// package changes it.
var authFetchTimeout = 10 * time.Second

// authHTTPClient is shared by every outbound auth fetch so the timeout cannot
// be forgotten at a call site. It is stateless apart from connection pooling,
// which is exactly what should be shared. Read at each use, not captured, so
// shortening the timeout above takes effect.
var authHTTPClient = &http.Client{Timeout: authFetchTimeout}

// resolveBackoffInitial and resolveBackoffMax bound how often a failed
// resolution is retried. The schedule matches the `reconnect` block's defaults
// (config/util.go) — 1s doubling to 60s — because this is the same "dependency
// is not up yet" situation, and a config author reading one should not have to
// learn a second set of numbers.
const (
	resolveBackoffInitial = 1 * time.Second
	resolveBackoffMax     = 60 * time.Second
)

// nextBackoff returns the delay to wait after a failure that followed a wait of
// current (zero for the first failure).
func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return resolveBackoffInitial
	}
	if next := current * 2; next < resolveBackoffMax {
		return next
	}
	return resolveBackoffMax
}
