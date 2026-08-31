package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// OIDCMetadata holds the fields from an OpenID Connect discovery document that
// vinculum exposes (for MCP's /.well-known/oauth-authorization-server endpoint).
type OIDCMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	JWKSUri                           string   `json:"jwks_uri"`
	ResponseTypesSupported            []string `json:"response_types_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	IntrospectionEndpoint             string   `json:"introspection_endpoint,omitempty"`
	UserInfoEndpoint                  string   `json:"userinfo_endpoint,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
}

type oidcAuthenticator struct {
	issuer string
	// configuredJWKSURL is the operator's explicit `jwks_url`, empty when the
	// URL has to be discovered from the issuer.
	configuredJWKSURL string
	audience          []string
	clockSkew         time.Duration
	// algorithms is the set of signing algorithms a JWKS key may advertise to be
	// considered for verification. jwx selects the algorithm from the key's own
	// `alg`, never from the token header, so filtering the key set is what makes
	// the configured list actually restrictive.
	algorithms map[jwa.SignatureAlgorithm]struct{}
	// tokenHeader carries the raw token when it does not arrive as
	// `Authorization: Bearer`. Empty means the standard header.
	tokenHeader string

	// resource is the RFC 9728 identity this block publishes, or nil when the
	// block declared no `resource` and so serves no discovery document.
	resource *protectedResource

	logger *zap.Logger

	// ctx is cancelled by Stop. Everything with a lifetime — the JWKS cache's
	// background refresher, any resolution attempt in flight — hangs off it, so
	// shutdown does not leave goroutines fetching from an issuer.
	ctx    context.Context
	cancel context.CancelFunc

	// The resolution state machine. Talking to the issuer is deferred out of
	// config processing entirely (see resolve), so these guard an attempt that
	// can happen at any time from any request.
	mu       sync.Mutex
	resolved *oidcResolution
	inflight chan struct{}
	lastErr  error
	nextTry  time.Time
	backoff  time.Duration
}

// oidcResolution is everything that had to be fetched from the issuer before a
// token can be verified. Built once, then read without further I/O.
type oidcResolution struct {
	jwksURL string
	cache   *jwk.Cache
	// meta is the discovery document, nil when jwks_url was explicit.
	meta *OIDCMetadata
	// cancel tears down the cache's background refresher. Also reached by
	// cancelling the authenticator's ctx, which this descends from; held so a
	// failed attempt can clean up after itself without waiting for shutdown.
	cancel context.CancelFunc
}

