package config

// The top-level evaluation namespace, as described by `vinculum schema`.
//
// schema.go describes *blocks* — what the parser accepts. This file describes
// what an expression inside one of those blocks may *name*: the roots
// `sys.hostname`, `env.HOME`, `http_status.NotFound`, and `bus.main` all start
// from.
//
// The two halves of the namespace are told apart by where their members come
// from, because that is what decides whether a member can be checked:
//
//   - A **provider** namespace comes from RegisterAmbientProvider, and its
//     members are whatever the provider's cty.Value carries. Those are
//     reflected, so the curated prose beside them cannot drift — except where
//     the provider says the names are not the language's to know (`env`).
//   - A **block** namespace is filled in by the config author: every `bus`
//     block publishes a `bus.<name>`. There is nothing to enumerate, so what is
//     described is the root itself and which block declares into it.

import (
	"fmt"
	"sort"

	"github.com/zclconf/go-cty/cty"
)

// ---------------------------------------------------------------------------
// Curated authoring types
// ---------------------------------------------------------------------------

// NamespaceSchema is curated metadata for one top-level name in the evaluation
// namespace.
//
// For a provider namespace it is registered next to the provider:
//
//	cfg.RegisterAmbientProvider("sys", provide, cfg.WithNamespaceSchema(sysNamespace))
//
// and Members is validated against the value the provider actually returns, in
// both directions — the same contract a block's attributes get.
type NamespaceSchema struct {
	// Block names the top-level block type that publishes names into this
	// namespace, for a namespace the config author fills in (`bus.<name>`).
	// Setting it makes the namespace a block namespace, which has no members of
	// its own to enumerate.
	Block string

	// Summary is a one-line description, used as a completion item's detail.
	Summary string

	// Doc is richer Markdown used for hover; may be multi-paragraph.
	Doc string

	// DocPage names the hand-written reference page for this namespace,
	// relative to doc/, as TypeSchema.DocPage does.
	DocPage string

	// Members holds curated per-member metadata, keyed by member name. Every
	// key must name a member the provider's value carries, and every member it
	// carries must have an entry.
	Members map[string]MemberMeta

	// FreeMembers marks a namespace whose member names are not the language's
	// to know: `env` is the environment of whichever process happens to be
	// running, so enumerating it would describe the machine that generated the
	// document rather than the language.
	//
	// Members is then a floor rather than the whole list — whatever is
	// described is described, and anything else is allowed. Nothing undescribed
	// is emitted, which is also what keeps the document from varying with the
	// machine that produced it, and `vinculum check` accepts any name below
	// such a root.
	FreeMembers bool

	// Constant marks a namespace whose values are identical in every process,
	// so the value is part of the language rather than of the environment and
	// is emitted alongside the member. True of `http_status`; false of `sys`,
	// whose hostname and pid describe the machine.
	Constant bool

	// UniformMemberSummary is the summary taken by every member that does not
	// carry its own. It exists for a namespace whose members are a single
	// uniform family — `http_status` is sixty status codes that differ only in
	// which code they are — where sixty hand-written summaries would say the
	// same sentence sixty times and hide the one fact that differs.
	//
	// Only legal together with Constant, because the emitted value is then what
	// distinguishes one member from another. Without it the members would be
	// documented and indistinguishable.
	UniformMemberSummary string
}

// MemberMeta is curated metadata for a single member of a namespace.
type MemberMeta struct {
	// Summary is a one-line description, shown as a completion item's detail.
	Summary string

	// Doc is richer Markdown for hover.
	Doc string

	// Members describes the members of a member that is itself an object
	// (`sys.functy.version`). An object member must set this or FreeMembers, so
	// that gaining a field is a reported change rather than a silent one.
	Members map[string]MemberMeta

	// FreeMembers marks an object member whose names are not fixed by the
	// language — `sys.signals` carries whichever signals the host OS defines.
	// As on a namespace, Members is then a floor: `sys.signals.bynumber` is
	// described and always present, while the signal names beside it vary with
	// the OS and are neither enumerated nor checked.
	FreeMembers bool
}

