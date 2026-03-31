// Package auth provides authentication middleware for vinculum servers.
// It implements the auth "basic", "oidc", "oauth2", "custom", and "none" modes
// defined in HTTP-AUTH-SPEC.md.
package auth

import (
	"net/http"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// AuthFailure describes a failed authentication attempt.
type AuthFailure struct {
	// Status is the HTTP status code to return (401 or 403).
	Status int
	// WWWAuthenticate is the value for the WWW-Authenticate response header.
	// Empty string means no header.
	WWWAuthenticate string
	// Response, if non-nil, is written directly as the HTTP response instead
	// of the default status + header (used by auth "custom" for redirects).
	Response *types.HTTPResponseWrapper
}

// Authenticator validates an incoming HTTP request and returns the value to
// expose as ctx.auth on success, or an AuthFailure on rejection.
type Authenticator interface {
	Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error)
}

// NewAuthMiddleware wraps next with authentication enforcement using auth.
// On success the auth value is stored in the request context (as ctx.auth).
// On failure an appropriate HTTP error is written and the request is aborted.
func NewAuthMiddleware(authenticator Authenticator, evalCtx *hcl.EvalContext, logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authVal, failure, err := authenticator.Authenticate(r, evalCtx)
		if err != nil {
			logger.Error("Auth internal error", zap.Error(err))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if failure != nil {
			if failure.Response != nil {
				writeResponse(w, failure.Response)
				return
			}
			if failure.WWWAuthenticate != "" {
				w.Header().Set("WWW-Authenticate", failure.WWWAuthenticate)
			}
			http.Error(w, http.StatusText(failure.Status), failure.Status)
			return
		}

		// Store auth value in request context so BuildEvalContext picks it up.
		r = r.WithContext(hclutil.WithAuthValue(r.Context(), authVal))
		next.ServeHTTP(w, r)
	})
}

// BuildAuthenticator constructs an Authenticator from the given AuthConfig.
// Returns nil, nil when ac is nil or ac.Mode == "none" (no authentication).
// The serverName is used as the default Basic auth realm when not specified.
// evalCtx is the global configuration eval context, used for evaluating
// static expressions (e.g. credentials maps) at construction time if needed.
func BuildAuthenticator(ac *cfg.AuthConfig, serverName string, evalCtx *hcl.EvalContext) (Authenticator, error) {
	if ac == nil || ac.Mode == "none" {
		return nil, nil
	}

	switch ac.Mode {
	case "basic":
		return newBasicAuthenticator(ac, serverName, evalCtx)
	case "oidc":
		return newOIDCAuthenticator(ac, evalCtx)
	case "oauth2":
		return newOAuth2Authenticator(ac, evalCtx)
	case "custom":
		return newCustomAuthenticator(ac), nil
	default:
		// ValidateAuthConfig should have caught this; defensive fallback.
		return nil, nil
	}
}

// writeResponse writes an HTTPResponseWrapper to w.
func writeResponse(w http.ResponseWriter, resp *types.HTTPResponseWrapper) {
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