// oidcDefinition is the body of an `auth "oidc"` block.
type oidcDefinition struct {
	// Issuer is the OIDC issuer URL, used for discovery.
	Issuer string `hcl:"issuer,optional"`
	// JWKSUrl names the key endpoint directly, skipping discovery.
	JWKSUrl string `hcl:"jwks_url,optional"`
	// Audience lists acceptable `aud` values.
	Audience hcl.Expression `hcl:"audience,optional"`
	// Algorithms restricts which signing algorithms may verify a token.
	Algorithms hcl.Expression `hcl:"algorithms,optional"`
	// ClockSkew is the tolerance applied to exp and nbf.
	ClockSkew hcl.Expression `hcl:"clock_skew,optional"`
	// TokenHeader carries the bare token, for a proxy that presents it under
	// its own header rather than as `Authorization: Bearer`.
	TokenHeader string `hcl:"token_header,optional"`
	// Resource is the RFC 9728 resource identifier this block protects, which
	// turns on OAuth discovery for clients that know only an endpoint URL.
	Resource string `hcl:"resource,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

func init() {
	cfg.RegisterAuthType("oidc", processOIDCAuth, cfg.WithSchema(oidcAuthSchema))
}

var oidcAuthSchema = cfg.TypeSchema{
	Sample:  &oidcDefinition{},
	Summary: "A bearer token verified against an OIDC issuer's keys.",
	DocPage: "auth.md#oidc",
	Doc: `Verifies a JWT's signature against the issuer's published keys. Verification is
local, so no call is made to the issuer per request, and a token stays valid
until it expires — see ` + "[`introspection`](auth.md#introspection)" + ` where revocation must take
effect immediately.

The issuer's discovery document and keys are fetched on first use rather than at
startup, so an issuer that is unreachable does not stop the process from
starting. Until they arrive, protected routes answer ` + "`503`" + ` — never anonymous
access.`,
	Attrs: map[string]cfg.AttrMeta{
		"issuer": {
			Summary: "OIDC issuer URL.",
			Doc: "Its `/.well-known/openid-configuration` document is fetched to find the " +
				"key endpoint.",
			Hint: cfg.HintURL,
		},
		"jwks_url": {
			Summary: "Key endpoint, named directly.",
			Doc:     "Skips discovery, for an issuer that publishes no discovery document.",
			Hint:    cfg.HintURL,
		},
		"audience": {
			Summary: "Accepted `aud` values.",
			Doc: "The token must carry at least one of them. Without this, any token the " +
				"issuer signed is accepted, including one minted for a different " +
				"service — set it whenever the issuer serves more than this API.",
		},
		"algorithms": {
			Summary: "Permitted token signing algorithms.",
			Doc: "A key verifies a token only if the algorithm it advertises is listed " +
				"here, so narrowing the list narrows what the issuer can present. The " +
				"algorithm always comes from the key rather than from the token header, " +
				"which is attacker-controlled. Unrecognized names, an empty list, and " +
				"`\"none\"` are rejected at config load.",
			Default: `["RS256", "ES256"]`,
		},
		"clock_skew": {
			Summary: "Tolerance applied to `exp` and `nbf`.",
			Doc:     "Accepts a duration string, a number of seconds, or a duration value.",
			Hint:    cfg.HintDuration,
			Default: "30s",
		},
		"token_header": {
			Summary: "Header carrying the token, instead of `Authorization: Bearer`.",
			Doc: "For a reverse proxy that presents the token under its own name — " +
				"Cloudflare Access uses `Cf-Access-Jwt-Assertion`, an AWS ALB uses " +
				"`x-amzn-oidc-data`. The value is the bare token, with no `Bearer ` " +
				"prefix. The signature is still verified, so unlike " +
				"[`proxy`](auth.md#proxy) this needs no network-level trust in the proxy.",
			Default: "Authorization",
		},
		"resource": {
			Summary: "Resource identifier to publish for OAuth discovery (RFC 9728).",
			Doc: "Setting it serves a protected resource metadata document, and adds a " +
				"`resource_metadata` pointer to it on every `401`, so a client holding no " +
				"credentials and knowing only this server's URL can find the issuer and " +
				"obtain a token. This is what the MCP authorization spec requires; a client " +
				"configured with credentials already needs none of it.\n\n" +
				"Either an absolute URL, or a path resolved against the referencing " +
				"server's `external_url` — so `resource = \"/mcp\"` beside `handle \"/mcp\"`. " +
				"The value **must exactly match the URL clients are pointed at**, since a " +
				"client checks it against the URL it dialled and refuses a mismatch. A " +
				"relative value can therefore belong to only one `server \"http\"`.",
			Hint: cfg.HintURL,
		},
	},
	Constraints: []cfg.Constraint{
		cfg.MutuallyExclusive("issuer", "jwks_url").
			WithMessage("Setting jwks_url replaces discovery, which is what issuer is for."),
		cfg.AtLeastOneOf("issuer", "jwks_url").
			WithMessage("Verifying a token needs the issuer's keys, found either by discovery from issuer or directly at jwks_url."),
		cfg.Requires("resource", "issuer").
			WithMessage("Publishing a resource means telling clients which authorization server issues its tokens, and that is the issuer. A jwks_url names a key endpoint, which cannot be turned back into an issuer."),
	},
}

