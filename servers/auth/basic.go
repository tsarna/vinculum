package auth

import (
	"crypto/subtle"
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

// basicDefinition is the body of an `auth "basic"` block.
type basicDefinition struct {
	// Realm is shown in the WWW-Authenticate header; defaults to the block name.
	Realm string `hcl:"realm,optional"`
	// Credentials is a map(string) of username → password.
	Credentials hcl.Expression `hcl:"credentials,optional"`
	// Action checks the credentials itself, for a store this cannot express.
	Action hcl.Expression `hcl:"action,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

func init() {
	cfg.RegisterAuthType("basic", processBasicAuth, cfg.WithSchema(basicAuthSchema))
}

var basicAuthSchema = cfg.TypeSchema{
	Sample:  &basicDefinition{},
	Summary: "HTTP Basic authentication.",
	DocPage: "auth.md#basic",
	Doc: `Checks a username and password from the ` + "`Authorization`" + ` header, either against
a map or with an expression.

Basic credentials travel base64-encoded, not encrypted, so this belongs behind
TLS.`,
	Attrs: map[string]cfg.AttrMeta{
		"realm": {
			Summary: "Realm shown in the `WWW-Authenticate` header.",
			Doc:     "Browsers show it in the password prompt, and use it to decide which saved credentials to offer.",
			Default: "the block's name",
		},
		"credentials": {
			Summary: "Map of username to password.",
			Doc: "Supply passwords from the environment rather than as literals. " +
				"Comparison is constant-time, so a wrong password does not leak its " +
				"correct prefix through timing.",
		},
		"action": {
			Summary: "Expression that checks the credentials itself.",
			Doc: "For credentials this block cannot express — a database, an API. " +
				"`ctx.request.user` and `ctx.request.password` carry what the client sent. " +
				"Return an object to accept (it becomes `ctx.auth`, with `username` filled " +
				"in), or a falsey value to reject.",
			Hint:    cfg.HintActionExpression,
			Context: "http-request",
		},
	},
	Constraints: []cfg.Constraint{
		cfg.MutuallyExclusive("credentials", "action"),
		cfg.AtLeastOneOf("credentials", "action").
			WithMessage("Basic auth needs either a map of credentials or an action to check them."),
	},
}

func processBasicAuth(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Authenticator, hcl.Diagnostics) {
	def := basicDefinition{}
	if diags := gohcl.DecodeBody(body, config.EvalCtx(), &def); diags.HasErrors() {
		return nil, diags
	}

	hasCredentials := cfg.IsExpressionProvided(def.Credentials)
	hasAction := cfg.IsExpressionProvided(def.Action)
	switch {
	case hasCredentials && hasAction:
		return nil, authDiag(block, "Conflicting auth attributes",
			`auth "basic" takes exactly one of "credentials" or "action", not both.`)
	case !hasCredentials && !hasAction:
		return nil, authDiag(block, "Missing auth attribute",
			`auth "basic" needs either "credentials" (a map of username to password) or "action" (an expression that checks them).`)
	}

	realm := def.Realm
	if realm == "" {
		realm = block.Labels[1]
	}

	return &basicAuthenticator{
		realm:       realm,
		credentials: def.Credentials,
		action:      def.Action,
	}, nil
}

type basicAuthenticator struct {
	realm       string
	credentials hcl.Expression // map(string) username→password; nil when action is used
	action      hcl.Expression // per-request expression; nil when credentials is used
}

func (b *basicAuthenticator) Method() string { return "basic" }

// Claims recognizes a request carrying Basic credentials. Whether they are
// correct is Authenticate's business — a wrong password must be rejected here
// rather than passed to the next mechanism on the route.
func (b *basicAuthenticator) Claims(r *http.Request) bool {
	_, _, ok := r.BasicAuth()
	return ok
}

func (b *basicAuthenticator) Challenge() string {
	return fmt.Sprintf("Basic realm=%q", b.realm)
}

func (b *basicAuthenticator) Authenticate(r *http.Request, evalCtx *hcl.EvalContext) (cty.Value, *AuthFailure, error) {
	rejected := &AuthFailure{
		Status:          http.StatusUnauthorized,
		WWWAuthenticate: b.Challenge(),
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		return cty.NilVal, rejected, nil
	}

	if cfg.IsExpressionProvided(b.credentials) {
		credsVal, diags := b.credentials.Value(evalCtx)
		if diags.HasErrors() {
			return cty.NilVal, nil, fmt.Errorf("evaluating auth credentials: %w", diags)
		}
		if !credsVal.Type().IsObjectType() && !credsVal.Type().IsMapType() {
			return cty.NilVal, nil, fmt.Errorf("auth credentials must be an object or map, got %s", credsVal.Type().FriendlyName())
		}
		if !credsVal.IsKnown() || credsVal.IsNull() {
			return cty.NilVal, rejected, nil
		}

		var expected cty.Value
		if credsVal.Type().IsObjectType() && credsVal.Type().HasAttribute(username) {
			expected = credsVal.GetAttr(username)
		} else if credsVal.Type().IsMapType() {
			idx := cty.StringVal(username)
			if credsVal.HasIndex(idx).True() {
				expected = credsVal.Index(idx)
			}
		}

		if expected == cty.NilVal || !expected.IsKnown() || expected.IsNull() || expected.Type() != cty.String {
			return cty.NilVal, rejected, nil
		}
		// Constant-time, so a wrong password does not leak its correct prefix
		// through how long the comparison took.
		if subtle.ConstantTimeCompare([]byte(expected.AsString()), []byte(password)) != 1 {
			return cty.NilVal, rejected, nil
		}

		return cty.ObjectVal(map[string]cty.Value{
			"username": cty.StringVal(username),
			"subject":  cty.StringVal(username),
			"claims":   cty.NullVal(cty.DynamicPseudoType),
		}), nil, nil
	}

	result, failure, err := evalAuthAction(r, b.action, evalCtx)
	if err != nil || failure != nil {
		return cty.NilVal, failure, err
	}
	if result.IsNull() || !result.IsKnown() || !result.Type().IsObjectType() {
		return cty.NilVal, rejected, nil
	}

	// The action decided who this is; the username came off the wire, so fill it
	// in rather than making every such action repeat it.
	vals := map[string]cty.Value{}
	for name := range result.Type().AttributeTypes() {
		vals[name] = result.GetAttr(name)
	}
	if err := rejectReservedAuthKeys(vals); err != nil {
		return cty.NilVal, nil, err
	}
	vals["username"] = cty.StringVal(username)
	if _, has := vals["subject"]; !has {
		vals["subject"] = cty.StringVal(username)
	}
	if _, has := vals["claims"]; !has {
		vals["claims"] = cty.NullVal(cty.DynamicPseudoType)
	}
	return cty.ObjectVal(vals), nil, nil
}
