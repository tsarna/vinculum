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

// evalAuthAction evaluates an authentication expression with the request in
// scope, and handles the two outcomes every such expression shares: an
// evaluation error, and a response the expression wants sent verbatim.
//
// A non-nil failure or error means the caller is done; otherwise the returned
// value is the expression's own, for the caller to interpret.
func evalAuthAction(r *http.Request, action hcl.Expression, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
	actionEvalCtx, err := hclutil.NewEvalContext(r.Context()).
		WithAttribute("request", types.BuildHTTPRequestObject(r, nil)).
		BuildEvalContext(evalCtx)
	if err != nil {
		return cty.NilVal, nil, fmt.Errorf("building auth action eval context: %w", err)
	}

	result, diags := action.Value(actionEvalCtx)
	if diags.HasErrors() {
		// Deliberately a plain 401: the diagnostic describes the config, and
		// telling a caller why authentication failed to evaluate tells them
		// about the inside of the server.
		return cty.NilVal, &AuthFailure{Status: http.StatusUnauthorized}, nil
	}

	// An http::redirect() or http::response() is sent as written — this is what
	// lets a custom mechanism send a browser to a login page.
	if resp, ok := types.GetHTTPResponseFromValue(result); ok {
		return cty.NilVal, &AuthFailure{Status: resp.Status, Response: resp}, nil
	}

	return result, nil, nil
}

// evalClaims evaluates a `claims` expression, which decides whether a request
// carries this mechanism's kind of credential.
//
// A missing expression claims everything, which is what a route naming one
// mechanism wants. An expression that fails to evaluate claims nothing: the
// alternative is for a broken predicate to capture every request and reject it,
// which would take down a route rather than fall through to the mechanisms
// after it.
func evalClaims(r *http.Request, claims hcl.Expression, evalCtx *hcl.EvalContext) bool {
	if !cfg.IsExpressionProvided(claims) {
		return true
	}

	claimsEvalCtx, err := hclutil.NewEvalContext(r.Context()).
		WithAttribute("request", types.BuildHTTPRequestObject(r, nil)).
		BuildEvalContext(evalCtx)
	if err != nil {
		return false
	}

	result, diags := claims.Value(claimsEvalCtx)
	if diags.HasErrors() || result.IsNull() || !result.IsKnown() || result.Type() != cty.Bool {
		return false
	}
	return result.True()
}

// rejectReservedAuthKeys fails an identity object that sets a key the runtime
// owns. `method` names the mechanism that authenticated the request, so a value
// supplied here would either be overwritten silently or would lie about which
// mechanism won.
func rejectReservedAuthKeys(vals map[string]cty.Value) error {
	if _, taken := vals[cfg.AuthMethodField]; taken {
		return fmt.Errorf("an auth action may not return %q: it names the auth mechanism that "+
			"authenticated the request, and is filled in automatically", cfg.AuthMethodField)
	}
	return nil
}

// withMethod stamps the mechanism onto a successful identity, so an expression
// can tell which one authenticated the request when a route accepts several.
func withMethod(val cty.Value, method string) cty.Value {
	if val == cty.NilVal || val.IsNull() || !val.IsKnown() {
		return val
	}
	if !val.Type().IsObjectType() {
		return val
	}

	vals := map[string]cty.Value{}
	for name := range val.Type().AttributeTypes() {
		vals[name] = val.GetAttr(name)
	}
	vals[cfg.AuthMethodField] = cty.StringVal(method)
	return cty.ObjectVal(vals)
}

// authDiag builds a config-time diagnostic pointing at an auth block.
func authDiag(block *hcl.Block, summary, detail string) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  block.DefRange.Ptr(),
	}}
}