func processOIDCAuth(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Authenticator, hcl.Diagnostics) {
	def := oidcDefinition{}
	if diags := cfg.DecodeBody(body, config.EvalCtx(), &def); diags.HasErrors() {
		return nil, diags
	}

	if def.Issuer == "" && def.JWKSUrl == "" {
		return nil, authDiag(block, "Missing auth attribute",
			`auth "oidc" needs "issuer" (to discover the issuer's keys) or "jwks_url" (to name the key endpoint directly).`)
	}

	if def.Resource != "" && def.Issuer == "" {
		return nil, authDiag(block, "resource requires issuer",
			`Publishing a "resource" means telling clients which authorization server issues its tokens, `+
				`and authorization_servers carries the issuer identifier. A "jwks_url" names a key endpoint, `+
				`which cannot be turned back into an issuer — set "issuer" instead, or drop "resource".`)
	}

	a, err := newOIDCAuthenticator(&def, config.EvalCtx(), config.Logger)
	if err != nil {
		return nil, authDiag(block, "Invalid auth configuration", err.Error())
	}

	config.Startables = append(config.Startables, a)
	config.Stoppables = append(config.Stoppables, a)
	return a, nil
}

func newOIDCAuthenticator(ac *oidcDefinition, evalCtx *hcl.EvalContext, logger *zap.Logger) (*oidcAuthenticator, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	a := &oidcAuthenticator{
		algorithms: map[jwa.SignatureAlgorithm]struct{}{
			jwa.RS256(): {},
			jwa.ES256(): {},
		},
		clockSkew: 30 * time.Second,
		logger:    logger,
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())

	// Parse clock_skew (string, number, or duration capsule).
	if cfg.IsExpressionProvided(ac.ClockSkew) {
		val, diags := ac.ClockSkew.Value(evalCtx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("auth oidc: evaluating clock_skew: %w", diags)
		}
		d, err := cfg.ParseDurationFromValue(val)
		if err != nil {
			return nil, fmt.Errorf("auth oidc: invalid clock_skew: %w", err)
		}
		a.clockSkew = d
	}

	// Parse algorithms.
	if cfg.IsExpressionProvided(ac.Algorithms) {
		algsVal, diags := ac.Algorithms.Value(evalCtx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("auth oidc: evaluating algorithms: %w", diags)
		}
		algs, err := ctyStringList(algsVal, "algorithms")
		if err != nil {
			return nil, fmt.Errorf("auth oidc: %w", err)
		}
		if len(algs) == 0 {
			return nil, fmt.Errorf("auth oidc: algorithms must not be empty")
		}
		set := make(map[jwa.SignatureAlgorithm]struct{}, len(algs))
		for _, name := range algs {
			alg, ok := jwa.LookupSignatureAlgorithm(name)
			if !ok {
				return nil, fmt.Errorf("auth oidc: unknown signing algorithm %q", name)
			}
			if alg == jwa.NoSignature() {
				return nil, fmt.Errorf("auth oidc: signing algorithm %q is not permitted", name)
			}
			set[alg] = struct{}{}
		}
		a.algorithms = set
	}

	// Parse audience.
	if cfg.IsExpressionProvided(ac.Audience) {
		audVal, diags := ac.Audience.Value(evalCtx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("auth oidc: evaluating audience: %w", diags)
		}
		aud, err := ctyStringList(audVal, "audience")
		if err != nil {
			return nil, fmt.Errorf("auth oidc: %w", err)
		}
		a.audience = aud
	}

	// Record where verification material comes from. Nothing is fetched here:
	// see resolve.
	a.issuer = ac.Issuer
	a.configuredJWKSURL = ac.JWKSUrl
	a.tokenHeader = ac.TokenHeader

	// The identifier cannot be resolved yet: a relative resource is resolved
	// against the external_url of whichever server references this block, and no
	// server has been processed at this point. ContributeRoutes finishes it.
	if ac.Resource != "" {
		a.resource = newProtectedResource(ac.Resource, ac.DefRange, []string{ac.Issuer})
	}

	return a, nil
}

// ContributeRoutes serves this block's protected resource metadata document from
// the server that references it, which is where it has to live: the well-known
// URL sits at the host root, and a mounted handler never sees it.
func (a *oidcAuthenticator) ContributeRoutes(host cfg.AuthHost) ([]cfg.ContributedRoute, hcl.Diagnostics) {
	if a.resource == nil {
		return nil, nil
	}
	return a.resource.bind(host)
}

// ReportIfUnbound warns when this block declared a resource that no server "http"
// ever asked it to publish.
func (a *oidcAuthenticator) ReportIfUnbound(logger *zap.Logger) {
	if a.resource != nil {
		a.resource.reportIfUnbound(logger)
	}
}

