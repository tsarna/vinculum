package mcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
)

// McpServer wraps an mcp.Server and implements the config Listener, Startable,
// and HandlerServer interfaces so it integrates with the vinculum config system.
type McpServer struct {
	cfg.BaseServer
	server *Server
}

func (s *McpServer) GetHandler() http.Handler {
	return s.server.HTTPHandler()
}

func (s *McpServer) Start() error {
	return s.server.Start()
}

func (s *McpServer) Drain(ctx context.Context) error {
	return s.server.Drain(ctx)
}

// HCL struct definitions for the "server mcp" block

type McpServerDefinition struct {
	Listen          string                       `hcl:"listen,optional"`
	ShutdownTimeout hcl.Expression               `hcl:"shutdown_timeout,optional"`
	Path            string                       `hcl:"path,optional"`
	ServerName      string                       `hcl:"server_name,optional"`
	ServerVersion   string                       `hcl:"server_version,optional"`
	Disabled        bool                         `hcl:"disabled,optional"`
	Tracing         hcl.Expression               `hcl:"tracing,optional"`
	Metrics         hcl.Expression               `hcl:"metrics,optional"`
	TLS             *cfg.TLSConfig               `hcl:"tls,block"`
	Auth            *cfg.AuthConfig              `hcl:"auth,block"`
	Baggage         *hclutil.BaggageFilterConfig `hcl:"baggage,block"`
	DefRange        hcl.Range                    `hcl:",def_range"`
	Resources       []mcpResourceDefinition      `hcl:"resource,block"`
	Tools           []mcpToolDefinition          `hcl:"tool,block"`
	Prompts         []mcpPromptDefinition        `hcl:"prompt,block"`
}

type mcpResourceDefinition struct {
	URI         string         `hcl:"uri,label"`
	Name        string         `hcl:"name"`
	Description string         `hcl:"description,optional"`
	MimeType    string         `hcl:"mime_type,optional"`
	Disabled    bool           `hcl:"disabled,optional"`
	Action      hcl.Expression `hcl:"action,optional"`
	DefRange    hcl.Range      `hcl:",def_range"`
}

type mcpParamDefinition struct {
	Name        string         `hcl:"name,label"`
	Type        string         `hcl:"type"`
	Description string         `hcl:"description,optional"`
	Required    bool           `hcl:"required,optional"`
	Default     hcl.Expression `hcl:"default,optional"`
	Enum        hcl.Expression `hcl:"enum,optional"`
	DefRange    hcl.Range      `hcl:",def_range"`
}

type mcpToolDefinition struct {
	Name        string               `hcl:"name,label"`
	Description string               `hcl:"description"`
	Disabled    bool                 `hcl:"disabled,optional"`
	Params      []mcpParamDefinition `hcl:"param,block"`
	Action      hcl.Expression       `hcl:"action,optional"`
	DefRange    hcl.Range            `hcl:",def_range"`
}

type mcpPromptDefinition struct {
	Name        string               `hcl:"name,label"`
	Description string               `hcl:"description,optional"`
	Disabled    bool                 `hcl:"disabled,optional"`
	Params      []mcpParamDefinition `hcl:"param,block"`
	Action      hcl.Expression       `hcl:"action,optional"`
	DefRange    hcl.Range            `hcl:",def_range"`
}

func init() {
	cfg.RegisterServerType("mcp", ProcessMcpServerBlock, cfg.WithSchema(mcpServerSchema))
}

