package config

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"go.uber.org/zap"
)

// AuthHost is what a server tells an auth block about itself when it references
// it, so the block can resolve URLs that point back at the server and mount
// endpoints of its own on it.
//
// An auth block is processed before any server and does not know which server
// will reference it, so this is the only point at which the two halves of an
// externally visible URL are both in hand.
type AuthHost struct {
	// ServerName is the referencing server's block name.
	ServerName string

	// ExternalURL is the server's external_url: the base URL clients reach it
	// at from outside. It is configuration rather than something to derive,
	// because a proxy that terminates TLS or mounts the server under a path
	// prefix leaves the real scheme, host, and prefix invisible from inside.
	//
	// Nil when the server did not set one, which is the common case — only a
	// feature that must hand a client an absolute URL back to this server needs
	// it, and such a feature reports its own error when it is missing.
	ExternalURL *url.URL

	// DefRange is the referencing server's definition range, for diagnostics
	// about the pairing rather than about either block on its own.
	DefRange hcl.Range

	// ProtectedRoutes are the patterns on this server whose effective policy
	// includes the block being asked, in the order they were wired. An auth block
	// that hands clients a URL needs them: what it publishes has to agree with
	// what it actually guards.
	ProtectedRoutes []string

	// UserLogger reports a configuration worth a second look but not worth
	// refusing to start — the same channel reportAnonymous uses, and for the same
	// reason: the cause is something the author wrote.
	UserLogger *zap.Logger
}

// ResolveExternal resolves ref against the host's external_url.
//
// ref is either an absolute URL, returned as-is with external_url not consulted,
// or a path naming a route on this server, which requires one. attr names the
// attribute ref came from and subject locates it, so a diagnostic points at what
// the author wrote rather than at the server that happened to reference it.
//
// A path is **joined** onto external_url's own path rather than resolved against
// it as a URL reference. The two differ exactly when the proxy mounts the server
// under a prefix: given external_url "https://h/vinculum", reference resolution
// would turn "/mcp" into "https://h/mcp" and drop the prefix, where the author
// writing a path alongside handle "/mcp" means the route, and so means
// "https://h/vinculum/mcp".
func (h AuthHost) ResolveExternal(ref, attr string, subject hcl.Range) (*url.URL, hcl.Diagnostics) {
	bad := func(summary, detail string) hcl.Diagnostics {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  summary,
			Detail:   detail,
			Subject:  &subject,
		}}
	}

	u, err := url.Parse(ref)
	if err != nil {
		return nil, bad("Invalid "+attr, err.Error())
	}

	if u.IsAbs() {
		return u, nil
	}

	if u.Opaque != "" || u.Host != "" {
		return nil, bad("Invalid "+attr,
			fmt.Sprintf("%q is neither an absolute URL nor a path. Write %s as either "+
				"\"https://host/path\" or \"/path\".", ref, attr))
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, bad("Invalid "+attr,
			"A "+attr+" identifies a resource and may not carry a query string or fragment.")
	}

	if h.ExternalURL == nil {
		return nil, bad("Relative "+attr+" needs external_url",
			fmt.Sprintf("%q is a path, so it is resolved against the external base URL of the "+
				"server that references this block, but server \"http\" %q does not set "+
				"external_url. Either set it there, or write %s as an absolute URL.",
				ref, h.ServerName, attr))
	}

	resolved := *h.ExternalURL
	resolved.Path = path.Join(resolved.Path, u.Path)
	// path.Join cleans away a trailing slash. Put it back: the resolved value has
	// to string-match the URL a client was configured with, so ".../mcp/" and
	// ".../mcp" are not interchangeable.
	if strings.HasSuffix(u.Path, "/") && !strings.HasSuffix(resolved.Path, "/") {
		resolved.Path += "/"
	}
	return &resolved, nil
}

// ContributedRoute is an endpoint an auth block asks its host server to mount.
type ContributedRoute struct {
	// Pattern is a ServeMux pattern, rooted at the host server's mux.
	Pattern string

	// Handler serves it.
	Handler http.Handler

	// Subject anchors a diagnostic if the pattern is malformed or collides with
	// another route. It should point at the auth block, since that is what the
	// author would have to change.
	Subject hcl.Range
}

// RouteContributor is implemented by an Authenticator that needs endpoints of
// its own on every server that references it — the RFC 9728 metadata document
// today, and the redirect and logout endpoints an interactive flow would need.
//
// Contributed routes are mounted **outside** the authentication middleware, and
// that is a correctness requirement rather than a convenience: the document that
// tells a client how to authenticate cannot itself require authentication, or no
// client can ever reach it. Vinculum gets this for free because auth wraps
// individual handlers rather than the mux; anyone tempted to "simplify" that into
// mux-level middleware would lock every client out of discovery.
//
// ContributeRoutes is called once per referencing server, at config time, after
// that server has wired its own routes. An implementation whose identity can only
// belong to one server reports the conflict from here, on the second call.
//
// Nothing calls it at all when no server references the block, which is why
// ReportIfUnbound exists: a block configured to publish something, that nothing
// ever asked to publish, is inert in a way no request will ever reveal. Only the
// pass over every processed block can see that, since a server that would have
// asked might have been processed at any point.
type RouteContributor interface {
	ContributeRoutes(host AuthHost) ([]ContributedRoute, hcl.Diagnostics)

	// ReportIfUnbound warns if this block was configured to contribute routes
	// and ContributeRoutes was never called. Silent otherwise, including when
	// the block had nothing to contribute in the first place.
	ReportIfUnbound(logger *zap.Logger)
}

// AuthRouteSet collects the distinct authenticators a server has referenced, so
// that each contributes its routes once however many routes named it.
//
// Deduplication is by authenticator identity, which is deduplication by auth
// block: a named block holds exactly one built authenticator, so two routes
// naming auth.corp hold the same pointer. First-seen order is preserved to keep
// diagnostics stable across runs.
type AuthRouteSet struct {
	order     []Authenticator
	protected map[Authenticator][]string
}

// Add notes every authenticator in the policy that protects route. A nil policy
// is ignored; an authenticator seen again gains the route without being asked to
// contribute twice.
func (s *AuthRouteSet) Add(policy *AuthPolicy, route string) {
	if policy == nil {
		return
	}
	for _, a := range policy.Authenticators {
		if s.protected == nil {
			s.protected = map[Authenticator][]string{}
		}
		if _, seen := s.protected[a]; !seen {
			s.order = append(s.order, a)
		}
		s.protected[a] = append(s.protected[a], route)
	}
}

// Contribute asks each collected authenticator for its routes, in first-seen
// order, telling each which of the server's routes it protects.
func (s *AuthRouteSet) Contribute(host AuthHost) ([]ContributedRoute, hcl.Diagnostics) {
	var (
		routes []ContributedRoute
		diags  hcl.Diagnostics
	)
	for _, a := range s.order {
		contributor, ok := a.(RouteContributor)
		if !ok {
			continue
		}
		scoped := host
		scoped.ProtectedRoutes = s.protected[a]
		contributed, moreDiags := contributor.ContributeRoutes(scoped)
		diags = diags.Extend(moreDiags)
		routes = append(routes, contributed...)
	}
	return routes, diags
}
