package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"
)

// AuthBlockHandler implements BlockHandler for auth blocks.
//
// Several blocks may share a name so long as at most one of them is enabled.
// That makes the "declare both, let an environment variable pick" idiom a rule
// rather than something that happens to work:
//
//	auth "oidc"  "site" { disabled = env.DEV != "", issuer = "…" }
//	auth "basic" "site" { disabled = env.DEV == "", credentials = { … } }
//
// The declarations need not share a mechanism, which is the point.
type AuthBlockHandler struct {
	BlockHandlerBase

	// declarations records every auth block by name, in source order, during
	// preprocessing — before anything is decoded, so that ordering can be
	// arranged before any of them is built.
	declarations map[string][]*hcl.Block
}

func NewAuthBlockHandler() *AuthBlockHandler {
	return &AuthBlockHandler{declarations: map[string][]*hcl.Block{}}
}

// Schema describes the auth block for `vinculum schema`. A typed block has no
// body of its own; each mechanism contributes its own via WithSchema.
func (h *AuthBlockHandler) Schema() TypeSchema {
	return TypeSchema{
		Summary: "A named authentication mechanism.",
		Doc: `The first label selects the mechanism and the second names it, making it
available in expressions as ` + "`auth.<name>`" + `. A server or a route then references
one:

	auth "oidc" "corp" { issuer = "https://accounts.example.com" }

	server "http" "main" {
	  listen = ":8080"
	  auth   = auth.corp

	  handle "/healthz" {
	    auth   = auth.anonymous
	    action = "ok"
	  }
	}

A route inherits its server's ` + "`auth`" + ` unless it sets its own. ` + "`auth.anonymous`" + `
is predefined and permits an unauthenticated request, which is how a route opts
out of the authentication it would otherwise inherit.

The attribute also accepts a list, in which case the first mechanism that
recognizes the request's credential judges it — and its rejection is final,
rather than falling through to the next. Write ` + "`auth.anonymous`" + ` last to also
allow requests carrying no credential at all:

	handle "/wiki/" {
	  auth = [auth.corp, auth.anonymous]   # sign in to edit, read anonymously
	}

Several blocks may share a name so long as no more than one is enabled, so an
environment variable can choose between them. If every declaration of a name is
disabled, a route naming it alone is unauthenticated; a route naming it
alongside another mechanism is left with that one.

An authenticated request exposes its identity to expressions as ` + "`ctx.auth`" + `.`,
	}
}

// Preprocess records the block so that FinishPreprocessing can see every
// declaration of a name before any of them is processed.
func (h *AuthBlockHandler) Preprocess(block *hcl.Block) hcl.Diagnostics {
	name := block.Labels[1]

	if name == AuthAnonymousName || name == AuthDisabledName {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Reserved auth name",
			Detail: fmt.Sprintf("auth.%s is predefined and cannot be declared. "+
				"auth.%s allows an unauthenticated request; auth.%s is what the name of a "+
				"disabled block resolves to.", name, AuthAnonymousName, AuthDisabledName),
			Subject: block.DefRange.Ptr(),
		}}
	}

	h.declarations[name] = append(h.declarations[name], block)
	return nil
}

// GetBlockDependencyId names the block in the dependency graph.
//
// Only the last declaration of a name carries the shared `auth.<name>` id that
// references resolve against; earlier ones are given a distinct id and the last
// depends on them (see GetBlockDependencies). A dependent therefore waits for
// every declaration of the name, not just for whichever happened to register
// the id last — which is what makes the binding below final by the time anything
// reads it.
func (h *AuthBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	name := block.Labels[1]
	decls := h.declarations[name]
	if len(decls) > 0 && decls[len(decls)-1] != block {
		return authDeclID(block), nil
	}
	return "auth." + name, nil
}

// GetBlockDependencies returns the block's own references, plus — for the last
// declaration of a name — the earlier declarations of that name.
func (h *AuthBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	deps := ExtractBlockDependencies(block, "action", "claims")

	name := block.Labels[1]
	decls := h.declarations[name]
	if len(decls) > 1 && decls[len(decls)-1] == block {
		for _, earlier := range decls[:len(decls)-1] {
			deps = append(deps, authDeclID(earlier))
		}
	}
	return deps, nil
}