// mcpParamSchema documents the param block shared by tools and prompts.
var mcpParamSchema = cfg.TypeSchema{
	Summary: "One argument the client may pass.",
	Doc: `The label is the parameter name; its value arrives as ` + "`ctx.args.<name>`" + `.

A tool publishes its parameters as a JSON Schema, so ` + "`type`" + `, ` + "`enum`" + `, and
` + "`default`" + ` all reach the model and its arguments arrive with those types. The
prompt protocol carries only a name, a description, and whether the argument is
required — so on a prompt, ` + "`type`" + ` and ` + "`enum`" + ` constrain nothing at runtime and
every argument arrives as a string.`,
	Attrs: map[string]cfg.AttrMeta{
		"type": {
			Summary: "Type of the parameter.",
			Doc:     "Published to the model on a tool. On a prompt it checks `default` and `enum` at config time only, since prompt arguments are strings on the wire.",
			Enum:    []string{"string", "number", "boolean"},
		},
		"description": {
			Summary: "What the parameter means, shown to the model.",
		},
		"required": {
			Summary: "Whether the client must supply the parameter.",
			Hint:    cfg.HintBool,
			Default: "false",
		},
		"default": {
			Summary: "Value used when the client omits the parameter.",
			Doc:     "Applied when the argument is absent, whether or not the client honours the default published in a tool's schema. It must match `type`. On a prompt it is stringified with every other argument.",
			Hint:    cfg.HintExpression,
		},
		"enum": {
			Summary: "Closed set of values the parameter accepts.",
			Doc:     "Every entry must match `type`. Published in a tool's input schema; the prompt protocol has nowhere to carry it, so on a prompt it documents intent without constraining the caller.",
		},
	},
	Constraints: []cfg.Constraint{
		cfg.MutuallyExclusive("required", "default").
			WithMessage("A parameter with a default is not required."),
	},
}

var mcpServerSchema = cfg.TypeSchema{
	Sample:  &McpServerDefinition{},
	Summary: "A Model Context Protocol server.",
	DocPage: "server-mcp.md",
	Doc: `Exposes resources, tools, and prompts to MCP clients over streamable HTTP.

With ` + "`listen`" + ` it runs its own HTTP server; without one, mount it on a route of a
` + "`server \"http\"`" + ` block with ` + "`handler = server.<name>`" + `.`,
	Attrs: map[string]cfg.AttrMeta{
		"listen": {
			Summary: "Address to serve on, as a standalone server.",
			Doc:     "Omit to mount this server into a `server \"http\"` route instead.",
			Hint:    cfg.HintListenAddr,
		},
		"path": {
			Summary: "Path the MCP endpoint is served at.",
			Doc:     "Standalone mode only. A mounted server is reached at the route its `handle` block declares.",
			Default: "/",
		},
		"server_name": {
			Summary: "Name reported to clients during initialization.",
			Default: "<name>",
		},
		"server_version": {
			Summary: "Version reported to clients during initialization.",
			Default: "0.0.0",
		},
		"disabled": cfg.DisabledAttr,
		"tracing": {
			Summary: "Where to report request traces.",
			Doc:     "A `client \"otlp\"` block. Spans follow the GenAI/MCP semantic conventions. Auto-wires to the default when omitted.",
			Hint:    cfg.HintTracingRef,
		},
		"metrics": cfg.MetricsAttr,
		"shutdown_timeout": cfg.ShutdownTimeoutAttr.WithDoc(
			"On shutdown the server stops accepting new requests before anything else is torn down, " +
				"then waits this long for what is already in flight. Whatever is still running when the " +
				"time is up is closed out from under it. `0` waits indefinitely. " +
				"Standalone mode only — a mounted server drains with the `server \"http\"` block hosting it."),
	},
	Blocks: map[string]cfg.TypeSchema{
		"resource": {
			Summary: "Data the server exposes to clients.",
			Doc: `The label is the resource URI. Curly-brace placeholders make it a
template — ` + "`db://records/{table}/{id}`" + ` — and each placeholder arrives as
` + "`ctx.args.<name>`" + `.`,
			Attrs: map[string]cfg.AttrMeta{
				"name": {
					Summary: "Display name shown to clients.",
				},
				"description": {
					Summary: "What the resource holds, shown to the model.",
				},
				"mime_type": {
					Summary: "Content type of the resource's contents.",
				},
				"action": {
					Summary: "Expression evaluated when a client reads the resource.",
					Doc:     "Required unless the resource is disabled. Its value becomes the contents, and must be a string — served as-is under `mime_type` — or an `mcp::image()`. Wrap structured data in `jsonencode()`; anything else is an error at request time. `ctx.uri` is the resolved URI and `ctx.args` holds any template placeholders.",
					Hint:    cfg.HintActionExpression,
					Context: "mcp-resource",
				},
				"disabled": {
					Summary: "Skip this resource entirely.",
					Doc:     "The resource is not registered, so clients never see it.",
					Hint:    cfg.HintBool,
				},
			},
		},
		"tool": {
			Summary: "An operation the model can invoke.",
			Doc:     "The label is the tool name. Declare its arguments with `param` blocks.",
			Attrs: map[string]cfg.AttrMeta{
				"description": {
					Summary: "What the tool does, shown to the model.",
					Doc:     "This is how the model decides when to call it, so be specific.",
				},
				"action": {
					Summary: "Expression evaluated when the tool is called.",
					Doc:     "Required unless the tool is disabled. Arguments arrive as `ctx.args.<param>`. A string becomes text content, `mcp::image()` image content, and `mcp::error(message)` reports failure to the model; any other type is an error. Wrap structured data in `jsonencode()`.",
					Hint:    cfg.HintActionExpression,
					Context: "mcp-tool",
				},
				"disabled": {
					Summary: "Skip this tool entirely.",
					Doc:     "The tool is not registered, so the model never sees it.",
					Hint:    cfg.HintBool,
				},
			},
			Blocks: map[string]cfg.TypeSchema{"param": mcpParamSchema},
		},
		"prompt": {
			Summary: "A reusable prompt template clients can render.",
			Doc:     "The label is the prompt name. Declare its arguments with `param` blocks.",
			Attrs: map[string]cfg.AttrMeta{
				"description": {
					Summary: "What the prompt is for, shown to the model.",
				},
				"action": {
					Summary: "Expression evaluated when a client requests the prompt.",
					Doc:     "Required unless the prompt is disabled. Arguments arrive as `ctx.args.<param>`. Return a string, or `mcp::user_message()`/`mcp::assistant_message()` values — singly or as a list — to control message roles.",
					Hint:    cfg.HintActionExpression,
					Context: "mcp-prompt",
				},
				"disabled": {
					Summary: "Skip this prompt entirely.",
					Doc:     "The prompt is not registered, so clients never see it.",
					Hint:    cfg.HintBool,
				},
			},
			Blocks: map[string]cfg.TypeSchema{"param": mcpParamSchema},
		},
	},
}