// Start warms the authenticator so an unreachable issuer produces one log line
// at boot rather than first appearing as a rejected request.
//
// It neither blocks nor fails. An identity provider is somebody else's process
// on the other side of a network, and one being down at boot must not stop the
// rest of this one from starting — an HTTP listener with nothing to do with
// OIDC should still bind. Requests retry on their own schedule, so a provider
// that comes up late is recovered from without a restart.
func (a *oidcAuthenticator) Start() error {
	go func() {
		_, _ = a.resolve(a.ctx) // failure is logged by resolve
	}()
	return nil
}

// Stop cancels the JWKS cache's background refresher and any fetch in flight.
func (a *oidcAuthenticator) Stop() error {
	a.cancel()
	return nil
}

// resolve returns the verification material, fetching it from the issuer on
// first use and on any later attempt the backoff schedule allows.
//
// Doing this lazily rather than during config processing is what keeps a
// temporarily unreachable issuer from being fatal: `vinculum check` validates a
// config without needing the provider up, and `vinculum serve` starts and then
// heals when the provider appears.
//
// Three properties matter here:
//
//   - Single-flight. One attempt runs at a time; concurrent callers wait for it
//     rather than each opening their own connection to the issuer.
//   - Negative caching. A failure is remembered until nextTry, so a provider
//     that is down costs one request per backoff interval, not one per inbound
//     HTTP request.
//   - The attempt outlives its caller. Fetching runs in its own goroutine on
//     the authenticator's context, so the request that happened to trigger it
//     going away — a client disconnect, a handler timeout — does not abort work
//     every other waiter is depending on. Only the *waiting* is caller-scoped,
//     and that is true even for the caller that started the attempt.
func (a *oidcAuthenticator) resolve(ctx context.Context) (*oidcResolution, error) {
	for {
		a.mu.Lock()
		if a.resolved != nil {
			res := a.resolved
			a.mu.Unlock()
			return res, nil
		}
		if ch := a.inflight; ch != nil {
			a.mu.Unlock()
			select {
			case <-ch:
				continue // re-read the outcome under the lock
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-a.ctx.Done():
				return nil, a.ctx.Err()
			}
		}
		if time.Now().Before(a.nextTry) {
			err := a.lastErr
			a.mu.Unlock()
			return nil, err
		}
		ch := make(chan struct{})
		a.inflight = ch
		a.mu.Unlock()

		go a.runAttempt(ch)
		// Round the loop to wait on ch like any other caller.
	}
}

// runAttempt performs one resolution and records its outcome, closing done when
// the result is readable.
func (a *oidcAuthenticator) runAttempt(done chan struct{}) {
	res, err := a.attemptResolve()

	a.mu.Lock()
	a.inflight = nil
	var retryIn time.Duration
	if err == nil {
		a.resolved = res
		a.lastErr = nil
		a.backoff = 0
		a.nextTry = time.Time{}
	} else {
		a.lastErr = err
		a.backoff = nextBackoff(a.backoff)
		a.nextTry = time.Now().Add(a.backoff)
		retryIn = a.backoff
	}
	close(done)
	a.mu.Unlock()

	if err != nil {
		// Bounded by the backoff schedule, so this cannot become per-request
		// spam however much traffic arrives while the issuer is down.
		a.logger.Warn("OIDC issuer unreachable; requests will be rejected until it responds",
			zap.String("issuer", a.issuer),
			zap.Duration("retry_after", retryIn),
			zap.Error(err))
	}
}

