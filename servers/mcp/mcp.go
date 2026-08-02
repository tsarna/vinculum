package mcp

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
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

// HCL struct definitions for the "server mcp" block

type McpServerDefinition struct {
	Listen        string                       `hcl:"listen,optional"`
	Path          string                       `hcl:"path,optional"`
	ServerName    string                       `hcl:"server_name,optional"`
	ServerVersion string                       `hcl:"server_version,optional"`
	Disabled      bool                         `hcl:"disabled,optional"`
	Tracing       hcl.Expression               `hcl:"tracing,optional"`
	Metrics       hcl.Expression               `hcl:"metrics,optional"`
	TLS           *cfg.TLSConfig               `hcl:"tls,block"`
	Auth          *cfg.AuthConfig              `hcl:"auth,block"`
	Baggage       *hclutil.BaggageFilterConfig `hcl:"baggage,block"`
	DefRange      hcl.Range                    `hcl:",def_range"`
	Resources     []mcpResourceDefinition      `hcl:"resource,block"`
	Tools         []mcpToolDefinition          `hcl:"tool,block"`
	Prompts       []mcpPromptDefinition        `hcl:"prompt,block"`
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
	Doc:     "The label is the parameter name; its value arrives as `ctx.args.<name>`.",
	Attrs: map[string]cfg.AttrMeta{
		"type": {
			Summary: "Type of the parameter.",
			Enum:    []string{"string", "number", "boolean"},
		},
		"description": {
			Summary: "What the parameter means, shown to the model.",
		},
		"required": {
			Summary: "Whether the client must supply the parameter.",
			Hint:    cfg.HintBool,
		},
		"default": {
			Summary: "Value used when the client omits the parameter.",
			Hint:    cfg.HintExpression,
		},
		"enum": {
			Summary: "Closed set of values the parameter accepts.",
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
			Doc:     "Standalone mode only.",
		},
		"server_name": {
			Summary: "Name reported to clients during initialization.",
			Doc:     "Defaults to the block's name.",
		},
		"server_version": {
			Summary: "Version reported to clients during initialization.",
		},
		"disabled": cfg.DisabledAttr,
		"tracing": {
			Summary: "Where to report request traces.",
			Doc:     "A `client \"otlp\"` block. Spans follow the GenAI/MCP semantic conventions. Auto-wires to the default when omitted.",
			Hint:    cfg.HintTracingRef,
		},
		"metrics": cfg.MetricsAttr,
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
					Doc:     "Its value becomes the contents: a string is returned as-is, anything else is JSON-encoded. `ctx.uri` is the resolved URI and `ctx.args` holds any template placeholders.",
					Hint:    cfg.HintActionExpression,
					Context: "mcp-resource",
				},
				"disabled": {
					Summary: "Skip this resource entirely.",
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
					Doc:     "Arguments arrive as `ctx.args.<param>`. Return `mcp::error(message)` to report failure to the model.",
					Hint:    cfg.HintActionExpression,
					Context: "mcp-tool",
				},
				"disabled": {
					Summary: "Skip this tool entirely.",
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
					Doc:     "Arguments arrive as `ctx.args.<param>`. Return a string, or `mcp::user_message()`/`mcp::assistant_message()` values to control message roles.",
					Hint:    cfg.HintActionExpression,
					Context: "mcp-prompt",
				},
				"disabled": {
					Summary: "Skip this prompt entirely.",
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
	tools, toolDiags := buildToolDefs(def.Tools, block)
	diags = diags.Extend(toolDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	// Build prompt defs
	prompts, promptDiags := buildPromptDefs(def.Prompts, block)
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

	srv, err := New(ServerConfig{
		Name:          name,
		Listen:        def.Listen,
		Path:          def.Path,
		ServerName:    def.ServerName,
		ServerVersion: def.ServerVersion,
		TLSConfig:     tlsCfg,
		Auth:          def.Auth,
		OtlpClient:    otlpClient,
		MeterProvider: mp,
		BaggageFilter: def.Baggage,
		ParentEvalCtx: config.EvalCtx(),
		Logger:        config.Logger,
		Resources:     resources,
		Tools:         tools,
		Prompts:       prompts,
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

	if def.Listen != "" {
		config.Startables = append(config.Startables, mcpSrv)
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

func buildToolDefs(defs []mcpToolDefinition, block *hcl.Block) ([]ToolDef, hcl.Diagnostics) {
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
		params, paramDiags := buildParamDefs(d.Params)
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

func buildPromptDefs(defs []mcpPromptDefinition, block *hcl.Block) ([]PromptDef, hcl.Diagnostics) {
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
		params, paramDiags := buildParamDefs(d.Params)
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

func buildParamDefs(defs []mcpParamDefinition) ([]ParamDef, hcl.Diagnostics) {
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
		result = append(result, ParamDef{
			Name:        d.Name,
			Type:        d.Type,
			Description: d.Description,
			Required:    d.Required,
		})
	}
	return result, diags
}
