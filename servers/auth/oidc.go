package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
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
	jwksURL    string
	audience   []string
	clockSkew  time.Duration
	algorithms []string
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
		algorithms: []string{"RS256", "ES256"},
		clockSkew:  30 * time.Second,
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
		a.algorithms = algs
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
		cache := jwk.NewCache(context.Background())
		if err := cache.Register(jwksURL, jwk.WithMinRefreshInterval(15*time.Minute)); err != nil {
			return nil, fmt.Errorf("auth oidc: registering JWKS cache: %w", err)
		}
		// Perform an initial fetch to catch config errors at startup.
		if _, err := cache.Refresh(context.Background(), jwksURL); err != nil {
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

	keySet, err := a.cache.Get(r.Context(), a.jwksURL)
	if err != nil {
		return cty.NilVal, nil, fmt.Errorf("auth oidc: getting JWKS: %w", err)
	}

	parsed, err := jwt.Parse([]byte(token),
		jwt.WithKeySet(keySet),
		jwt.WithValidate(true),
		jwt.WithAcceptableSkew(a.clockSkew),
	)
	if err != nil {
		// Try refreshing the cache once (handles key rotation / unknown kid).
		if _, refreshErr := a.cache.Refresh(r.Context(), a.jwksURL); refreshErr == nil {
			keySet, _ = a.cache.Get(r.Context(), a.jwksURL)
		}
		parsed, err = jwt.Parse([]byte(token),
			jwt.WithKeySet(keySet),
			jwt.WithValidate(true),
			jwt.WithAcceptableSkew(a.clockSkew),
		)
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
	tokenAud := token.Audience()
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
	claims["sub"] = cty.StringVal(token.Subject())
	if token.Issuer() != "" {
		claims["iss"] = cty.StringVal(token.Issuer())
	}
	for _, aud := range token.Audience() {
		_ = aud // aud is a []string; add as list below
	}
	if auds := token.Audience(); len(auds) > 0 {
		audVals := make([]cty.Value, len(auds))
		for i, a := range auds {
			audVals[i] = cty.StringVal(a)
		}
		claims["aud"] = cty.ListVal(audVals)
	}
	if !token.IssuedAt().IsZero() {
		claims["iat"] = cty.NumberIntVal(token.IssuedAt().Unix())
	}
	if !token.Expiration().IsZero() {
		claims["exp"] = cty.NumberIntVal(token.Expiration().Unix())
	}

	// Private claims.
	for pair := token.Iterate(context.Background()); pair.Next(context.Background()); {
		p := pair.Pair()
		key, ok := p.Key.(string)
		if !ok {
			continue
		}
		// Skip standard claims already handled.
		switch key {
		case "sub", "iss", "aud", "iat", "exp", "nbf", "jti":
			continue
		}
		if v := anyToCty(p.Value); v != cty.NilVal {
			claims[key] = v
		}
	}

	claimsVal := cty.ObjectVal(claims)

	subject := token.Subject()

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
