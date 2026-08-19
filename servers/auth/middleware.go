// Package auth provides authentication for vinculum servers. It implements the
// mechanisms of the top-level `auth` block — basic, oidc, introspection,
// custom, and proxy — each registering itself with the config package from an
// init().
package auth

import (
	"net/http"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/tsarna/vinculum/types"
	"go.uber.org/zap"
)

// AuthFailure and Authenticator are declared in the config package, which owns
// the auth block and holds the one authenticator built for each name. Aliased
// here so the mechanisms in this package read as though they were local.
type (
	AuthFailure   = cfg.AuthFailure
	Authenticator = cfg.Authenticator
)

// NewAuthMiddleware wraps next with the given policy.
//
// A request is judged by the first mechanism that *claims* it — that recognizes
// the kind of credential it carries — and that mechanism's rejection is final.
// Falling through to the next on a wrong password would let a caller keep
// guessing against every mechanism a route accepts, and would make which one
// rejected them unknowable.
//
// A request no mechanism claims is anonymous, which the policy either permits or
// answers with a challenge from each mechanism that has one.
func NewAuthMiddleware(policy *cfg.AuthPolicy, evalCtx *hcl.EvalContext, logger *zap.Logger, next http.Handler) http.Handler {
	if policy == nil || (!policy.Enforced() && policy.AllowAnonymous) {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authenticator := claimant(policy, r)
		if authenticator == nil {
			if policy.AllowAnonymous {
				next.ServeHTTP(w, r)
				return
			}
			writeUnclaimed(w, policy)
			return
		}

		authVal, failure, err := authenticator.Authenticate(r, evalCtx)
		if err != nil {
			logger.Error("Auth internal error",
				zap.String("method", authenticator.Method()),
				zap.Error(err))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if failure != nil {
			writeFailure(w, failure)
			return
		}

		// Store auth value in request context so BuildEvalContext picks it up.
		r = r.WithContext(hclutil.WithAuthValue(r.Context(), withMethod(authVal, authenticator.Method())))
		next.ServeHTTP(w, r)
	})
}

// claimant returns the first mechanism that recognizes the request's credential.
func claimant(policy *cfg.AuthPolicy, r *http.Request) Authenticator {
	for _, a := range policy.Authenticators {
		if a.Claims(r) {
			return a
		}
	}
	return nil
}

// writeUnclaimed answers a request that carried no credential any mechanism
// recognized, offering each challenge the policy can issue.
//
// RFC 7235 allows more than one WWW-Authenticate header, which is what lets a
// route accepting both a bearer token and a browser login tell a client about
// both rather than picking one arbitrarily.
func writeUnclaimed(w http.ResponseWriter, policy *cfg.AuthPolicy) {
	for _, a := range policy.Authenticators {
		if challenge := a.Challenge(); challenge != "" {
			w.Header().Add("WWW-Authenticate", challenge)
		}
	}
	http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
}

func writeFailure(w http.ResponseWriter, failure *AuthFailure) {
	if failure.Response != nil {
		writeResponse(w, failure.Response)
		return
	}
	if failure.WWWAuthenticate != "" {
		w.Header().Set("WWW-Authenticate", failure.WWWAuthenticate)
	}
	http.Error(w, http.StatusText(failure.Status), failure.Status)
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
