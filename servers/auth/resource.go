package auth

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// wellKnownPRM is the RFC 9728 well-known path. A resource's metadata lives at
// this path with the resource's own path appended — "path insertion" — so
// https://h/mcp publishes at https://h/.well-known/oauth-protected-resource/mcp.
const wellKnownPRM = "/.well-known/oauth-protected-resource"

// protectedResource is the RFC 9728 identity of an auth block: the identifier
// clients present tokens for, and the metadata document telling them which
// authorization server issues those tokens.
//
// It exists so a client that knows only an endpoint URL can bootstrap. A client
// configured with credentials already needs none of this, which is why `resource`
// is optional and most blocks will not set it.
type protectedResource struct {
	// declared is the `resource` attribute as written: an absolute URL, or a
	// path resolved against the referencing server's external_url.
	declared string
	subject  hcl.Range

	// authServers is what goes in authorization_servers — the issuer identifier,
	// which is what a client follows to find the token endpoint.
	authServers []string

	// Everything below is filled in by bind during config processing, and read
	// only once requests are being served. No lock: every write happens before
	// the goroutine serving any request is started, so the ordering is
	// established by the server's own launch rather than by synchronization here.
	identifier  string
	metadataURL string
	boundTo     string
}

func newProtectedResource(declared string, subject hcl.Range, authServers []string) *protectedResource {
	return &protectedResource{
		declared:    declared,
		subject:     subject,
		authServers: authServers,
	}
}

// bind resolves the declared resource against a referencing server and returns
// the route that serves its metadata document.
//
// Called once per referencing server. A second call is what catches an auth block
// whose identity is ambiguous: a relative resource resolved against two different
// external URLs would be two resources wearing one name.
func (p *protectedResource) bind(host cfg.AuthHost) ([]cfg.ContributedRoute, hcl.Diagnostics) {
	identifier, diags := host.ResolveExternal(p.declared, "resource", p.subject)
	if diags.HasErrors() {
		return nil, diags
	}

	// The client refuses a metadata URL that is neither HTTPS nor loopback, and
	// reports it as a failure of the resource rather than of the deployment, so
	// catch it here where the fix is visible.
	if err := requireSecureURL(identifier); err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Insecure resource URL",
			Detail: fmt.Sprintf("The resource resolves to %s, and %s. A client performing "+
				"RFC 9728 discovery rejects it before it ever reaches the identity provider.",
				identifier, err),
			Subject: &p.subject,
		}}
	}

	if p.boundTo != "" {
		if p.identifier != identifier.String() {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Auth block has two resource identities",
				Detail: fmt.Sprintf("This block resolves to %s under server %q and to %s under "+
					"server %q. A protected resource is one resource with one identifier, so a "+
					"relative resource can belong to only one server. Either give each server its "+
					"own auth block, or write resource as an absolute URL.",
					p.identifier, p.boundTo, identifier, host.ServerName),
				Subject: &p.subject,
			}}
		}
		// Same identifier from a second server: harmless, and worth serving there
		// too, since either host may be the one a client was pointed at.
	}

	p.identifier = identifier.String()
	p.boundTo = host.ServerName
	p.reportExtraRoutes(host, identifier)

	metadataPath := wellKnownPRM
	if trimmed := strings.Trim(identifier.Path, "/"); trimmed != "" {
		metadataPath += "/" + trimmed
	}

	metadataURL := *identifier
	metadataURL.Path = metadataPath
	p.metadataURL = metadataURL.String()

	return []cfg.ContributedRoute{{
		Pattern: metadataPath,
		Handler: p.handler(),
		Subject: p.subject,
	}}, nil
}

// reportIfUnbound warns when a declared resource was never resolved, which means
// no `server "http"` referenced this block.
//
// The result is a block that looks configured for OAuth discovery and does none
// of it: no document is served, and no 401 points at one. Nothing at request time
// distinguishes that from a block that never asked for discovery, so startup is
// the only place it can be said.
//
// A warning rather than an error, because declaring an auth block and not
// referencing it is legal on its own, and because a block referenced only by
// `server "metrics"` is a working authenticator — it is the `resource` on it that
// has no effect, not the block.
func (p *protectedResource) reportIfUnbound(logger *zap.Logger) {
	if logger == nil || p.boundTo != "" {
		return
	}

	logger.Warn("Auth block declares a resource that nothing publishes",
		zap.String("resource", p.declared),
		zap.String("at", p.subject.String()),
		zap.String("effect", "no protected resource metadata document is served and no 401 "+
			"points at one, because no server \"http\" route references this block"),
	)
}