// ProcessMcpServerBlock parses an "server mcp" block and creates an McpServer.
func ProcessMcpServerBlock(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Listener, hcl.Diagnostics) {
	var def McpServerDefinition
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

	if def.Disabled {
		return nil, nil
	}

	name := block.Labels[1]

	// Build resource defs
	resources, resourceDiags := buildResourceDefs(def.Resources, block)
	diags = diags.Extend(resourceDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	// Build tool defs
	tools, toolDiags := buildToolDefs(config, def.Tools, block)
	diags = diags.Extend(toolDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	// Build prompt defs
	prompts, promptDiags := buildPromptDefs(config, def.Prompts, block)
	diags = diags.Extend(promptDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	// Validate auth block if present.
	if def.Auth != nil {
		if authDiags := cfg.ValidateAuthConfig(def.Auth); authDiags.HasErrors() {
			return nil, authDiags
		}
	}

	if baggageDiags := def.Baggage.Validate(); baggageDiags.HasErrors() {
		return nil, baggageDiags
	}

	var tlsCfg *tls.Config
	if def.TLS != nil {
		if def.Listen == "" {
			defRange := def.TLS.DefRange
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "TLS requires standalone mode",
				Detail:   "A tls block can only be used on a server \"mcp\" block that also has a listen address.",
				Subject:  &defRange,
			}}
		}
		var err error
		tlsCfg, err = def.TLS.BuildTLSServerConfig(config.BaseDir)
		if err != nil {
			defRange := def.TLS.DefRange
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Invalid TLS configuration",
				Detail:   err.Error(),
				Subject:  &defRange,
			}}
		}
	}

	// Resolve tracing client at config parse time.
	otlpClient, tracingDiags := config.ResolveOtlpClient(def.Tracing)
	if tracingDiags.HasErrors() {
		return nil, tracingDiags
	}

	// Resolve metrics backend at config parse time.
	mp, metricsDiags := cfg.ResolveMeterProvider(config, def.Metrics)
	if metricsDiags.HasErrors() {
		return nil, metricsDiags
	}

	shutdownTimeout, timeoutDiags := config.ParseDurationOrDefault(def.ShutdownTimeout, cfg.DefaultShutdownTimeout)
	if timeoutDiags.HasErrors() {
		return nil, timeoutDiags
	}

	srv, err := New(ServerConfig{
		Name:            name,
		Listen:          def.Listen,
		Path:            def.Path,
		ServerName:      def.ServerName,
		ServerVersion:   def.ServerVersion,
		TLSConfig:       tlsCfg,
		Auth:            def.Auth,
		ShutdownTimeout: shutdownTimeout,
		OtlpClient:      otlpClient,
		MeterProvider:   mp,
		BaggageFilter:   def.Baggage,
		ParentEvalCtx:   config.EvalCtx(),
		Config:          config,
		Logger:          config.Logger,
		Resources:       resources,
		Tools:           tools,
		Prompts:         prompts,
	})
	if err != nil {
		defRange := def.DefRange
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Failed to create MCP server",
			Detail:   fmt.Sprintf("Error creating MCP server %q: %v", name, err),
			Subject:  &defRange,
		}}
	}

	mcpSrv := &McpServer{
		BaseServer: cfg.BaseServer{Name: name, DefRange: def.DefRange},
		server:     srv,
	}

	// Only a standalone server owns a listener; a mounted one is drained by the
	// server "http" block that hosts it.
	if def.Listen != "" {
		config.Startables = append(config.Startables, mcpSrv)
		config.Drainables = append(config.Drainables, mcpSrv)
	}

	return mcpSrv, nil
}

