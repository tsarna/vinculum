package config

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

// AuthDefinition is the envelope every auth block shares. The first label
// selects the mechanism, the second names it, and the rest of the body is
// decoded by that mechanism's own processor:
//
//	auth "basic"  "ops"  { realm = "API", credentials = { alice = env.PASSWORD } }
//	auth "oidc"   "corp" { issuer = "https://accounts.example.com" }
//	auth "custom" "sess" { action = lookup_session(ctx.request.cookie("session")) }
//
// A server or route then references one by name:
//
//	server "http" "main" {
//	  auth = auth.corp
//	  handle "/public" { auth = auth.anonymous }
//	}
type AuthDefinition struct {
	Type string `hcl:"type,label"`
	Name string `hcl:"name,label"`

	Disabled      bool      `hcl:"disabled,optional"`
	DefRange      hcl.Range `hcl:",def_range"`
	RemainingBody hcl.Body  `hcl:",remain"`
}

// AuthFailure describes a rejected request.
type AuthFailure struct {
	// Status is the HTTP status code to return (401, 403, or 503).
	Status int
	// WWWAuthenticate is the value for the WWW-Authenticate response header.
	// Empty string means no header.
	WWWAuthenticate string
	// Response, if non-nil, is written directly as the HTTP response instead of
	// the default status + header. Used where a rejection needs headers or a
	// body the two fields above cannot express — a redirect from auth "custom",
	// or the Retry-After on a 503 from an authenticator whose identity provider
	// is unreachable.
	Response *types.HTTPResponseWrapper
}

// Authenticator validates an incoming HTTP request and returns the value to
// expose as ctx.auth on success, or an AuthFailure on rejection.
//
// Implementations live in servers/auth. The interface is declared here because
// a named auth block holds the one instance built for it, and config cannot
// import the package that builds them.
type Authenticator interface {
	// Method names the mechanism, and is what a successful request sees as
	// ctx.auth.method.
	Method() string

	// Claims reports whether the request presents a credential of this
	// mechanism's kind — a Bearer header, Basic credentials, the trusted
	// proxy's headers. It decides *which* authenticator judges a request when a
	// route names several, so it must not depend on whether that credential is
	// valid: a wrong password is claimed and then rejected, never passed on.
	Claims(r *http.Request) bool

	// Challenge is the WWW-Authenticate value to offer a request that arrived
	// with no credential at all. Empty for mechanisms with no challenge to
	// issue, such as a proxy-header or cookie-based one.
	Challenge() string

	// Authenticate judges a request this authenticator has claimed.
	Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error)
}

// AuthMethodField is the ctx.auth key naming the mechanism that authenticated
// the request. It is reserved: an auth "custom" action returning an object that
// sets it is an error rather than a value silently overwritten.
const AuthMethodField = "method"

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// AuthProcessor decodes one auth block's body and builds the authenticator it
// describes. Returning a nil Authenticator with no diagnostics means the block
// produced nothing to enforce.
type AuthProcessor func(config *Config, block *hcl.Block, body hcl.Body) (Authenticator, hcl.Diagnostics)

var authRegistry = map[string]AuthProcessor{}

// RegisterAuthType registers a processor for a named auth mechanism.
// Sub-packages call this from their init() function, optionally passing
// WithSchema to describe the block for `vinculum schema`.
func RegisterAuthType(typeName string, p AuthProcessor, opts ...RegisterOption) {
	recordPlugin("auth." + typeName)
	authRegistry[typeName] = p
	registerTypeSchema("auth", typeName, opts)
}

// authTypeNames returns the registered mechanism names, sorted.
func authTypeNames() []string {
	names := make([]string, 0, len(authRegistry))
	for name := range authRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// References and sentinels
// ---------------------------------------------------------------------------

// AuthRef is what an `auth.<name>` reference evaluates to.
//
// The Authenticator is filled in when the block is processed, so a server that
// captures a reference need not be processed after the block it names — and a
// route's policy stays correct however the two are ordered.
type AuthRef struct {
	// Name is the block's name, or the sentinel's own name.
	Name string
	// Authenticator enforces this reference. Nil for both sentinels, and for a
	// block whose processor produced nothing.
	Authenticator Authenticator
	// kind separates the two sentinels from a declared mechanism.
	kind authRefKind
}

type authRefKind int

const (
	authRefReal authRefKind = iota
	authRefAnonymous
	authRefDisabled
)

// AuthAnonymousName and AuthDisabledName are reserved: a block may not take
// either.
//
// They are named to be hard to confuse, because confusing them is a fail-open
// bug. "Anonymous" is an identity state a route permits; "disabled" is a toggle
// on a mechanism. A pair like "none" and "disabled" would read as synonyms
// while meaning opposite things in a list — one admits a request, the other
// vanishes and leaves the rest of the list enforcing.
const (
	AuthAnonymousName = "anonymous"
	AuthDisabledName  = "disabled"
)

// AuthAnonymous is `auth.anonymous`, the assertion that a request no mechanism
// claimed may proceed. It is a policy an author writes, not a state a block
// falls into: a route naming it is deliberately open, and says so at the point
// of use. It also reads as a list element — `[auth.corp, auth.anonymous]` is
// "corp, or anonymous", where "none" would read as negating the whole list.
var AuthAnonymous = &AuthRef{Name: AuthAnonymousName, kind: authRefAnonymous}

// AuthDisabled is what the name of a switched-off block resolves to. It is
// filtered out of a list before anything is enforced, which is what keeps
// disabling one of several mechanisms from opening a route: `[auth.oidc,
// auth.admin]` with oidc off leaves `[auth.admin]`, not anonymous access.
//
// Deliberately not writable. Author-facing configuration has exactly one way to
// say "anonymous is acceptable", and that is AuthAnonymous.
var AuthDisabled = &AuthRef{Name: AuthDisabledName, kind: authRefDisabled}

// AuthCapsuleType is the cty capsule type behind an `auth.<name>` value.
var AuthCapsuleType = cty.CapsuleWithOps("auth", reflect.TypeOf(AuthRef{}), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("auth(%s)", val.(*AuthRef).Name)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "auth"
	},
})

