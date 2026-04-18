package auth

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

type customAuthenticator struct {
	action hcl.Expression
}

func newCustomAuthenticator(ac *cfg.AuthConfig) *customAuthenticator {
	return &customAuthenticator{action: ac.Action}
}

func (c *customAuthenticator) Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
	actionEvalCtx, err := hclutil.NewEvalContext(r.Context()).
		WithAttribute("request", types.BuildHTTPRequestObject(r, nil)).
		BuildEvalContext(evalCtx)
	if err != nil {
		return cty.NilVal, nil, fmt.Errorf("building custom auth eval context: %w", err)
	}

	result, diags := c.action.Value(actionEvalCtx)
	if diags.HasErrors() {
		// Expression error → 401 (do not disclose error details to client)
		return cty.NilVal, &AuthFailure{Status: http.StatusUnauthorized}, nil
	}

	// http_redirect or http_response value → send directly.
	if resp, ok := types.GetHTTPResponseFromValue(result); ok {
		return cty.NilVal, &AuthFailure{
			Status:   resp.Status,
			Response: resp,
		}, nil
	}

	// null → 401 Unauthorized.
	if result.IsNull() || !result.IsKnown() {
		return cty.NilVal, &AuthFailure{Status: http.StatusUnauthorized}, nil
	}

	// Object → authentication succeeded; the object becomes ctx.auth.
	if result.Type().IsObjectType() || result.Type().IsMapType() {
		return result, nil, nil
	}

	return cty.NilVal, &AuthFailure{Status: http.StatusUnauthorized}, nil
}
