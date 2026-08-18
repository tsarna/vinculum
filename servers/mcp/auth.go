package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	serverauth "github.com/tsarna/vinculum/servers/auth"
	"github.com/tsarna/vinculum/types"
	"go.uber.org/zap"
)

// buildMCPAuthenticator creates an Authenticator for a standalone MCP server.
//
// The second return value resolves the OIDC discovery document, and is nil
// unless this authenticator discovers one. It is a function rather than the
// document itself because discovery now happens on first use: at the moment the
// server is being built, the issuer may not have been contacted yet — or may
// not be reachable at all.
func buildMCPAuthenticator(authCfg *cfg.AuthConfig, serverName string, config *cfg.Config) (serverauth.Authenticator, func(context.Context) (*serverauth.OIDCMetadata, error), error) {
	authenticator, err := serverauth.BuildAuthenticator(authCfg, serverName, config)
	if err != nil {
		return nil, nil, err
	}

	type metaExposer interface {
		DiscoveryMetadata() func(context.Context) (*serverauth.OIDCMetadata, error)
	}
	var resolveMeta func(context.Context) (*serverauth.OIDCMetadata, error)
	if me, ok := authenticator.(metaExposer); ok {
		resolveMeta = me.DiscoveryMetadata()
	}

	return authenticator, resolveMeta, nil
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
//
// The document is fetched from the issuer on demand and cached once it arrives.
// While the issuer is unreachable the endpoint answers 503 rather than an empty
// document, so a client can tell "not yet" from "no such metadata".
type oidcMetadataHandler struct {
	resolve func(context.Context) (*serverauth.OIDCMetadata, error)

	mu      sync.Mutex
	payload []byte
}

func (h *oidcMetadataHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	payload := h.payload
	h.mu.Unlock()

	if payload == nil {
		meta, err := h.resolve(r.Context())
		if err != nil {
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		payload, err = json.Marshal(meta)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		h.mu.Lock()
		h.payload = payload
		h.mu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(payload) //nolint:errcheck
}