// attemptResolve performs one full fetch: discovery if needed, then the JWKS.
func (a *oidcAuthenticator) attemptResolve() (*oidcResolution, error) {
	ctx, cancel := context.WithTimeout(a.ctx, authFetchTimeout)
	defer cancel()

	res := &oidcResolution{jwksURL: a.configuredJWKSURL}
	if res.jwksURL == "" {
		meta, err := fetchOIDCMetadata(ctx, a.issuer)
		if err != nil {
			return nil, fmt.Errorf("auth oidc: fetching discovery document: %w", err)
		}
		res.meta = meta
		res.jwksURL = meta.JWKSUri
	}

	// Two contexts, deliberately. cacheCtx bounds the *lifetime* of the cache's
	// background refresher, so it lasts until shutdown or until this attempt
	// gives up. ctx bounds this *attempt*, and Register takes it because
	// Register performs the first fetch and waits for it — handed a context
	// that only cancels, an unreachable JWKS endpoint blocks here forever.
	cacheCtx, cacheCancel := context.WithCancel(a.ctx)
	cache, err := jwk.NewCache(cacheCtx, httprc.NewClient(httprc.WithHTTPClient(authHTTPClient)))
	if err != nil {
		cacheCancel()
		return nil, fmt.Errorf("auth oidc: creating JWKS cache: %w", err)
	}
	if err := cache.Register(ctx, res.jwksURL, jwk.WithMinInterval(15*time.Minute)); err != nil {
		cacheCancel()
		return nil, fmt.Errorf("auth oidc: registering JWKS cache: %w", err)
	}
	if _, err := cache.Refresh(ctx, res.jwksURL); err != nil {
		cacheCancel()
		return nil, fmt.Errorf("auth oidc: initial JWKS fetch from %s: %w", res.jwksURL, err)
	}

	res.cache = cache
	res.cancel = cacheCancel
	return res, nil
}

// unavailable is the response to a request that arrived while the issuer could
// not be reached. It is a 503 and never a pass: a provider being down must not
// turn a protected route into an open one.
func (a *oidcAuthenticator) unavailable() *AuthFailure {
	a.mu.Lock()
	retryAfter := time.Until(a.nextTry)
	a.mu.Unlock()

	seconds := 1
	if retryAfter > 0 {
		seconds = int(math.Ceil(retryAfter.Seconds()))
	}

	return &AuthFailure{
		Status: http.StatusServiceUnavailable,
		Response: &types.HTTPResponseWrapper{
			Status:      http.StatusServiceUnavailable,
			Headers:     http.Header{"Retry-After": []string{strconv.Itoa(seconds)}},
			ContentType: "text/plain; charset=utf-8",
			Body:        []byte("Service Unavailable\n"),
			IsError:     true,
		},
	}
}

func (a *oidcAuthenticator) Method() string { return "oidc" }

// Claims recognizes a request carrying a token where this block expects one.
// Whether it verifies is Authenticate's business — a bad token must be rejected
// here rather than passed to the next mechanism on the route.
func (a *oidcAuthenticator) Claims(r *http.Request) bool {
	_, err := a.extractToken(r)
	return err == nil
}

// Challenge offers a Bearer challenge, carrying a pointer to the discovery
// document when this block publishes one.
//
// RFC 9728 §5.1 wants that pointer on every 401, not only on a request that
// arrived with no credential at all — a client whose token has expired needs to
// find the issuer just as much as one that never had a token. Every rejection
// path routes its WWW-Authenticate through here, so they all get it.
func (a *oidcAuthenticator) Challenge() string {
	// A token in a header of the proxy's choosing is not something a client can
	// be asked for, so there is no challenge to issue.
	if a.tokenHeader != "" {
		return ""
	}
	return a.bearerChallenge()
}

// bearerChallenge builds a Bearer challenge carrying params plus, when this block
// publishes one, the pointer to its discovery document.
func (a *oidcAuthenticator) bearerChallenge(params ...string) string {
	if param := a.resource.resourceMetadataChallenge(); param != "" {
		params = append(params, param)
	}
	if len(params) == 0 {
		return "Bearer"
	}
	return "Bearer " + strings.Join(params, ", ")
}

// extractToken reads the token from wherever this block expects it: the bare
// value of a configured header, or the standard Authorization: Bearer.
func (a *oidcAuthenticator) extractToken(r *http.Request) (string, error) {
	if a.tokenHeader == "" {
		return extractBearerToken(r)
	}
	token := strings.TrimSpace(r.Header.Get(a.tokenHeader))
	if token == "" {
		return "", fmt.Errorf("missing %s header", a.tokenHeader)
	}
	return token, nil
}

