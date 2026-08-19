package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

type introspectionAuthenticator struct {
	introspectURL string
	clientID      string
	clientSecret  string
	audience      []string
	cacheTTL      time.Duration

	// cancel stops the sweep goroutine. Without it the loop is started on
	// context.Background() and runs for the life of the process, one per
	// authenticator built.
	cancel context.CancelFunc

	mu    sync.Mutex
	cache map[string]introspectionCacheEntry
}

type introspectionCacheEntry struct {
	result    cty.Value
	failure   *AuthFailure
	expiresAt time.Time
}

// introspectionResponse is the RFC 7662 introspection response body.
type introspectionResponse struct {
	Active   bool                   `json:"active"`
	Subject  string                 `json:"sub,omitempty"`
	ClientID string                 `json:"client_id,omitempty"`
	Username string                 `json:"username,omitempty"`
	Scope    string                 `json:"scope,omitempty"`
	Audience interface{}            `json:"aud,omitempty"`
	Exp      int64                  `json:"exp,omitempty"`
	Extra    map[string]interface{} `json:"-"`
}

// introspectionDefinition is the body of an `auth "introspection"` block.
type introspectionDefinition struct {
	// IntrospectUrl is the RFC 7662 introspection endpoint.
	IntrospectUrl string `hcl:"introspect_url"`
	// ClientID and ClientSecret authenticate this server to that endpoint.
	ClientID     string `hcl:"client_id"`
	ClientSecret string `hcl:"client_secret"`
	// Audience lists acceptable `aud` values.
	Audience hcl.Expression `hcl:"audience,optional"`
	// CacheTTL bounds how long an introspection result is reused.
	CacheTTL hcl.Expression `hcl:"cache_ttl,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

func init() {
	cfg.RegisterAuthType("introspection", processIntrospectionAuth, cfg.WithSchema(introspectionAuthSchema))
}

var introspectionAuthSchema = cfg.TypeSchema{
	Sample:  &introspectionDefinition{},
	Summary: "A bearer token checked with the authorization server.",
	DocPage: "auth.md#introspection",
	Doc: `Asks the authorization server about each token, through its
[RFC 7662](https://datatracker.ietf.org/doc/html/rfc7662) introspection endpoint.

This sees a revoked token immediately, which a signature check cannot — the cost
is a round trip per request, which ` + "`cache_ttl`" + ` trades back against how quickly
revocation takes effect. Where a locally verifiable signature is enough,
` + "[`oidc`](auth.md#oidc)" + ` makes no call at all.

Use it for opaque tokens, which carry nothing to verify locally, and wherever a
revocation has to take effect before the token would have expired on its own.`,
	Attrs: map[string]cfg.AttrMeta{
		"introspect_url": {
			Summary: "RFC 7662 token introspection endpoint.",
			Hint:    cfg.HintURL,
		},
		"client_id": {
			Summary: "Client ID this server presents to the introspection endpoint.",
		},
		"client_secret": {
			Summary: "Client secret this server presents to the introspection endpoint.",
			Doc:     "Supply it from the environment rather than as a literal.",
		},
		"audience": {
			Summary: "Accepted `aud` values.",
			Doc:     "The introspection response must carry at least one of them.",
		},
		"cache_ttl": {
			Summary: "How long to reuse an introspection result.",
			Doc: "Zero calls the endpoint on every request, so a revoked token stops " +
				"working at once. A non-zero value trades that immediacy for the round " +
				"trip: a token revoked at the authorization server keeps working here " +
				"for up to this long.",
			Hint:    cfg.HintDuration,
			Default: "0s",
		},
	},
}

func processIntrospectionAuth(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Authenticator, hcl.Diagnostics) {
	def := introspectionDefinition{}
	if diags := gohcl.DecodeBody(body, config.EvalCtx(), &def); diags.HasErrors() {
		return nil, diags
	}

	a, err := newIntrospectionAuthenticator(&def, config.EvalCtx())
	if err != nil {
		return nil, authDiag(block, "Invalid auth configuration", err.Error())
	}

	config.Stoppables = append(config.Stoppables, a)
	return a, nil
}

func newIntrospectionAuthenticator(ac *introspectionDefinition, evalCtx *hcl.EvalContext) (*introspectionAuthenticator, error) {
	a := &introspectionAuthenticator{
		introspectURL: ac.IntrospectUrl,
		clientID:      ac.ClientID,
		clientSecret:  ac.ClientSecret,
	}

	if cfg.IsExpressionProvided(ac.CacheTTL) {
		val, diags := ac.CacheTTL.Value(evalCtx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("auth introspection: evaluating cache_ttl: %w", diags)
		}
		d, err := cfg.ParseDurationFromValue(val)
		if err != nil {
			return nil, fmt.Errorf("auth introspection: invalid cache_ttl: %w", err)
		}
		a.cacheTTL = d
	}

	if cfg.IsExpressionProvided(ac.Audience) {
		audVal, diags := ac.Audience.Value(evalCtx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("auth introspection: evaluating audience: %w", diags)
		}
		aud, err := ctyStringList(audVal, "audience")
		if err != nil {
			return nil, fmt.Errorf("auth introspection: %w", err)
		}
		a.audience = aud
	}

	if a.cacheTTL > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		a.cancel = cancel
		a.cache = make(map[string]introspectionCacheEntry)
		go a.sweepLoop(ctx)
	}

	return a, nil
}

// Stop ends the cache sweep.
func (a *introspectionAuthenticator) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}
	return nil
}

// sweepLoop periodically removes expired cache entries to bound memory growth.
func (a *introspectionAuthenticator) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(a.cacheTTL / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			a.mu.Lock()
			for token, entry := range a.cache {
				if now.After(entry.expiresAt) {
					delete(a.cache, token)
				}
			}
			a.mu.Unlock()
		}
	}
}

func (a *introspectionAuthenticator) Method() string { return "introspection" }

// Claims recognizes a request carrying a bearer token. Whether the
// authorization server accepts it is Authenticate's business.
func (a *introspectionAuthenticator) Claims(r *http.Request) bool {
	_, err := extractBearerToken(r)
	return err == nil
}

func (a *introspectionAuthenticator) Challenge() string { return "Bearer" }

func (a *introspectionAuthenticator) Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
	token, err := extractBearerToken(r)
	if err != nil {
		return cty.NilVal, &AuthFailure{
			Status:          http.StatusUnauthorized,
			WWWAuthenticate: "Bearer",
		}, nil
	}

	// Check cache.
	if a.cache != nil {
		a.mu.Lock()
		if entry, ok := a.cache[token]; ok && time.Now().Before(entry.expiresAt) {
			result, failure := entry.result, entry.failure
			a.mu.Unlock()
			return result, failure, nil
		}
		a.mu.Unlock()
	}

	authVal, failure, introspectErr := introspectToken(r.Context(), token, a.introspectURL, a.clientID, a.clientSecret, a.audience)
	if introspectErr != nil {
		return cty.NilVal, nil, introspectErr
	}

	// Store in cache.
	if a.cache != nil && a.cacheTTL > 0 {
		a.mu.Lock()
		a.cache[token] = introspectionCacheEntry{
			result:    authVal,
			failure:   failure,
			expiresAt: time.Now().Add(a.cacheTTL),
		}
		a.mu.Unlock()
	}

	return authVal, failure, nil
}

// introspectToken calls the RFC 7662 introspection endpoint and returns the
// auth value or failure. Shared by both oidcAuthenticator (introspect mode)
// and introspectionAuthenticator.
func introspectToken(ctx context.Context, token, introspectURL, clientID, clientSecret string, audience []string) (cty.Value, *AuthFailure, error) {
	form := url.Values{}
	form.Set("token", token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, introspectURL, strings.NewReader(form.Encode()))
	if err != nil {
		return cty.NilVal, nil, fmt.Errorf("auth: building introspection request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)

	resp, err := authHTTPClient.Do(req)
	if err != nil {
		return cty.NilVal, nil, fmt.Errorf("auth: introspection request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return cty.NilVal, &AuthFailure{
			Status:          http.StatusUnauthorized,
			WWWAuthenticate: `Bearer error="invalid_token"`,
		}, nil
	}

	// Parse the raw JSON so we get all fields.
	var raw map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return cty.NilVal, nil, fmt.Errorf("auth: decoding introspection response: %w", err)
	}

	active, _ := raw["active"].(bool)
	if !active {
		return cty.NilVal, &AuthFailure{
			Status:          http.StatusUnauthorized,
			WWWAuthenticate: `Bearer error="invalid_token"`,
		}, nil
	}

	// Audience check.
	if len(audience) > 0 {
		if !introspectHasAudience(raw["aud"], audience) {
			return cty.NilVal, &AuthFailure{Status: http.StatusForbidden}, nil
		}
	}

	// Build claims cty object from all returned fields.
	claims := map[string]cty.Value{}
	for k, v := range raw {
		if k == "active" {
			continue
		}
		if cv := anyToCty(v); cv != cty.NilVal {
			claims[k] = cv
		}
	}
	var claimsVal cty.Value
	if len(claims) > 0 {
		claimsVal = cty.ObjectVal(claims)
	} else {
		claimsVal = cty.EmptyObjectVal
	}

	subject := ""
	if s, ok := raw["sub"].(string); ok {
		subject = s
	}

	var usernameVal cty.Value
	if u, ok := raw["username"].(string); ok && u != "" {
		usernameVal = cty.StringVal(u)
	} else {
		usernameVal = cty.NullVal(cty.DynamicPseudoType)
	}

	authVal := cty.ObjectVal(map[string]cty.Value{
		"username": usernameVal,
		"subject":  cty.StringVal(subject),
		"claims":   claimsVal,
	})
	return authVal, nil, nil
}

// introspectHasAudience checks whether the aud field (string or []string) contains any required value.
func introspectHasAudience(raw interface{}, required []string) bool {
	switch v := raw.(type) {
	case string:
		for _, req := range required {
			if v == req {
				return true
			}
		}
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				for _, req := range required {
					if s == req {
						return true
					}
				}
			}
		}
	}
	return false
}