// NewAuthCapsule wraps an AuthRef as a cty value.
func NewAuthCapsule(ref *AuthRef) cty.Value {
	return cty.CapsuleVal(AuthCapsuleType, ref)
}

// ---------------------------------------------------------------------------
// Policy resolution
// ---------------------------------------------------------------------------

// AuthPolicy is the decided authentication for one server or route.
type AuthPolicy struct {
	// Authenticators are tried in order; the first to claim a request judges it.
	Authenticators []Authenticator
	// AllowAnonymous permits a request no authenticator claimed. It is true when
	// the policy is empty, and when it names auth.anonymous.
	AllowAnonymous bool
	// Names are the references the policy was written from, in source order,
	// for diagnostics.
	Names []string
}

// Enforced reports whether the policy rejects anything at all. A policy that
// neither authenticates nor restricts is one an author may not have meant to
// write, which is what the anonymous-policy warning is about.
func (p *AuthPolicy) Enforced() bool {
	return p != nil && len(p.Authenticators) > 0
}

// ResolveAuth evaluates an `auth` attribute into the policy it describes.
//
// The attribute accepts a single reference or a list of them, the same value
// polymorphism `get()` and the extract* helpers use. Within a list:
//
//   - auth.disabled entries are dropped, since a switched-off mechanism should
//     leave the rest of the list enforcing rather than open the route;
//   - auth.anonymous asserts that a request no mechanism claimed may proceed,
//     and must be written last, because `[auth.anonymous, auth.oidc]` reads as
//     though anonymous wins when it does not;
//   - what remains is tried in order.
//
// An absent attribute returns nil, which means "inherit"; the caller decides
// what to fall back to.
func ResolveAuth(config *Config, expr hcl.Expression) (*AuthPolicy, hcl.Diagnostics) {
	if !IsExpressionProvided(expr) {
		return nil, nil
	}

	val, diags := expr.Value(config.evalCtx)
	if diags.HasErrors() {
		return nil, diags
	}

	exprRange := expr.Range()
	bad := func(summary, detail string) hcl.Diagnostics {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  summary,
			Detail:   detail,
			Subject:  &exprRange,
		}}
	}

	if val.IsNull() {
		return nil, bad("Invalid auth reference",
			"auth is null. Use auth.anonymous to allow unauthenticated requests.")
	}

	var refs []*AuthRef
	switch {
	case val.Type() == AuthCapsuleType:
		refs = append(refs, val.EncapsulatedValue().(*AuthRef))
	case val.Type().IsTupleType() || val.Type().IsListType() || val.Type().IsSetType():
		for it := val.ElementIterator(); it.Next(); {
			_, elem := it.Element()
			if elem.IsNull() || elem.Type() != AuthCapsuleType {
				return nil, bad("Invalid auth reference",
					"Every element of an auth list must be an auth block reference, such as auth.corp or auth.anonymous.")
			}
			refs = append(refs, elem.EncapsulatedValue().(*AuthRef))
		}
	default:
		return nil, bad("Invalid auth reference",
			fmt.Sprintf("auth must be an auth block reference or a list of them, got %s.",
				val.Type().FriendlyName()))
	}

	policy := &AuthPolicy{}
	for i, ref := range refs {
		policy.Names = append(policy.Names, ref.Name)
		switch ref.kind {
		case authRefDisabled:
			// Dropped: a mechanism that is switched off neither enforces nor
			// opens anything.
		case authRefAnonymous:
			if i != len(refs)-1 {
				return nil, bad("auth.anonymous must be last",
					"auth.anonymous allows a request that no other mechanism claimed, so it "+
						"applies however it is ordered. Writing it before another mechanism "+
						"reads as though anonymous access wins, which it does not.")
			}
			policy.AllowAnonymous = true
		default:
			if ref.Authenticator != nil {
				policy.Authenticators = append(policy.Authenticators, ref.Authenticator)
			}
		}
	}

	// Everything named was switched off, so nothing is left to enforce. That is
	// what disabling the only mechanism on a route has always meant.
	if len(policy.Authenticators) == 0 {
		policy.AllowAnonymous = true
	}

	return policy, nil
}