func buildResourceDefs(defs []mcpResourceDefinition, block *hcl.Block) ([]ResourceDef, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	var result []ResourceDef
	for _, d := range defs {
		if d.Disabled {
			continue
		}
		if !cfg.IsExpressionProvided(d.Action) {
			defRange := d.DefRange
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing action",
				Detail:   fmt.Sprintf("Resource %q requires an 'action' expression", d.URI),
				Subject:  &defRange,
			})
			continue
		}
		tmpl, err := ParseResourceTemplate(d.URI)
		if err != nil {
			defRange := d.DefRange
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid URI template",
				Detail:   fmt.Sprintf("Resource %q has invalid URI template: %v", d.URI, err),
				Subject:  &defRange,
			})
			continue
		}
		result = append(result, ResourceDef{
			URI:         d.URI,
			Name:        d.Name,
			Description: d.Description,
			MIMEType:    d.MimeType,
			Action:      d.Action,
			Template:    tmpl,
		})
	}
	return result, diags
}

func buildToolDefs(config *cfg.Config, defs []mcpToolDefinition, block *hcl.Block) ([]ToolDef, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	var result []ToolDef
	for _, d := range defs {
		if d.Disabled {
			continue
		}
		if !cfg.IsExpressionProvided(d.Action) {
			defRange := d.DefRange
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing action",
				Detail:   fmt.Sprintf("Tool %q requires an 'action' expression", d.Name),
				Subject:  &defRange,
			})
			continue
		}
		params, paramDiags := buildParamDefs(config, d.Params)
		diags = diags.Extend(paramDiags)
		result = append(result, ToolDef{
			Name:        d.Name,
			Description: d.Description,
			Params:      params,
			Action:      d.Action,
		})
	}
	return result, diags
}

