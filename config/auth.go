package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

// AuthConfig holds the configuration for an auth block. It is designed to be
// embedded (as a pointer) in any server or handler block that supports auth,
// decoded by gohcl.
//
// The block label selects the authentication mode:
//
//	auth "basic"  { realm = "API", credentials = { alice = "s3cr3t" } }
//	auth "oidc"   { issuer = "https://accounts.example.com", audience = ["api.example.com"] }
//	auth "oauth2" { introspect_url = "...", client_id = "...", client_secret = env.SECRET }
//	auth "custom" { action = lookup_session(ctx.request.cookie("session")) }
//	auth "none"   {}   # explicitly opt out of inherited server-level auth
type AuthConfig struct {
	Mode string `hcl:",label"` // "basic" | "oidc" | "oauth2" | "custom" | "none"

	// basic — realm shown in WWW-Authenticate header; defaults to server name
	Realm string `hcl:"realm,optional"`
	// basic — map(string) expression; username → plaintext password
	Credentials hcl.Expression `hcl:"credentials,optional"`

	// basic + custom — expression evaluated per request
	Action hcl.Expression `hcl:"action,optional"`

	// oidc + oauth2 — required aud values; token must contain at least one
	Audience hcl.Expression `hcl:"audience,optional"` // list(string)

	// oidc — OIDC issuer URL (used for discovery)
	Issuer string `hcl:"issuer,optional"`
	// oidc — override the JWKS endpoint (disables OIDC discovery)
	JWKSUrl string `hcl:"jwks_url,optional"`
	// oidc — permitted signing algorithms; defaults to ["RS256","ES256"]
	Algorithms hcl.Expression `hcl:"algorithms,optional"` // list(string)
	// oidc — permitted clock skew for exp/nbf; defaults to "30s".
	// Accepts a string ("30s"), a number (seconds), or a duration capsule.
	ClockSkew hcl.Expression `hcl:"clock_skew,optional"`

	// oidc (introspection) + oauth2 — RFC 7662 introspection endpoint
	IntrospectUrl string `hcl:"introspect_url,optional"`

	// oidc introspection — client ID for introspection endpoint
	IntrospectClientID string `hcl:"introspect_client_id,optional"`
	// oidc introspection — client secret for introspection endpoint
	IntrospectClientSecret string `hcl:"introspect_client_secret,optional"`

	// oauth2 — client ID for introspection endpoint (required)
	ClientID string `hcl:"client_id,optional"`
	// oauth2 — client secret (required; use env.VAR)
	ClientSecret string `hcl:"client_secret,optional"`
	// oauth2 — cache TTL for introspection results; defaults to "0s" (no cache).
	// Accepts a string ("60s"), a number (seconds), or a duration capsule.
	CacheTTL hcl.Expression `hcl:"cache_ttl,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

// ValidateAuthConfig checks that the required fields for the selected mode are
// present and that no conflicting options were set. Returns HCL diagnostics on error.
func ValidateAuthConfig(ac *AuthConfig) hcl.Diagnostics {
	var diags hcl.Diagnostics

	add := func(summary, detail string) {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  summary,
			Detail:   detail,
			Subject:  &ac.DefRange,
		})
	}

	switch ac.Mode {
	case "none":
		// No attributes required or allowed.

	case "basic":
		hasCredentials := IsExpressionProvided(ac.Credentials)
		hasAction := IsExpressionProvided(ac.Action)
		if hasCredentials && hasAction {
			add("Conflicting auth attributes",
				`auth "basic" requires exactly one of "credentials" or "action", not both.`)
		} else if !hasCredentials && !hasAction {
			add("Missing auth attribute",
				`auth "basic" requires either "credentials" (map of username→password) or "action" (expression).`)
		}

	case "oidc":
		hasIssuer := ac.Issuer != ""
		hasJWKS := ac.JWKSUrl != ""
		hasIntrospect := ac.IntrospectUrl != ""
		if !hasIssuer && !hasJWKS {
			add("Missing auth attribute",
				`auth "oidc" requires "issuer" (for OIDC discovery) or "jwks_url" (to skip discovery).`)
		}
		if hasJWKS && hasIntrospect {
			add("Conflicting auth attributes",
				`auth "oidc": "jwks_url" and "introspect_url" are mutually exclusive.`)
		}
		if hasIntrospect {
			if ac.IntrospectClientID == "" {
				add("Missing auth attribute", `auth "oidc" with "introspect_url" requires "introspect_client_id".`)
			}
			if ac.IntrospectClientSecret == "" {
				add("Missing auth attribute", `auth "oidc" with "introspect_url" requires "introspect_client_secret".`)
			}
		}

	case "oauth2":
		if ac.IntrospectUrl == "" {
			add("Missing auth attribute", `auth "oauth2" requires "introspect_url".`)
		}
		if ac.ClientID == "" {
			add("Missing auth attribute", `auth "oauth2" requires "client_id".`)
		}
		if ac.ClientSecret == "" {
			add("Missing auth attribute", `auth "oauth2" requires "client_secret".`)
		}

	case "custom":
		if !IsExpressionProvided(ac.Action) {
			add("Missing auth attribute", `auth "custom" requires an "action" expression.`)
		}

	default:
		add("Invalid auth mode",
			fmt.Sprintf("Unknown auth mode %q. Valid modes are: basic, oidc, oauth2, custom, none.", ac.Mode))
	}

	return diags
}
