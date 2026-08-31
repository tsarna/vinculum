package auth

import (
	"net/http"
	"strings"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

// proxyDefinition is the body of an `auth "proxy"` block.
type proxyDefinition struct {
	// TrustedProxies lists the networks whose identity headers are believed.
	TrustedProxies []string `hcl:"trusted_proxies"`
	// UserHeader, EmailHeader, and GroupsHeader name the headers to read.
	UserHeader   string `hcl:"user_header,optional"`
	EmailHeader  string `hcl:"email_header,optional"`
	GroupsHeader string `hcl:"groups_header,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

// Defaults matching oauth2-proxy's `--pass-user-headers` set, which several
// other proxies have adopted.
const (
	defaultProxyUserHeader   = "X-Forwarded-User"
	defaultProxyEmailHeader  = "X-Forwarded-Email"
	defaultProxyGroupsHeader = "X-Forwarded-Groups"
)

func init() {
	cfg.RegisterAuthType("proxy", processProxyAuth, cfg.WithSchema(proxyAuthSchema))
}

var proxyAuthSchema = cfg.TypeSchema{
	Sample:  &proxyDefinition{},
	Summary: "Identity asserted by a trusted reverse proxy.",
	DocPage: "auth.md#proxy",
	Doc: `Reads identity headers set by a proxy that has already authenticated the user:
oauth2-proxy, Cloudflare Access, nginx ` + "`auth_request`" + `, Traefik ForwardAuth, Envoy
ext_authz, Pomerium, Authelia, Ory Oathkeeper.

**These headers are plaintext, so the only thing making them trustworthy is that
the request came from the proxy.** Two conditions have to hold, and only the
first is enforceable here:

1. Vinculum must not be reachable except through the proxy. ` + "`trusted_proxies`" + `
   rejects a request arriving from anywhere else, but it cannot help if an
   attacker can route through the proxy's own address.
2. The proxy must **replace** these headers on every request, never append to
   what the client sent. Only the proxy's configuration can guarantee that.

Where the proxy can pass on the token it verified — oauth2-proxy's
` + "`--pass-authorization-header`" + ` — prefer ` + "[`oidc`](auth.md#oidc)" + `, with
` + "`token_header`" + ` if it arrives under a name of the proxy's own. A signature is
checked rather than a network path trusted, so neither condition above applies.`,
	Attrs: map[string]cfg.AttrMeta{
		"trusted_proxies": {
			Summary: "CIDRs or bare IPs whose identity headers are believed.",
			Doc: "Required. A request from any other address is rejected outright rather " +
				"than having its headers ignored, since a request that reached this " +
				"server without traversing the proxy is not one this mechanism can say " +
				"anything about.",
		},
		"user_header": {
			Summary: "Header carrying the authenticated user.",
			Doc:     "Becomes `ctx.auth.username` and `ctx.auth.subject`.",
			Default: defaultProxyUserHeader,
		},
		"email_header": {
			Summary: "Header carrying the user's email address.",
			Doc:     "Becomes `ctx.auth.claims.email`, when the proxy sends it.",
			Default: defaultProxyEmailHeader,
		},
		"groups_header": {
			Summary: "Header carrying the user's groups, comma-separated.",
			Doc:     "Becomes `ctx.auth.claims.groups`, a list, when the proxy sends it.",
			Default: defaultProxyGroupsHeader,
		},
	},
}

func processProxyAuth(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Authenticator, hcl.Diagnostics) {
	def := proxyDefinition{}
	if diags := cfg.DecodeBody(body, config.EvalCtx(), &def); diags.HasErrors() {
		return nil, diags
	}

	trusted, diags := cfg.ParseTrustedProxies(def.TrustedProxies, block.DefRange.Ptr(), "trusted_proxies")
	if diags.HasErrors() {
		return nil, diags
	}

	return &proxyAuthenticator{
		trusted:      trusted,
		userHeader:   headerOr(def.UserHeader, defaultProxyUserHeader),
		emailHeader:  headerOr(def.EmailHeader, defaultProxyEmailHeader),
		groupsHeader: headerOr(def.GroupsHeader, defaultProxyGroupsHeader),
	}, nil
}

func headerOr(configured, fallback string) string {
	if configured == "" {
		return fallback
	}
	return configured
}

type proxyAuthenticator struct {
	trusted      *cfg.TrustedProxies
	userHeader   string
	emailHeader  string
	groupsHeader string
}

func (p *proxyAuthenticator) Method() string { return "proxy" }

// Claims recognizes a request that came from a trusted proxy carrying a user.
//
// The trust check is part of claiming rather than only of judging: headers from
// an untrusted peer are not this mechanism's credential at all, they are
// something a client made up. Treating them as a claim would let anyone able to
// reach this server take a route's authentication away from the mechanism that
// would really have judged it.
func (p *proxyAuthenticator) Claims(r *http.Request) bool {
	return p.trusted.TrustsPeer(r) && r.Header.Get(p.userHeader) != ""
}

// Challenge is empty: there is nothing a client can do to authenticate itself
// here, because the identity is the proxy's to assert.
func (p *proxyAuthenticator) Challenge() string { return "" }

func (p *proxyAuthenticator) Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
	if !p.trusted.TrustsPeer(r) {
		return cty.NilVal, &AuthFailure{Status: http.StatusForbidden}, nil
	}

	user := r.Header.Get(p.userHeader)
	if user == "" {
		return cty.NilVal, &AuthFailure{Status: http.StatusUnauthorized}, nil
	}

	claims := map[string]cty.Value{}
	if email := r.Header.Get(p.emailHeader); email != "" {
		claims["email"] = cty.StringVal(email)
	}
	if groups := r.Header.Get(p.groupsHeader); groups != "" {
		var vals []cty.Value
		for _, g := range strings.Split(groups, ",") {
			if g = strings.TrimSpace(g); g != "" {
				vals = append(vals, cty.StringVal(g))
			}
		}
		if len(vals) > 0 {
			claims["groups"] = cty.ListVal(vals)
		}
	}

	claimsVal := cty.NullVal(cty.DynamicPseudoType)
	if len(claims) > 0 {
		claimsVal = cty.ObjectVal(claims)
	}

	return cty.ObjectVal(map[string]cty.Value{
		"username": cty.StringVal(user),
		"subject":  cty.StringVal(user),
		"claims":   claimsVal,
	}), nil, nil
}