func (a *oidcAuthenticator) Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
	// Checked before anything is resolved: a request with no credential is
	// wrong whatever the issuer's state, and answering costs no I/O.
	token, err := a.extractToken(r)
	if err != nil {
		return cty.NilVal, &AuthFailure{
			Status:          http.StatusUnauthorized,
			WWWAuthenticate: a.Challenge(),
		}, nil
	}

	res, err := a.resolve(r.Context())
	if err != nil {
		return cty.NilVal, a.unavailable(), nil
	}

	keySet, err := res.cache.Lookup(r.Context(), res.jwksURL)
	if err != nil {
		return cty.NilVal, nil, fmt.Errorf("auth oidc: getting JWKS: %w", err)
	}

	parsed, err := a.parseToken(token, keySet)
	if err != nil {
		// Try refreshing the cache once (handles key rotation / unknown kid).
		if refreshed, refreshErr := res.cache.Refresh(r.Context(), res.jwksURL); refreshErr == nil {
			parsed, err = a.parseToken(token, refreshed)
		}
		if err != nil {
			// A rejected token needs the discovery pointer as much as a missing
			// one does — an expired token is the ordinary way a working client
			// arrives here, and it has to find the issuer again to renew.
			return cty.NilVal, &AuthFailure{
				Status:          http.StatusUnauthorized,
				WWWAuthenticate: a.bearerChallenge(`error="invalid_token"`),
			}, nil
		}
	}

	// Audience check.
	if len(a.audience) > 0 {
		if !tokenHasAudience(parsed, a.audience) {
			return cty.NilVal, &AuthFailure{Status: http.StatusForbidden}, nil
		}
	}

	authVal := jwtTokenToCty(parsed)
	return authVal, nil, nil
}

// parseToken verifies and validates the token against the permitted subset of
// the key set.
func (a *oidcAuthenticator) parseToken(token string, keySet jwk.Set) (jwt.Token, error) {
	return jwt.Parse([]byte(token),
		jwt.WithKeySet(a.permittedKeys(keySet)),
		jwt.WithValidate(true),
		jwt.WithAcceptableSkew(a.clockSkew),
	)
}

// permittedKeys returns the subset of keySet whose advertised `alg` is in the
// configured algorithms list.
//
// jwx chooses the verification algorithm from the key's own `alg` rather than
// from the (attacker-controlled) token header, and skips keys that advertise
// none. Restricting which keys are offered is therefore exactly equivalent to
// restricting which algorithms may verify a token.
//
// Anything unusable is skipped rather than reported: this runs per request, and
// a JWKS the issuer serves is not something an operator can fix from here, so
// one odd entry must not fail every request. A key excluded for any reason
// simply cannot verify a token, which is the intended outcome.
//
// Skipping is the whole error strategy — this cannot fail. AddKey rejects only
// a nil key or one already in the set by interface identity, and neither is
// reachable from iterating a parsed set into an empty one.
func (a *oidcAuthenticator) permittedKeys(keySet jwk.Set) jwk.Set {
	permitted := jwk.NewSet()
	for i := range keySet.Len() {
		key, ok := keySet.Key(i)
		if !ok {
			continue
		}
		keyAlg, ok := key.Algorithm()
		if !ok {
			// No `alg`; jwx would refuse to use it anyway.
			continue
		}
		alg, ok := jwa.LookupSignatureAlgorithm(keyAlg.String())
		if !ok {
			continue
		}
		if _, ok := a.algorithms[alg]; !ok {
			continue
		}
		_ = permitted.AddKey(key) // unreachable failure; see above
	}
	return permitted
}

// fetchOIDCMetadata retrieves the OIDC discovery document from the issuer.
func fetchOIDCMetadata(ctx context.Context, issuer string) (*OIDCMetadata, error) {
	discoveryURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", discoveryURL, err)
	}
	resp, err := authHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", discoveryURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", discoveryURL, resp.StatusCode)
	}
	var meta OIDCMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, fmt.Errorf("decoding discovery document: %w", err)
	}
	if meta.JWKSUri == "" {
		return nil, fmt.Errorf("discovery document missing jwks_uri")
	}
	return &meta, nil
}

// extractBearerToken extracts the token from the Authorization: Bearer header.
func extractBearerToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", fmt.Errorf("Authorization header is not Bearer")
	}
	token := strings.TrimSpace(auth[len(prefix):])
	if token == "" {
		return "", fmt.Errorf("empty Bearer token")
	}
	return token, nil
}