// reportExtraRoutes warns when this block guards routes the published identifier
// does not name.
//
// A client checks the document's `resource` against the URL it dialled and
// refuses a mismatch, so discovery works from the route the identifier names and
// fails from any other — even though a client that already holds a valid token is
// served normally by all of them. That split is confusing enough to be worth a
// line, and quiet enough to be missed without one.
//
// Not an error: RFC 9728 permits one identifier to span a multi-path API, and a
// deployment whose clients are always pointed at the one endpoint is correct as
// written.
func (p *protectedResource) reportExtraRoutes(host cfg.AuthHost, identifier *url.URL) {
	if host.UserLogger == nil {
		return
	}

	var extra []string
	for _, route := range host.ProtectedRoutes {
		if !routeCovers(route, identifier.Path) {
			extra = append(extra, route)
		}
	}
	if len(extra) == 0 {
		return
	}

	host.UserLogger.Warn("Auth block publishes a resource that does not name every route it protects",
		zap.String("resource", p.identifier),
		zap.Strings("routes", extra),
		zap.String("server", host.ServerName),
		zap.String("at", p.subject.String()),
		zap.String("effect", "OAuth discovery from these routes points clients at a document "+
			"whose resource is a different URL, which a client rejects"),
	)
}

// routeCovers reports whether a ServeMux pattern matches the resource path.
//
// The comparison is deliberately shallow — strip any method and host prefix,
// then compare paths, treating a subtree pattern as covering what is under it.
// A wildcard segment is not resolved: `/items/{id}` names a family of URLs and no
// single identifier can name it, which is the case the warning exists for.
func routeCovers(pattern, resourcePath string) bool {
	path := pattern
	if i := strings.LastIndex(path, " "); i >= 0 {
		path = path[i+1:] // drop "METHOD " and any host
	}
	if i := strings.Index(path, "/"); i > 0 {
		path = path[i:] // drop a bare "host/path" prefix
	}

	if resourcePath == "" {
		resourcePath = "/"
	}
	switch {
	case path == resourcePath:
		return true
	case strings.HasSuffix(path, "/{$}"):
		return strings.TrimSuffix(path, "{$}") == resourcePath
	case strings.HasSuffix(path, "/"):
		return strings.HasPrefix(resourcePath, path) || resourcePath+"/" == path
	default:
		return false
	}
}

// resourceMetadataChallenge returns the WWW-Authenticate parameter pointing a
// client at the metadata document, or "" when there is none to point at.
func (p *protectedResource) resourceMetadataChallenge() string {
	if p == nil || p.metadataURL == "" {
		return ""
	}
	return fmt.Sprintf("resource_metadata=%q", p.metadataURL)
}

// protectedResourceMetadata is the RFC 9728 document. Only the fields vinculum
// can populate truthfully are here: inventing scopes_supported or a
// resource_name would be describing a policy nothing enforces.
type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported,omitempty"`
}

// handler serves the metadata document.
//
// The document is public by definition — it is what an unauthenticated client
// reads to discover how to authenticate — so it is served to any origin. The
// server mounts it outside the auth middleware for the same reason.
func (p *protectedResource) handler() http.Handler {
	body, err := json.Marshal(protectedResourceMetadata{
		Resource:             p.identifier,
		AuthorizationServers: p.authServers,
		// The token arrives in a header. RFC 9728's other two values, "body" and
		// "query", are forms vinculum does not accept.
		BearerMethodsSupported: []string{"header"},
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(body) //nolint:errcheck
	})
}

// requireSecureURL reports why u is unusable for OAuth discovery, or nil if it
// is fine. HTTPS is required except against loopback, which is what makes local
// development work without certificates.
func requireSecureURL(u *url.URL) error {
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" {
		return fmt.Errorf("scheme %q is neither https nor http", u.Scheme)
	}

	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("plain http is only accepted for loopback, not for host %q", host)
}