// authDeclID is the dependency id of a declaration that is not the last of its
// name. The source position makes it unique without depending on the mechanism,
// so two declarations of one name may share a mechanism.
func authDeclID(block *hcl.Block) string {
	return fmt.Sprintf("auth.%s#%d", block.Labels[1], block.DefRange.Start.Byte)
}

func (h *AuthBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	def := AuthDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &def)
	if diags.HasErrors() {
		return diags
	}
	def.Type = block.Labels[0]
	def.Name = block.Labels[1]
	def.DefRange = block.DefRange

	if def.Disabled {
		// The name still resolves. Binding it to the sentinel rather than
		// leaving it unknown is what lets a config disable a mechanism without
		// every reference to it becoming an error — and the sentinel is dropped
		// from a list, so the other mechanisms on a route keep enforcing.
		h.bindDisabled(config, def.Name)
		return nil
	}

	processor, ok := authRegistry[def.Type]
	if !ok {
		return hcl.Diagnostics{
			unknownTypeDiag("auth", def.Type, authTypeNames(), block.DefRange),
		}
	}

	if existing := h.enabledDeclaration(config, def.Name); existing != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate auth name",
			Detail: fmt.Sprintf("auth.%s is already defined at %s. Several auth blocks may "+
				"share a name, but no more than one of them may be enabled — otherwise "+
				"which one a reference means would depend on declaration order.",
				def.Name, existing.DefRange),
			Subject: block.DefRange.Ptr(),
		}}
	}

	authenticator, diags := processor(config, block, def.RemainingBody)
	if diags.HasErrors() {
		return diags
	}

	h.bind(config, &AuthRef{Name: def.Name, Authenticator: authenticator}, block)
	return nil
}

// enabledDeclaration returns the block that already bound this name to a
// mechanism, or nil.
func (h *AuthBlockHandler) enabledDeclaration(config *Config, name string) *hcl.Block {
	if ref, ok := config.authRefs[name]; ok && ref.kind == authRefReal {
		return config.authBlocks[name]
	}
	return nil
}

// bindDisabled resolves a name to the disabled sentinel, unless an enabled
// declaration has already claimed it.
func (h *AuthBlockHandler) bindDisabled(config *Config, name string) {
	if ref, ok := config.authRefs[name]; ok && ref.kind == authRefReal {
		return
	}
	h.bind(config, AuthDisabled.withName(name), nil)
}

// bind publishes a name into the auth namespace.
func (h *AuthBlockHandler) bind(config *Config, ref *AuthRef, block *hcl.Block) {
	config.authRefs[ref.Name] = ref
	if block != nil {
		config.authBlocks[ref.Name] = block
	}
	config.CtyAuthMap[ref.Name] = NewAuthCapsule(ref)
	config.evalCtx.Variables["auth"] = cty.ObjectVal(config.CtyAuthMap)
}

// withName returns a copy of the sentinel carrying a declared name, so a
// diagnostic can say which reference resolved to it while identity comparison
// against the sentinel still works through kind.
func (r *AuthRef) withName(name string) *AuthRef {
	return &AuthRef{Name: name, kind: r.kind}
}

// initAuthNamespace seeds the two predefined names. They exist whether or not
// any auth block is declared, since a route may say `auth = auth.anonymous` in
// a config that has no auth blocks at all.
func initAuthNamespace(config *Config) {
	config.authRefs = map[string]*AuthRef{
		AuthAnonymousName: AuthAnonymous,
		AuthDisabledName:  AuthDisabled,
	}
	config.authBlocks = map[string]*hcl.Block{}
	config.CtyAuthMap = map[string]cty.Value{
		AuthAnonymousName: NewAuthCapsule(AuthAnonymous),
		AuthDisabledName:  NewAuthCapsule(AuthDisabled),
	}
}