// Namespace kinds, emitted as a namespace's `kind`.
const (
	// NamespaceProvider is a namespace contributed by an ambient provider; its
	// members are part of the language.
	NamespaceProvider = "provider"
	// NamespaceBlock is a namespace the config author fills in by declaring
	// blocks; its members are the names in that config.
	NamespaceBlock = "block"
)

// ---------------------------------------------------------------------------
// Block namespaces
// ---------------------------------------------------------------------------

// blockNamespaceSchemas describes the roots that blocks publish names into.
//
// Unlike the provider half there is no registry to reflect: the root name is
// fixed by the block handler that writes it (see bus.go, client.go, variable.go
// and the per-type registrations in triggers/ and conditions/), and its members
// are whatever the config declares. What is checked instead is that Block names
// a real top-level block type, so renaming a block cannot leave a root pointing
// at nothing.
//
// This is the single source of truth for "is this root a block reference",
// which `vinculum check` also reads (see exprcheck.go).
var blockNamespaceSchemas = map[string]NamespaceSchema{
	"auth": {
		Block:   "auth",
		Summary: "Each authentication mechanism, by name.",
		Doc: "Name one in a server's or route's `auth`, or a list of them to accept " +
			"several. Two names are predefined: `auth.anonymous` allows an unauthenticated " +
			"request, and `auth.disabled` is what the name of a switched-off block " +
			"resolves to.",
		DocPage: "auth.md",
	},
	"bus": {
		Block:   "bus",
		Summary: "Each bus, by name.",
		Doc:     "Every bus is declared, `bus.main` included; the name carries no special meaning.",
		DocPage: "config.md#bus",
	},
	"check": {
		Block:   "check",
		Summary: "Each health check, by name.",
		Doc: "Reads as the check's last result with `get()`, and is watchable: a reactive " +
			"expression naming one is re-evaluated when that check passes or fails, and a " +
			"`trigger \"watch\"` over one fires on its transitions without involving the " +
			"aggregate. A check nothing has probed yet reads as `true`.",
		DocPage: "health.md",
	},
	"client": {
		Block:   "client",
		Summary: "Each client, by name.",
		Doc:     "All client types share a single name namespace.",
		DocPage: "config.md#client",
	},
	"condition": {
		Block:   "condition",
		Summary: "Each condition, by name.",
		Doc: "Reads as the condition's current state with `get()`, and is watchable: a reactive " +
			"expression naming one is re-evaluated whenever it changes. All condition types share a " +
			"single name namespace.",
		DocPage: "condition.md",
	},
	"fsm": {
		Block:   "fsm",
		Summary: "Each state machine, by name.",
		Doc: "An fsm receives messages, so it may be used wherever a subscriber is expected, and " +
			"its current state is readable with `get()`.",
		DocPage: "fsm.md",
	},
	"metric": {
		Block:   "metric",
		Summary: "Each metric, by name.",
		Doc:     "Pass one to `increment()`, `observe()`, or `set()` to record a measurement.",
		DocPage: "metric.md",
	},
	"server": {
		Block:   "server",
		Summary: "Each server, by name.",
		Doc: "All server types share a single name namespace — you cannot have both an HTTP " +
			"server and a WebSocket server called `main`.",
		DocPage: "config.md#server",
	},
	"trigger": {
		Block:   "trigger",
		Summary: "Each trigger, by name.",
		Doc:     "All trigger types share a single name namespace.",
		DocPage: "trigger.md",
	},
	"var": {
		Block:   "var",
		Summary: "Each variable, by name.",
		Doc: "Variables are mutable and goroutine-safe; read and write them with `get()`, `set()`, " +
			"and `increment()`. A variable is watchable, so a reactive expression naming one is " +
			"re-evaluated whenever it changes.",
		DocPage: "config.md#var",
	},
	"wire_format": {
		Block:   "wire_format",
		Summary: "Each wire format, by name.",
		Doc:     "Name one in a receiver's or sender's `wire_format` to encode and decode its payloads.",
		DocPage: "config.md#wire_format",
	},
}