// tokenHasAudience checks whether any element of required appears in the token's aud claim.
func tokenHasAudience(token jwt.Token, required []string) bool {
	tokenAud, ok := token.Audience()
	if !ok {
		return false
	}
	for _, req := range required {
		for _, aud := range tokenAud {
			if aud == req {
				return true
			}
		}
	}
	return false
}

// jwtTokenToCty converts a parsed JWT token to the ctx.auth cty object.
func jwtTokenToCty(token jwt.Token) cty.Value {
	claims := map[string]cty.Value{}

	// Standard claims.
	subject, _ := token.Subject()
	claims["sub"] = cty.StringVal(subject)
	if iss, ok := token.Issuer(); ok && iss != "" {
		claims["iss"] = cty.StringVal(iss)
	}
	if auds, ok := token.Audience(); ok && len(auds) > 0 {
		audVals := make([]cty.Value, len(auds))
		for i, a := range auds {
			audVals[i] = cty.StringVal(a)
		}
		claims["aud"] = cty.ListVal(audVals)
	}
	if iat, ok := token.IssuedAt(); ok && !iat.IsZero() {
		claims["iat"] = cty.NumberIntVal(iat.Unix())
	}
	if exp, ok := token.Expiration(); ok && !exp.IsZero() {
		claims["exp"] = cty.NumberIntVal(exp.Unix())
	}

	// Private claims.
	for _, key := range token.Keys() {
		// Skip standard claims already handled.
		switch key {
		case "sub", "iss", "aud", "iat", "exp", "nbf", "jti":
			continue
		}
		var raw any
		if err := token.Get(key, &raw); err != nil {
			continue
		}
		if v := anyToCty(raw); v != cty.NilVal {
			claims[key] = v
		}
	}

	claimsVal := cty.ObjectVal(claims)

	var usernameVal cty.Value
	if pu, ok := claims["preferred_username"]; ok && pu.Type() == cty.String {
		usernameVal = pu
	} else {
		usernameVal = cty.NullVal(cty.DynamicPseudoType)
	}

	return cty.ObjectVal(map[string]cty.Value{
		"username": usernameVal,
		"subject":  cty.StringVal(subject),
		"claims":   claimsVal,
	})
}

// anyToCty converts a JWT claim value (from the lestrrat-go/jwx iteration) to cty.
func anyToCty(v any) cty.Value {
	switch val := v.(type) {
	case string:
		return cty.StringVal(val)
	case bool:
		return cty.BoolVal(val)
	case float64:
		return cty.NumberFloatVal(val)
	case int64:
		return cty.NumberIntVal(val)
	case []string:
		if len(val) == 0 {
			return cty.ListValEmpty(cty.String)
		}
		elems := make([]cty.Value, len(val))
		for i, s := range val {
			elems[i] = cty.StringVal(s)
		}
		return cty.ListVal(elems)
	case []interface{}:
		if len(val) == 0 {
			return cty.ListValEmpty(cty.String)
		}
		elems := make([]cty.Value, 0, len(val))
		for _, item := range val {
			cv := anyToCty(item)
			if cv != cty.NilVal {
				elems = append(elems, cv)
			}
		}
		if len(elems) == 0 {
			return cty.ListValEmpty(cty.String)
		}
		return cty.TupleVal(elems)
	default:
		// Skip types we can't represent cleanly.
		return cty.NilVal
	}
}

// ctyStringList converts a cty list/tuple/set of strings to a []string.
func ctyStringList(val cty.Value, name string) ([]string, error) {
	if !val.IsKnown() || val.IsNull() {
		return nil, fmt.Errorf("%s must not be null", name)
	}
	ty := val.Type()
	if !ty.IsListType() && !ty.IsTupleType() && !ty.IsSetType() {
		return nil, fmt.Errorf("%s must be a list of strings, got %s", name, ty.FriendlyName())
	}
	var result []string
	for it := val.ElementIterator(); it.Next(); {
		_, v := it.Element()
		if v.Type() != cty.String {
			return nil, fmt.Errorf("%s elements must be strings", name)
		}
		result = append(result, v.AsString())
	}
	return result, nil
}