func buildPromptDefs(config *cfg.Config, defs []mcpPromptDefinition, block *hcl.Block) ([]PromptDef, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	var result []PromptDef
	for _, d := range defs {
		if d.Disabled {
			continue
		}
		if !cfg.IsExpressionProvided(d.Action) {
			defRange := d.DefRange
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing action",
				Detail:   fmt.Sprintf("Prompt %q requires an 'action' expression", d.Name),
				Subject:  &defRange,
			})
			continue
		}
		params, paramDiags := buildParamDefs(config, d.Params)
		diags = diags.Extend(paramDiags)
		result = append(result, PromptDef{
			Name:        d.Name,
			Description: d.Description,
			Params:      params,
			Action:      d.Action,
		})
	}
	return result, diags
}

func buildParamDefs(config *cfg.Config, defs []mcpParamDefinition) ([]ParamDef, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	result := make([]ParamDef, 0, len(defs))
	for _, d := range defs {
		switch d.Type {
		case "string", "number", "boolean":
		default:
			defRange := d.DefRange
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid param type",
				Detail:   fmt.Sprintf("Param %q has invalid type %q; expected string, number, or boolean", d.Name, d.Type),
				Subject:  &defRange,
			})
			continue
		}

		param := ParamDef{
			Name:        d.Name,
			Type:        d.Type,
			Description: d.Description,
			Required:    d.Required,
		}

		if cfg.IsExpressionProvided(d.Default) {
			v, moreDiags := paramLiteral(config, d.Default, d.Type, d.Name, "default")
			diags = diags.Extend(moreDiags)
			param.DefaultVal = v
		}

		if cfg.IsExpressionProvided(d.Enum) {
			values, moreDiags := paramEnum(config, d.Enum, d.Type, d.Name)
			diags = diags.Extend(moreDiags)
			param.Enum = values
		}

		result = append(result, param)
	}
	return result, diags
}

// paramLiteral evaluates one param value and checks it against the param's
// declared type, so a mismatch is a config error rather than a tool whose
// published input schema contradicts itself.
func paramLiteral(config *cfg.Config, expr hcl.Expression, paramType, paramName, attr string) (any, hcl.Diagnostics) {
	val, diags := expr.Value(config.EvalCtx())
	if diags.HasErrors() {
		return nil, diags
	}
	return paramLiteralValue(val, paramType, paramName, attr, expr.Range())
}

// paramLiteralValue is the type check itself, shared by `default` (one value)
// and `enum` (each element).
func paramLiteralValue(val cty.Value, paramType, paramName, attr string, rng hcl.Range) (any, hcl.Diagnostics) {
	if val.IsNull() || !val.IsKnown() {
		return nil, nil
	}

	want := map[string]cty.Type{
		"string":  cty.String,
		"number":  cty.Number,
		"boolean": cty.Bool,
	}[paramType]
	if val.Type() != want {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Param value does not match its type",
			Detail: fmt.Sprintf("Param %q declares type %q, so its %s must be a %s, not a %s.",
				paramName, paramType, attr, want.FriendlyName(), val.Type().FriendlyName()),
			Subject: &rng,
		}}
	}

	goVal, err := go2cty2go.CtyToAny(val)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid param value",
			Detail:   fmt.Sprintf("Param %q: %s: %v", paramName, attr, err),
			Subject:  &rng,
		}}
	}
	return goVal, nil
}

// paramEnum evaluates an enum list, checking every element against the param's
// declared type.
func paramEnum(config *cfg.Config, expr hcl.Expression, paramType, paramName string) ([]any, hcl.Diagnostics) {
	val, diags := expr.Value(config.EvalCtx())
	if diags.HasErrors() {
		return nil, diags
	}
	if val.IsNull() || !val.IsKnown() {
		return nil, nil
	}
	if !val.Type().IsTupleType() && !val.Type().IsListType() && !val.Type().IsSetType() {
		rng := expr.Range()
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid param enum",
			Detail:   fmt.Sprintf("Param %q: enum must be a list of values.", paramName),
			Subject:  &rng,
		}}
	}

	var result []any
	for it := val.ElementIterator(); it.Next(); {
		_, elem := it.Element()
		goVal, elemDiags := paramLiteralValue(elem, paramType, paramName, "enum", expr.Range())
		diags = diags.Extend(elemDiags)
		if goVal != nil {
			result = append(result, goVal)
		}
	}
	return result, diags
}