// blockNamespaceRoots returns the set of roots that resolve to a block name.
// It is what tells `bus.typo` (a missing bus) from `sys.typo` (a missing member
// of a fixed namespace) when reporting an unresolvable reference.
func blockNamespaceRoots() map[string]bool {
	roots := make(map[string]bool, len(blockNamespaceSchemas))
	for name := range blockNamespaceSchemas {
		roots[name] = true
	}
	return roots
}

// ---------------------------------------------------------------------------
// Emitted document
// ---------------------------------------------------------------------------

// SchemaNamespace describes one top-level name in the evaluation namespace.
type SchemaNamespace struct {
	// Kind is "provider" or "block".
	Kind string `json:"kind"`
	// Block names the top-level block type that publishes names here, for a
	// block namespace.
	Block   string `json:"block,omitempty"`
	Summary string `json:"summary,omitempty"`
	Doc     string `json:"doc,omitempty"`
	// DocPage is the hand-written reference page for this namespace, relative
	// to doc/.
	DocPage string `json:"docPage,omitempty"`
	// Undocumented is true when no curated schema was registered for the
	// namespace, as for a block.
	Undocumented bool `json:"undocumented,omitempty"`
	// Constant is true when every member's value is the same in every process,
	// which is what makes a member's `value` meaningful.
	Constant bool `json:"constant,omitempty"`
	// FreeMembers is true when the member names are not fixed by the language,
	// so `members` is empty by design rather than by omission. A block
	// namespace is always free: its names come from the config.
	FreeMembers bool `json:"freeMembers,omitempty"`
	// Members are the namespace's members, sorted by name.
	Members []*SchemaMember `json:"members"`
}

// SchemaMember describes one member of a namespace, read as `<namespace>.<name>`.
type SchemaMember struct {
	Name string `json:"name"`
	// Type is the coarse value type: the attribute vocabulary plus `object`,
	// `capsule`, and — for a capsule registered under a name usable in a `.cty`
	// annotation — that name, e.g. `time`.
	Type string `json:"type"`
	// Value is the member's literal value, present only in a namespace whose
	// values are the same in every process.
	Value   string `json:"value,omitempty"`
	Summary string `json:"summary,omitempty"`
	Doc     string `json:"doc,omitempty"`
	// FreeMembers is true for an object member whose own member names are not
	// fixed by the language, such as the host's signal table.
	FreeMembers bool `json:"freeMembers,omitempty"`
	// Members are this member's own members, when it is an object.
	Members []*SchemaMember `json:"members,omitempty"`
}

// ---------------------------------------------------------------------------
// Building
// ---------------------------------------------------------------------------

// namespaces describes every top-level name an expression may start from: the
// curated roots that blocks publish names into, and the registered ambient
// providers, reflected from the values they return.
//
// A name claimed by both is a collision the runtime resolves too, and in the
// same direction: ambient providers populate Constants first (see config.go),
// and each block handler then writes its own root over whatever was there. So
// the block namespace is described, and the provider is reported and dropped
// rather than silently describing something no expression can reach.
func (b *schemaBuilder) namespaces() map[string]*SchemaNamespace {
	out := map[string]*SchemaNamespace{}

	for _, name := range sortedKeys(blockNamespaceSchemas) {
		out[name] = b.blockNamespace(name, blockNamespaceSchemas[name])
	}

	for _, e := range ambientProviders {
		switch {
		case out[e.name] != nil && out[e.name].Kind == NamespaceBlock:
			b.problemf("namespace %q is both an ambient provider and a block namespace; the block wins at runtime", e.name)
		case out[e.name] != nil:
			b.problemf("namespace %q is registered by two ambient providers", e.name)
		default:
			out[e.name] = b.providerNamespace(e)
		}
	}

	return out
}

