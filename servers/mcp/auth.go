package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	serverauth "github.com/tsarna/vinculum/servers/auth"
	"github.com/tsarna/vinculum/types"
	"go.uber.org/zap"
)

// buildMCPAuthenticator creates an Authenticator for a standalone MCP server.
// Also returns cached OIDC metadata if the authenticator performed OIDC discovery.
func buildMCPAuthenticator(authCfg *cfg.AuthConfig, serverName string, evalCtx *hcl.EvalContext) (serverauth.Authenticator, *serverauth.OIDCMetadata, error) {
	authenticator, err := serverauth.BuildAuthenticator(authCfg, serverName, evalCtx)
	if err != nil {
		return nil, nil, err
	}

	// Extract cached OIDC metadata if available (only present when issuer discovery was used).
	type metaExposer interface {
		CachedMetadata() *serverauth.OIDCMetadata
	}
	var meta *serverauth.OIDCMetadata
	if me, ok := authenticator.(metaExposer); ok {
		meta = me.CachedMetadata()
	}

	return authenticator, meta, nil
}

// newMCPAuthMiddleware wraps next with authentication enforcement.
// On success the auth value is placed in the Go context so it flows through
// the MCP SDK into tool/resource/prompt handler contexts (and thus ctx.auth).
func newMCPAuthMiddleware(authenticator serverauth.Authenticator, evalCtx *hcl.EvalContext, logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authVal, failure, err := authenticator.Authenticate(r, evalCtx)
		if err != nil {
			logger.Error("MCP auth internal error", zap.Error(err))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if failure != nil {
			if failure.Response != nil {
				writeMCPAuthResponse(w, failure.Response)
				return
			}
			if failure.WWWAuthenticate != "" {
				w.Header().Set("WWW-Authenticate", failure.WWWAuthenticate)
			}
			http.Error(w, http.StatusText(failure.Status), failure.Status)
			return
		}

		// Store auth value in Go context — BuildEvalContext picks it up automatically.
		r = r.WithContext(hclutil.WithAuthValue(r.Context(), authVal))
		next.ServeHTTP(w, r)
	})
}

// writeMCPAuthResponse writes an HTTPResponseWrapper (e.g. a redirect from auth "custom").
func writeMCPAuthResponse(w http.ResponseWriter, resp *types.HTTPResponseWrapper) {
	for name, vals := range resp.Headers {
		for _, v := range vals {
			w.Header().Add(name, v)
		}
	}
	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	w.WriteHeader(resp.Status)
	if len(resp.Body) > 0 {
		w.Write(resp.Body) //nolint:errcheck
	}
}

// oidcMetadataHandler serves the OAuth2 authorization server metadata document
// at /.well-known/oauth-authorization-server (RFC 8414 / MCP spec requirement).
type oidcMetadataHandler struct {
	meta    *serverauth.OIDCMetadata
	payload []byte // lazily marshalled
}

func (h *oidcMetadataHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.payload == nil {
		data, err := json.Marshal(h.meta)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		h.payload = data
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(h.payload) //nolint:errcheck
}
