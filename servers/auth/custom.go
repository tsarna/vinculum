package auth

import (
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

// customDefinition is the body of an `auth "custom"` block.
type customDefinition struct {
	// Action authenticates the request and returns the identity.
	Action hcl.Expression `hcl:"action"`
	// Claims decides whether the request carries this mechanism's credential.
	Claims hcl.Expression `hcl:"claims,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

func init() {
	cfg.RegisterAuthType("custom", processCustomAuth, cfg.WithSchema(customAuthSchema))
}

var customAuthSchema = cfg.TypeSchema{
	Sample:  &customDefinition{},
	Summary: "Authentication written as an expression.",
	DocPage: "auth.md#custom",
	Doc: `For a scheme none of the other mechanisms covers — a session cookie, an API-key
table, a signed query parameter. The whole request is readable as
` + "`ctx.request`" + `, and the expression decides.`,
	Attrs: map[string]cfg.AttrMeta{
		"action": {
			Summary: "Expression that authenticates the request.",
			Doc: "Return an object to accept — it becomes `ctx.auth` — or `null` to reject " +
				"with 401. Returning an `http::redirect()` or `http::response()` sends " +
				"that instead, which is how a browser is sent to a login page. The " +
				"object may not set `method`, which names the mechanism that " +
				"authenticated the request and is filled in automatically.",
			Hint:    cfg.HintActionExpression,
			Context: "http-request",
		},
		"claims": {
			Summary: "Whether the request carries this mechanism's kind of credential.",
			Doc: "Only consulted when a route names several mechanisms, to decide which " +
				"one judges the request — the first to claim it decides, and its " +
				"rejection is final. Check for the credential's presence here, not its " +
				"validity: a request bearing a bad session cookie should be claimed and " +
				"rejected, not handed to the next mechanism. The other mechanisms can be " +
				"asked this by inspecting a header; an action cannot, since answering " +
				"would mean running it.",
			Hint:    cfg.HintExpression,
			Context: "http-request",
			Default: "claims every request",
		},
	},
}

func processCustomAuth(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Authenticator, hcl.Diagnostics) {
	def := customDefinition{}
	if diags := gohcl.DecodeBody(body, config.EvalCtx(), &def); diags.HasErrors() {
		return nil, diags
	}

	return &customAuthenticator{
		action:  def.Action,
		claims:  def.Claims,
		evalCtx: config.EvalCtx(),
	}, nil
}

type customAuthenticator struct {
	action hcl.Expression
	claims hcl.Expression
	// evalCtx is held because Claims is asked about a request without one —
	// deciding which mechanism owns a request is not itself an action with a
	// caller-supplied context.
	evalCtx *hcl.EvalContext
}

func (c *customAuthenticator) Method() string { return "custom" }

func (c *customAuthenticator) Claims(r *http.Request) bool {
	return evalClaims(r, c.claims, c.evalCtx)
}

// Challenge is empty: a custom mechanism's credential is whatever its expression
// reads, so there is no scheme to name in a WWW-Authenticate header.
func (c *customAuthenticator) Challenge() string { return "" }

func (c *customAuthenticator) Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
	result, failure, err := evalAuthAction(r, c.action, evalCtx)
	if err != nil || failure != nil {
		return cty.NilVal, failure, err
	}

	if result.IsNull() || !result.IsKnown() {
		return cty.NilVal, &AuthFailure{Status: http.StatusUnauthorized}, nil
	}

	if !result.Type().IsObjectType() && !result.Type().IsMapType() {
		return cty.NilVal, &AuthFailure{Status: http.StatusUnauthorized}, nil
	}

	if result.Type().IsObjectType() {
		vals := map[string]cty.Value{}
		for name := range result.Type().AttributeTypes() {
			vals[name] = result.GetAttr(name)
		}
		if err := rejectReservedAuthKeys(vals); err != nil {
			return cty.NilVal, nil, err
		}
	}

	return result, nil, nil
}