// blockNamespace describes a root the config author fills in.
func (b *schemaBuilder) blockNamespace(name string, ns NamespaceSchema) *SchemaNamespace {
	if b.opts.RequireDocs && ns.Summary == "" {
		b.problemf("namespace %s: missing summary", name)
	}
	if len(ns.Members) > 0 {
		b.problemf("namespace %s: a block namespace has no members to describe; its names come from the config", name)
	}
	if !blockTypeExists(ns.Block) {
		b.problemf("namespace %s: no top-level block type %q", name, ns.Block)
	}
	return &SchemaNamespace{
		Kind:        NamespaceBlock,
		Block:       ns.Block,
		Summary:     ns.Summary,
		Doc:         ns.Doc,
		DocPage:     ns.DocPage,
		FreeMembers: true,
		Members:     []*SchemaMember{},
	}
}

// providerNamespace describes an ambient provider's namespace, reflecting the
// members from the value the provider returns and merging the curated prose
// registered beside it.
func (b *schemaBuilder) providerNamespace(e ambientEntry) *SchemaNamespace {
	out := &SchemaNamespace{
		Kind:    NamespaceProvider,
		Members: []*SchemaMember{},
	}

	ns := NamespaceSchema{}
	if e.schema != nil {
		ns = *e.schema
	} else {
		out.Undocumented = true
		if b.opts.RequireDocs {
			b.problemf("namespace %s: no namespace schema registered", e.name)
		}
	}
	out.Summary = ns.Summary
	out.Doc = ns.Doc
	out.DocPage = ns.DocPage
	out.Constant = ns.Constant
	out.FreeMembers = ns.FreeMembers

	if b.opts.RequireDocs && e.schema != nil && ns.Summary == "" {
		b.problemf("namespace %s: missing summary", e.name)
	}
	if ns.Block != "" {
		b.problemf("namespace %s: an ambient provider's namespace cannot name a block", e.name)
	}
	if ns.UniformMemberSummary != "" && !ns.Constant {
		b.problemf("namespace %s: a uniform member summary says nothing unless the values are constant and emitted", e.name)
	}
	if ns.UniformMemberSummary != "" && ns.FreeMembers {
		b.problemf("namespace %s: free members are not enumerated, so a uniform summary would apply to nothing", e.name)
	}

	val, err := evalAmbientProvider(e)
	if err != nil {
		b.problemf("namespace %s: %v", e.name, err)
		return out
	}
	ty := val.Type()
	if !ty.IsObjectType() {
		b.problemf("namespace %s: provider returned %s, not an object", e.name, ty.FriendlyName())
		return out
	}

	out.Members = b.members(e.name, val, ns.Members, ns.FreeMembers, ns.Constant, ns.UniformMemberSummary)
	return out
}

