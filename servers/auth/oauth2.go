package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

type oauth2Authenticator struct {
	introspectURL string
	clientID      string
	clientSecret  string
	audience      []string
	cacheTTL      time.Duration

	mu    sync.Mutex
	cache map[string]oauth2CacheEntry
}

type oauth2CacheEntry struct {
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

func newOAuth2Authenticator(ac *cfg.AuthConfig, evalCtx *hcl.EvalContext) (Authenticator, error) {
	a := &oauth2Authenticator{
		introspectURL: ac.IntrospectUrl,
		clientID:      ac.ClientID,
		clientSecret:  ac.ClientSecret,
	}

	if cfg.IsExpressionProvided(ac.CacheTTL) {
		val, diags := ac.CacheTTL.Value(evalCtx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("auth oauth2: evaluating cache_ttl: %w", diags)
		}
		d, err := cfg.ParseDurationFromValue(val)
		if err != nil {
			return nil, fmt.Errorf("auth oauth2: invalid cache_ttl: %w", err)
		}
		a.cacheTTL = d
	}

	if cfg.IsExpressionProvided(ac.Audience) {
		audVal, diags := ac.Audience.Value(evalCtx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("auth oauth2: evaluating audience: %w", diags)
		}
		aud, err := ctyStringList(audVal, "audience")
		if err != nil {
			return nil, fmt.Errorf("auth oauth2: %w", err)
		}
		a.audience = aud
	}

	if a.cacheTTL > 0 {
		a.cache = make(map[string]oauth2CacheEntry)
	}

	return a, nil
}

func (a *oauth2Authenticator) Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
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

	authVal, failure, introspectErr := introspectToken(token, a.introspectURL, a.clientID, a.clientSecret, a.audience)
	if introspectErr != nil {
		return cty.NilVal, nil, introspectErr
	}

	// Store in cache.
	if a.cache != nil && a.cacheTTL > 0 {
		a.mu.Lock()
		a.cache[token] = oauth2CacheEntry{
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
// and oauth2Authenticator.
func introspectToken(token, introspectURL, clientID, clientSecret string, audience []string) (cty.Value, *AuthFailure, error) {
	form := url.Values{}
	form.Set("token", token)

	req, err := http.NewRequest(http.MethodPost, introspectURL, strings.NewReader(form.Encode()))
	if err != nil {
		return cty.NilVal, nil, fmt.Errorf("auth: building introspection request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)

	resp, err := http.DefaultClient.Do(req)
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
