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

type basicAuthenticator struct {
	realm       string
	credentials hcl.Expression // map(string) username→password; nil if action is used
	action      hcl.Expression // per-request expression; nil if credentials is used
	evalCtx     *hcl.EvalContext
}

func newBasicAuthenticator(ac *cfg.AuthConfig, serverName string, evalCtx *hcl.EvalContext) (Authenticator, error) {
	realm := ac.Realm
	if realm == "" {
		realm = serverName
	}
	return &basicAuthenticator{
		realm:       realm,
		credentials: ac.Credentials,
		action:      ac.Action,
		evalCtx:     evalCtx,
	}, nil
}

func (b *basicAuthenticator) Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
	noCredentials := &AuthFailure{
		Status:          http.StatusUnauthorized,
		WWWAuthenticate: fmt.Sprintf("Basic realm=%q", b.realm),
	}
	wrongCredentials := &AuthFailure{
		Status:          http.StatusUnauthorized,
		WWWAuthenticate: fmt.Sprintf("Basic realm=%q", b.realm),
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		return cty.NilVal, noCredentials, nil
	}

	if cfg.IsExpressionProvided(b.credentials) {
		// Static credentials map: evaluate and check.
		credsVal, diags := b.credentials.Value(evalCtx)
		if diags.HasErrors() {
			return cty.NilVal, nil, fmt.Errorf("evaluating auth credentials: %w", diags)
		}
		if !credsVal.Type().IsObjectType() && !credsVal.Type().IsMapType() {
			return cty.NilVal, nil, fmt.Errorf("auth credentials must be an object or map, got %s", credsVal.Type().FriendlyName())
		}
		if !credsVal.IsKnown() || credsVal.IsNull() {
			return cty.NilVal, wrongCredentials, nil
		}

		var expectedPassword cty.Value
		if credsVal.Type().IsObjectType() && credsVal.Type().HasAttribute(username) {
			expectedPassword = credsVal.GetAttr(username)
		} else if credsVal.Type().IsMapType() {
			idx := cty.StringVal(username)
			if credsVal.HasIndex(idx).True() {
				expectedPassword = credsVal.Index(idx)
			}
		}

		if !expectedPassword.IsKnown() || expectedPassword.IsNull() || expectedPassword.Type() != cty.String {
			return cty.NilVal, wrongCredentials, nil
		}
		if expectedPassword.AsString() != password {
			return cty.NilVal, wrongCredentials, nil
		}

		authVal := cty.ObjectVal(map[string]cty.Value{
			"username": cty.StringVal(username),
			"subject":  cty.NullVal(cty.DynamicPseudoType),
			"claims":   cty.NullVal(cty.DynamicPseudoType),
		})
		return authVal, nil, nil
	}

	// Action-based basic auth: evaluate with ctx.request in scope.
	actionEvalCtx, err := hclutil.NewEvalContext(r.Context()).
		WithAttribute("request", types.BuildHTTPRequestObject(r, nil)).
		BuildEvalContext(evalCtx)
	if err != nil {
		return cty.NilVal, nil, fmt.Errorf("building auth action eval context: %w", err)
	}

	result, diags := b.action.Value(actionEvalCtx)
	if diags.HasErrors() {
		return cty.NilVal, wrongCredentials, nil
	}

	// http_redirect or http_response value → send directly.
	if resp, ok := types.GetHTTPResponseFromValue(result); ok {
		return cty.NilVal, &AuthFailure{
			Status:   resp.Status,
			Response: resp,
		}, nil
	}

	if result.IsNull() || !result.IsKnown() {
		return cty.NilVal, wrongCredentials, nil
	}
	if !result.Type().IsObjectType() {
		return cty.NilVal, wrongCredentials, nil
	}

	// Merge username into the returned object.
	attrs := result.Type().AttributeTypes()
	vals := map[string]cty.Value{}
	for name := range attrs {
		vals[name] = result.GetAttr(name)
	}
	vals["username"] = cty.StringVal(username)
	if _, has := vals["subject"]; !has {
		vals["subject"] = cty.NullVal(cty.DynamicPseudoType)
	}
	if _, has := vals["claims"]; !has {
		vals["claims"] = cty.NullVal(cty.DynamicPseudoType)
	}
	return cty.ObjectVal(vals), nil, nil
}