// members reflects one object's attributes and merges the curated metadata
// describing them. The reflected value is authoritative in both directions: a
// member with nothing said about it is a documentation gap, and a curated
// member the value does not carry is stale prose for something that no longer
// exists.
//
// Where the names are free the first of those does not apply — there is nothing
// to be complete about — so only what is described is emitted. That is also
// what keeps the emitted document from varying with the OS or the environment
// of the machine that produced it.
func (b *schemaBuilder) members(path string, val cty.Value, curated map[string]MemberMeta, free, constant bool, uniform string) []*SchemaMember {
	attrTypes := val.Type().AttributeTypes()

	var names []string
	if free {
		names = sortedKeys(curated)
	} else {
		names = make([]string, 0, len(attrTypes))
		for name := range attrTypes {
			names = append(names, name)
		}
		sort.Strings(names)
	}

	out := make([]*SchemaMember, 0, len(names))
	for _, name := range names {
		memberPath := path + "." + name
		attrType, ok := attrTypes[name]
		if !ok {
			b.problemf("%s: documented member %q does not exist", path, name)
			continue
		}

		meta := curated[name]
		summary := meta.Summary
		if summary == "" {
			summary = uniform
		}
		if b.opts.RequireDocs && summary == "" {
			b.problemf("%s: missing summary", memberPath)
		}

		member := &SchemaMember{
			Name:        name,
			Type:        coarseCtyType(attrType),
			Summary:     summary,
			Doc:         meta.Doc,
			FreeMembers: meta.FreeMembers,
		}
		if constant {
			member.Value = literalValue(val.GetAttr(name))
		}

		switch {
		case !attrType.IsObjectType():
			if len(meta.Members) > 0 || meta.FreeMembers {
				b.problemf("%s: has no members of its own to describe (%s)", memberPath, attrType.FriendlyName())
			}
		case len(meta.Members) == 0 && !meta.FreeMembers:
			// An object that says nothing about its members would let a new
			// field appear with no documentation and no test noticing.
			b.problemf("%s: is an object; describe its members or mark them free", memberPath)
		default:
			member.Members = b.members(memberPath, val.GetAttr(name), meta.Members, meta.FreeMembers, constant, uniform)
		}

		out = append(out, member)
	}

	if !free {
		for _, name := range sortedKeys(curated) {
			if _, ok := attrTypes[name]; !ok {
				b.problemf("%s: documented member %q does not exist", path, name)
			}
		}
	}
	return out
}

// evalAmbientProvider calls a provider with a probe config to learn the shape
// of the value it contributes.
//
// The probe is a zero Config rather than a real one because the shape is what
// is wanted and a real one is not available: `vinculum schema` and `vinculum
// man` describe the language without parsing any configuration. The stock
// providers read only fields a zero Config answers safely. A plugin's provider
// might not, so a panic is turned into a reported problem — a third-party
// plugin must not be able to take down `vinculum man`.
func evalAmbientProvider(e ambientEntry) (val cty.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("provider panicked: %v", r)
		}
	}()
	val = e.p(&Config{})
	if val.IsNull() {
		return cty.NilVal, fmt.Errorf("provider returned a null value")
	}
	return val, nil
}

// coarseCtyType maps a cty type to the coarse value type emitted for a member.
// A capsule registered under a name usable in a `.cty` annotation is reported
// by that name — `sys.starttime` is a `time` — since that is the name a config
// author would write for it.
func coarseCtyType(ty cty.Type) string {
	switch {
	case ty == cty.String:
		return attrTypeString
	case ty == cty.Number:
		return attrTypeNumber
	case ty == cty.Bool:
		return attrTypeBool
	case ty.IsListType(), ty.IsSetType(), ty.IsTupleType():
		return attrTypeList
	case ty.IsMapType():
		return attrTypeMap
	case ty.IsObjectType():
		return CtxTypeObject
	case ty.IsCapsuleType():
		if name := functyTypeName(ty); name != "" {
			return name
		}
		return CtxTypeCapsule
	default:
		return attrTypeExpression
	}
}

// functyTypeName returns the name a type is registered under for use in `.cty`
// annotations, or "". Only closed registrations have an identity to match; an
// open one is a predicate over values, with no type to compare against.
func functyTypeName(ty cty.Type) string {
	for _, r := range functyBuiltinTypes {
		if !r.open && r.ty.Equals(ty) {
			return r.name
		}
	}
	for _, r := range registeredFunctyTypes {
		if !r.open && r.ty.Equals(ty) {
			return r.name
		}
	}
	return ""
}

// literalValue renders a scalar value as a config author would write it, or ""
// for anything that has no useful literal form.
func literalValue(val cty.Value) string {
	if val.IsNull() || !val.IsKnown() {
		return ""
	}
	switch val.Type() {
	case cty.String:
		return val.AsString()
	case cty.Number:
		return val.AsBigFloat().Text('f', -1)
	case cty.Bool:
		if val.True() {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// blockTypeExists reports whether name is a top-level block type.
func blockTypeExists(name string) bool {
	for _, header := range blockSchema {
		if header.Type == name {
			return true
		}
	}
	return false
}
