package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
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
	jwksURL   string
	audience  []string
	clockSkew time.Duration
	// algorithms is the set of signing algorithms a JWKS key may advertise to be
	// considered for verification. jwx selects the algorithm from the key's own
	// `alg`, never from the token header, so filtering the key set is what makes
	// the configured list actually restrictive.
	algorithms map[jwa.SignatureAlgorithm]struct{}
	cache      *jwk.Cache
	// cachedMeta holds the OIDC discovery document for re-serving.
	cachedMeta *OIDCMetadata
	// useIntrospect switches from local JWT validation to introspection.
	useIntrospect      bool
	introspectURL      string
	introspectClientID string
	introspectSecret   string
}

func newOIDCAuthenticator(ac *cfg.AuthConfig, evalCtx *hcl.EvalContext) (Authenticator, error) {
	a := &oidcAuthenticator{
		algorithms: map[jwa.SignatureAlgorithm]struct{}{
			jwa.RS256(): {},
			jwa.ES256(): {},
		},
		clockSkew: 30 * time.Second,
	}

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

	// Determine JWKS URL (via discovery or explicit override).
	if ac.IntrospectUrl != "" {
		// Use introspection instead of local JWKS validation.
		a.useIntrospect = true
		a.introspectURL = ac.IntrospectUrl
		a.introspectClientID = ac.IntrospectClientID
		a.introspectSecret = ac.IntrospectClientSecret
	} else {
		jwksURL := ac.JWKSUrl
		if jwksURL == "" {
			// Fetch OIDC discovery document to find JWKS URL.
			meta, err := fetchOIDCMetadata(ac.Issuer)
			if err != nil {
				return nil, fmt.Errorf("auth oidc: fetching discovery document: %w", err)
			}
			jwksURL = meta.JWKSUri
			a.cachedMeta = meta
		}
		a.jwksURL = jwksURL

		// Set up JWKS cache with background refresh.
		ctx := context.Background()
		cache, err := jwk.NewCache(ctx, httprc.NewClient())
		if err != nil {
			return nil, fmt.Errorf("auth oidc: creating JWKS cache: %w", err)
		}
		if err := cache.Register(ctx, jwksURL, jwk.WithMinInterval(15*time.Minute)); err != nil {
			return nil, fmt.Errorf("auth oidc: registering JWKS cache: %w", err)
		}
		// Perform an initial fetch to catch config errors at startup.
		if _, err := cache.Refresh(ctx, jwksURL); err != nil {
			return nil, fmt.Errorf("auth oidc: initial JWKS fetch from %s: %w", jwksURL, err)
		}
		a.cache = cache
	}

	return a, nil
}

// CachedMetadata returns the OIDC discovery document fetched at startup.
// Returns nil if jwks_url was provided directly (no discovery performed).
func (a *oidcAuthenticator) CachedMetadata() *OIDCMetadata {
	return a.cachedMeta
}

func (a *oidcAuthenticator) Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
	token, err := extractBearerToken(r)
	if err != nil {
		return cty.NilVal, &AuthFailure{
			Status:          http.StatusUnauthorized,
			WWWAuthenticate: "Bearer",
		}, nil
	}

	if a.useIntrospect {
		return introspectToken(token, a.introspectURL, a.introspectClientID, a.introspectSecret, a.audience)
	}

	keySet, err := a.cache.Lookup(r.Context(), a.jwksURL)
	if err != nil {
		return cty.NilVal, nil, fmt.Errorf("auth oidc: getting JWKS: %w", err)
	}

	parsed, err := a.parseToken(token, keySet)
	if err != nil {
		// Try refreshing the cache once (handles key rotation / unknown kid).
		if refreshed, refreshErr := a.cache.Refresh(r.Context(), a.jwksURL); refreshErr == nil {
			parsed, err = a.parseToken(token, refreshed)
		}
		if err != nil {
			return cty.NilVal, &AuthFailure{
				Status:          http.StatusUnauthorized,
				WWWAuthenticate: `Bearer error="invalid_token"`,
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
func fetchOIDCMetadata(issuer string) (*OIDCMetadata, error) {
	discoveryURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	resp, err := http.Get(discoveryURL) //nolint:gosec // URL comes from trusted config
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
