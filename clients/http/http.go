// Package http implements the `client "http"` block: a reusable HTTP
// client configuration that the http_* verb functions in package
// functions use to make outbound HTTP requests from action expressions.
package http

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("http", process)
}

// ─── HCL schema ──────────────────────────────────────────────────────────────

// httpClientDefinition is the decoded form of a `client "http"` block body.
type httpClientDefinition struct {
	BaseURL   string          `hcl:"base_url,optional"`
	UserAgent string          `hcl:"user_agent,optional"`
	Headers   hcl.Expression  `hcl:"headers,optional"`
	TLS       *cfg.TLSConfig  `hcl:"tls,block"`
	Redirects *redirectsBlock `hcl:"redirects,block"`
	Timeout   hcl.Expression  `hcl:"timeout,optional"`

	HTTP2                 *bool `hcl:"http2,optional"`
	MaxConnectionsPerHost *int  `hcl:"max_connections_per_host,optional"`
	MaxIdleConnections    *int  `hcl:"max_idle_connections,optional"`
	DisableKeepAlives     *bool `hcl:"disable_keep_alives,optional"`

	Auth             hcl.Expression    `hcl:"auth,optional"`
	AuthMaxLifetime  hcl.Expression    `hcl:"auth_max_lifetime,optional"`
	AuthMaxFailures  *int              `hcl:"auth_max_failures,optional"`
	AuthRetryBackoff *authBackoffBlock `hcl:"auth_retry_backoff,block"`
	Retry            *retryBlock       `hcl:"retry,block"`
	Cookies          *cookiesBlock     `hcl:"cookies,block"`

	// OTel propagation toggle. Spans and metrics are wired in
	// automatically when a tracer/meter provider is configured at the
	// global or block level — see Tracing and Metrics below.
	OTel *otelBlock `hcl:"otel,block"`

	// Tracing and Metrics each take an expression that resolves to a
	// specific tracer/meter provider, mirroring the convention used by
	// other vinculum clients. When omitted, the global default
	// provider is used (or a no-op if none is configured).
	Tracing hcl.Expression `hcl:"tracing,optional"`
	Metrics hcl.Expression `hcl:"metrics,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

// otelBlock is the `otel { ... }` sub-block.
type otelBlock struct {
	// Propagate controls whether W3C trace propagation headers
	// (traceparent, tracestate, baggage) are injected into outbound
	// requests. Defaults to true. A per-call override sits in
	// opts.otel.propagate.
	Propagate *bool     `hcl:"propagate,optional"`
	DefRange  hcl.Range `hcl:",def_range"`
}

// redirectsBlock is the `redirects { ... }` sub-block.
type redirectsBlock struct {
	Follow             *bool     `hcl:"follow,optional"`
	Max                *int      `hcl:"max,optional"`
	KeepAuthOnRedirect *bool     `hcl:"keep_auth_on_redirect,optional"`
	DefRange           hcl.Range `hcl:",def_range"`
}

// ─── Runtime types ───────────────────────────────────────────────────────────

// HTTPClient is the runtime representation of a `client "http"` block. It
// implements config.Client, config.CtyValuer, and HTTPCallable (the interface
// the verb functions consume).
type HTTPClient struct {
	cfg.BaseClient

	baseURL        *url.URL
	defaultHeaders http.Header
	requestTimeout time.Duration
	maxRedirects   int
	followRedirect bool
	keepAuthOnRdir bool
	retryPolicy    RetryPolicy

	// authHandler holds the auth cache and configuration. nil if the
	// client did not define an `auth` expression.
	authHandler AuthHandler

	httpClient *http.Client

	// parentEvalCtx is the HCL evaluation context the client was registered
	// against. The verb functions use it as the parent when evaluating
	// lazy expressions like retry.on_response and the auth hook. nil for
	// the shared null client (which has no expressions to evaluate).
	parentEvalCtx *hcl.EvalContext
}

// Compile-time checks.
var (
	_ cfg.Client    = (*HTTPClient)(nil)
	_ cfg.CtyValuer = (*HTTPClient)(nil)
)

// CtyValue exposes the client as a generic client capsule. There is no nested
// object structure for HTTP clients, unlike bus-backed clients.
func (c *HTTPClient) CtyValue() cty.Value {
	return cfg.NewClientCapsule(c)
}

// BaseURL returns the client's configured base URL, or nil if none.
func (c *HTTPClient) BaseURL() *url.URL {
	return c.baseURL
}

// DefaultHeaders returns the default headers configured on the client. The
// returned http.Header should be treated as read-only by callers — they must
// clone it before mutating.
func (c *HTTPClient) DefaultHeaders() http.Header {
	return c.defaultHeaders
}

// DefaultRequestTimeout returns the client's whole-request timeout, or 0 if
// none was configured.
func (c *HTTPClient) DefaultRequestTimeout() time.Duration {
	return c.requestTimeout
}

// DefaultRedirectPolicy returns the redirect policy this client was
// configured with. Verb functions use it as the base for per-call overrides.
func (c *HTTPClient) DefaultRedirectPolicy() RedirectPolicy {
	return RedirectPolicy{
		Follow:   c.followRedirect,
		Max:      c.maxRedirects,
		KeepAuth: c.keepAuthOnRdir,
	}
}

// DefaultRetryPolicy returns the retry policy this client was configured
// with. Clients without a `retry { ... }` block return a no-retry policy
// (MaxAttempts == 1).
func (c *HTTPClient) DefaultRetryPolicy() RetryPolicy {
	return c.retryPolicy
}

// AuthHandler returns the auth cache for this client, or nil if no
// `auth` expression was configured. Verb functions use it to fetch the
// Authorization header for outbound requests and to record per-request
// status for failure tracking.
func (c *HTTPClient) AuthHandler() AuthHandler {
	return c.authHandler
}

// ParentEvalCtx returns the HCL evaluation context the client was
// registered against, or nil for synthetic clients (e.g. NullClient). Used
// by verb functions to evaluate lazy expressions like retry.on_response.
func (c *HTTPClient) ParentEvalCtx() *hcl.EvalContext {
	return c.parentEvalCtx
}

// Do sends a request through this client's transport stack using the client's
// configured redirect policy. The returned response's Body must be closed by
// the caller.
func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.httpClient.Do(req)
}

// DoWithRedirectPolicy sends a request using a per-call redirect policy
// in place of the client's default. It constructs a fresh *http.Client
// that reuses the underlying Transport (and thus the connection pool)
// and the shared cookie jar, but installs a CheckRedirect closure built
// from pol. This is necessary because *http.Client exposes a single
// CheckRedirect hook that closes over its values at construction time.
func (c *HTTPClient) DoWithRedirectPolicy(req *http.Request, pol RedirectPolicy) (*http.Response, error) {
	perCall := &http.Client{
		Transport:     c.httpClient.Transport,
		Timeout:       c.httpClient.Timeout,
		CheckRedirect: buildCheckRedirect(pol),
		Jar:           c.httpClient.Jar,
	}
	return perCall.Do(req)
}

// ─── Config processing ──────────────────────────────────────────────────────

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := httpClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

	clientName := block.Labels[1]

	// ── base_url ──
	var baseURL *url.URL
	if def.BaseURL != "" {
		u, err := url.Parse(def.BaseURL)
		if err != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "http client: invalid base_url",
				Detail:   fmt.Sprintf("%q: %v", def.BaseURL, err),
				Subject:  &def.DefRange,
			}}
		}
		baseURL = u
	}

	// ── default headers ──
	headers := make(http.Header)
	if cfg.IsExpressionProvided(def.Headers) {
		val, hdrDiags := def.Headers.Value(config.EvalCtx())
		if hdrDiags.HasErrors() {
			return nil, hdrDiags
		}
		if err := applyHeadersToHeader(headers, val); err != nil {
			r := def.Headers.Range()
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "http client: invalid headers",
				Detail:   err.Error(),
				Subject:  &r,
			}}
		}
	}
	// user_agent shorthand — loses to an explicit User-Agent in headers.
	if def.UserAgent != "" && headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", def.UserAgent)
	}

	// ── TLS ──
	var tlsCfg *tls.Config
	if def.TLS != nil && def.TLS.Enabled {
		c, err := def.TLS.BuildTLSClientConfig(config.BaseDir)
		if err != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "http client: invalid TLS config",
				Detail:   err.Error(),
				Subject:  &def.TLS.DefRange,
			}}
		}
		tlsCfg = c
	}

	// ── Redirects ──
	followRedirect := true
	maxRedirects := 10
	keepAuth := false
	if def.Redirects != nil {
		if def.Redirects.Follow != nil {
			followRedirect = *def.Redirects.Follow
		}
		if def.Redirects.Max != nil {
			if *def.Redirects.Max < 0 {
				return nil, hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "http client: redirects.max must be >= 0",
					Subject:  &def.Redirects.DefRange,
				}}
			}
			maxRedirects = *def.Redirects.Max
		}
		if def.Redirects.KeepAuthOnRedirect != nil {
			keepAuth = *def.Redirects.KeepAuthOnRedirect
		}
	}

	// ── Timeout ──
	// Only the duration shorthand is supported (`timeout = "30s"`); the
	// per-stage long-form block is left for a future extension.
	var requestTimeout time.Duration
	if cfg.IsExpressionProvided(def.Timeout) {
		d, dDiags := config.ParseDuration(def.Timeout)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		requestTimeout = d
	}

	// ── Transport ──
	transport := &http.Transport{
		TLSClientConfig:   tlsCfg,
		ForceAttemptHTTP2: true,
		DisableKeepAlives: false,
	}
	if def.HTTP2 != nil {
		transport.ForceAttemptHTTP2 = *def.HTTP2
	}
	if def.DisableKeepAlives != nil {
		transport.DisableKeepAlives = *def.DisableKeepAlives
	}
	if def.MaxConnectionsPerHost != nil {
		transport.MaxConnsPerHost = *def.MaxConnectionsPerHost
	}
	if def.MaxIdleConnections != nil {
		transport.MaxIdleConns = *def.MaxIdleConnections
	}

	// ── Retry policy ──
	retryPolicy, retryDiags := parseRetryBlock(config, def.Retry)
	if retryDiags.HasErrors() {
		return nil, retryDiags
	}

	// ── Auth ──
	var authHandler AuthHandler
	if cfg.IsExpressionProvided(def.Auth) {
		var maxLifetime time.Duration
		if cfg.IsExpressionProvided(def.AuthMaxLifetime) {
			d, mlDiags := config.ParseDuration(def.AuthMaxLifetime)
			if mlDiags.HasErrors() {
				return nil, mlDiags
			}
			maxLifetime = d
		}
		maxFailures := 5
		if def.AuthMaxFailures != nil {
			if *def.AuthMaxFailures < 0 {
				return nil, hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "auth_max_failures must be >= 0",
					Subject:  &def.DefRange,
				}}
			}
			maxFailures = *def.AuthMaxFailures
		}
		backoff, backoffDiags := parseAuthBackoffBlock(config, def.AuthRetryBackoff)
		if backoffDiags.HasErrors() {
			return nil, backoffDiags
		}
		authHandler = newAuthCache(clientName, def.Auth, maxLifetime, maxFailures, backoff)
	}

	// ── Cookie jar ──
	jar, jarErr := buildCookieJar(def.Cookies)
	if jarErr != nil {
		subj := def.DefRange
		if def.Cookies != nil {
			subj = def.Cookies.DefRange
		}
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "http client: invalid cookies block",
			Detail:   jarErr.Error(),
			Subject:  &subj,
		}}
	}

	// ── OpenTelemetry providers and propagation toggle ──
	tracerProvider, traceDiags := config.ResolveTracerProvider(def.Tracing)
	if traceDiags.HasErrors() {
		return nil, traceDiags
	}
	meterProvider, metricsDiags := cfg.ResolveMeterProvider(config, def.Metrics)
	if metricsDiags.HasErrors() {
		return nil, metricsDiags
	}
	otelPropagate := true
	if def.OTel != nil && def.OTel.Propagate != nil {
		otelPropagate = *def.OTel.Propagate
	}

	// Wrap the base transport with otelhttp for spans, propagation,
	// and the standard http.client.* metrics. This must sit just above
	// the *http.Transport so that retries and per-call redirect
	// overrides (which build their own *http.Client wrappers around the
	// shared RoundTripper) all benefit from instrumentation.
	instrumentedTransport := wrapTransportWithOTel(
		transport,
		tracerProvider,
		meterProvider,
		otelPropagate,
	)

	// ── *http.Client ──
	defaultPolicy := RedirectPolicy{
		Follow:   followRedirect,
		Max:      maxRedirects,
		KeepAuth: keepAuth,
	}
	goClient := &http.Client{
		Transport:     instrumentedTransport,
		Timeout:       requestTimeout,
		CheckRedirect: buildCheckRedirect(defaultPolicy),
	}
	// Avoid storing a typed-nil into the http.CookieJar interface field:
	// http.Client checks Jar != nil before calling methods, but a typed
	// nil *cookiejar.Jar passes that check and then panics on Cookies().
	if jar != nil {
		goClient.Jar = jar
	}

	client := &HTTPClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		baseURL:        baseURL,
		defaultHeaders: headers,
		requestTimeout: requestTimeout,
		maxRedirects:   maxRedirects,
		followRedirect: followRedirect,
		keepAuthOnRdir: keepAuth,
		retryPolicy:    retryPolicy,
		authHandler:    authHandler,
		httpClient:     goClient,
		parentEvalCtx:  config.EvalCtx(),
	}

	return client, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// NullClient returns a zero-configured HTTPClient equivalent to passing
// `null` as the client argument to an http_* verb function:
//
//   - no default headers
//   - no auth, no cookies, no retries
//   - follow redirects (up to 10)
//   - TLS with system roots, full verification
//   - 30-second whole-request timeout
//   - OTel propagation on
//
// The returned client is safe for concurrent use and may be shared.
func NullClient() *HTTPClient {
	return nullClient
}

// nullClient is the shared default used when the caller passes null as the
// client argument. Constructed lazily at first use would also work, but the
// cost of eager construction is a single *http.Client and is negligible.
var nullClient = func() *HTTPClient {
	transport := &http.Transport{
		ForceAttemptHTTP2: true,
	}
	defaultPolicy := RedirectPolicy{Follow: true, Max: 10, KeepAuth: false}
	// Wrap with otelhttp using the global tracer/meter providers (or
	// the no-op providers if none are installed). The null client gets
	// default-on propagation just like a configured client would.
	instrumented := wrapTransportWithOTel(transport, nil, nil, true)
	return &HTTPClient{
		BaseClient:     cfg.BaseClient{Name: ""},
		defaultHeaders: make(http.Header),
		requestTimeout: 30 * time.Second,
		maxRedirects:   10,
		followRedirect: true,
		keepAuthOnRdir: false,
		retryPolicy:    noRetryPolicy(),
		httpClient: &http.Client{
			Transport:     instrumented,
			Timeout:       30 * time.Second,
			CheckRedirect: buildCheckRedirect(defaultPolicy),
		},
	}
}()

// RedirectPolicy controls redirect-following behavior. It is normally taken
// from the HTTPClient's configuration; verb functions may construct a
// modified policy and pass it to DoWithRedirectPolicy for a single call.
type RedirectPolicy struct {
	Follow   bool
	Max      int
	KeepAuth bool
}

// buildCheckRedirect returns a CheckRedirect function that enforces a fixed
// RedirectPolicy: maximum hop count, optional non-following, and cross-origin
// Authorization stripping.
func buildCheckRedirect(pol RedirectPolicy) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if !pol.Follow {
			return http.ErrUseLastResponse
		}
		if len(via) >= pol.Max {
			return fmt.Errorf("stopped after %d redirects", pol.Max)
		}
		if !pol.KeepAuth && len(via) > 0 {
			// Strip Authorization on cross-origin redirect. "Cross-origin" =
			// different scheme, host, or port.
			orig := via[0].URL
			if !sameOrigin(orig, req.URL) {
				req.Header.Del("Authorization")
			}
		}
		return nil
	}
}

// sameOrigin returns true if two URLs share scheme, hostname, and port.
func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Scheme == b.Scheme && a.Host == b.Host
}

// applyHeadersToHeader copies header values from a cty map or object into h.
// Each value may be a string (single value) or a list/tuple of strings
// (multi-value). A null map or null value is a no-op.
func applyHeadersToHeader(h http.Header, val cty.Value) error {
	if val.IsNull() {
		return nil
	}
	if !val.Type().IsMapType() && !val.Type().IsObjectType() {
		return fmt.Errorf("headers must be a map or object, got %s", val.Type().FriendlyName())
	}
	for name, elem := range val.AsValueMap() {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if elem.IsNull() {
			h.Del(canonical)
			continue
		}
		switch {
		case elem.Type() == cty.String:
			h.Add(canonical, elem.AsString())
		case elem.Type().IsListType() || elem.Type().IsTupleType():
			for it := elem.ElementIterator(); it.Next(); {
				_, v := it.Element()
				if v.IsNull() || v.Type() != cty.String {
					return fmt.Errorf("header %q list values must be strings", name)
				}
				h.Add(canonical, v.AsString())
			}
		default:
			return fmt.Errorf("header %q value must be a string or list of strings, got %s", name, elem.Type().FriendlyName())
		}
	}
	return nil
}

// GetHTTPClientFromCapsule extracts an *HTTPClient (or anything implementing
// the HTTPCallable interface, since this is structural) from a cty value. It
// accepts:
//
//   - a plain client capsule wrapping an *HTTPClient (via CtyValue())
//   - any other capsule whose encapsulated value satisfies HTTPCallable
//   - an object with a _capsule attribute containing either of the above
//
// Returns a nil HTTPCallable if the value is null; callers should treat that
// as the default-client path.
func GetHTTPCallableFromValue(val cty.Value) (HTTPCallable, error) {
	if val.IsNull() {
		return nil, nil
	}
	// The cty capsule pattern: extract the encapsulated interface{} and
	// then type-assert to HTTPCallable. We never type-assert to
	// *HTTPClient here, which preserves the seam for mock client
	// implementations.
	enc, err := getEncapsulated(val)
	if err != nil {
		return nil, err
	}
	c, ok := enc.(HTTPCallable)
	if !ok {
		return nil, fmt.Errorf("value is not an http client, got %T", enc)
	}
	return c, nil
}

// getEncapsulated walks through object/_capsule layers to get the raw value.
func getEncapsulated(val cty.Value) (interface{}, error) {
	if val.Type().IsObjectType() {
		if !val.Type().HasAttribute("_capsule") {
			return nil, fmt.Errorf("object has no _capsule attribute")
		}
		val = val.GetAttr("_capsule")
	}
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("expected capsule, got %s", val.Type().FriendlyName())
	}
	return val.EncapsulatedValue(), nil
}

// HTTPCallable is the interface the verb functions require of any value
// passed as the client argument. The real HTTPClient implements it; mock
// client types may also implement it to participate in the same call
// path. The verb functions never type-assert to a concrete type — they
// go through this interface exclusively, which keeps mock clients
// transparent to the verb function code path.
type HTTPCallable interface {
	GetName() string
	BaseURL() *url.URL
	DefaultHeaders() http.Header
	DefaultRequestTimeout() time.Duration
	DefaultRedirectPolicy() RedirectPolicy
	DefaultRetryPolicy() RetryPolicy

	// AuthHandler returns the auth cache and configuration for this
	// client, or nil if no `auth` expression was configured. Mock
	// implementations may return nil to opt out of auth entirely.
	AuthHandler() AuthHandler

	// ParentEvalCtx returns the HCL evaluation context the client was
	// registered against, or nil for synthetic clients (e.g. mocks, the
	// shared null client). Verb functions need it to evaluate lazy
	// expressions like retry.on_response that were stored at config time.
	ParentEvalCtx() *hcl.EvalContext

	// Do sends a request using the implementation's default redirect policy.
	Do(req *http.Request) (*http.Response, error)

	// DoWithRedirectPolicy sends a request using the supplied per-call
	// redirect policy in place of the default. Mock implementations that do
	// not actually follow redirects may delegate to Do(req).
	DoWithRedirectPolicy(req *http.Request, pol RedirectPolicy) (*http.Response, error)
}

var _ HTTPCallable = (*HTTPClient)(nil)
