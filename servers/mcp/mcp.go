package mcp

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
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
	Listen        string                  `hcl:"listen,optional"`
	Path          string                  `hcl:"path,optional"`
	ServerName    string                  `hcl:"server_name,optional"`
	ServerVersion string                  `hcl:"server_version,optional"`
	Disabled      bool                    `hcl:"disabled,optional"`
	TLS           *cfg.TLSConfig          `hcl:"tls,block"`
	Auth          *cfg.AuthConfig         `hcl:"auth,block"`
	DefRange      hcl.Range               `hcl:",def_range"`
	Resources     []mcpResourceDefinition `hcl:"resource,block"`
	Tools         []mcpToolDefinition     `hcl:"tool,block"`
	Prompts       []mcpPromptDefinition   `hcl:"prompt,block"`
}

type mcpResourceDefinition struct {
	URI         string         `hcl:",label"`
	Name        string         `hcl:"name"`
	Description string         `hcl:"description,optional"`
	MimeType    string         `hcl:"mime_type,optional"`
	Disabled    bool           `hcl:"disabled,optional"`
	Action      hcl.Expression `hcl:"action,optional"`
	DefRange    hcl.Range      `hcl:",def_range"`
}

type mcpParamDefinition struct {
	Name        string         `hcl:",label"`
	Type        string         `hcl:"type"`
	Description string         `hcl:"description,optional"`
	Required    bool           `hcl:"required,optional"`
	Default     hcl.Expression `hcl:"default,optional"`
	Enum        hcl.Expression `hcl:"enum,optional"`
	DefRange    hcl.Range      `hcl:",def_range"`
}

type mcpToolDefinition struct {
	Name        string               `hcl:",label"`
	Description string               `hcl:"description"`
	Disabled    bool                 `hcl:"disabled,optional"`
	Params      []mcpParamDefinition `hcl:"param,block"`
	Action      hcl.Expression       `hcl:"action,optional"`
	DefRange    hcl.Range            `hcl:",def_range"`
}

type mcpPromptDefinition struct {
	Name        string               `hcl:",label"`
	Description string               `hcl:"description,optional"`
	Disabled    bool                 `hcl:"disabled,optional"`
	Params      []mcpParamDefinition `hcl:"param,block"`
	Action      hcl.Expression       `hcl:"action,optional"`
	DefRange    hcl.Range            `hcl:",def_range"`
}

func init() {
	cfg.RegisterServerType("mcp", ProcessMcpServerBlock)
}

// ProcessMcpServerBlock parses an "server mcp" block and creates an McpServer.
func ProcessMcpServerBlock(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Listener, hcl.Diagnostics) {
	var def McpServerDefinition
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}

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

	srv, err := New(ServerConfig{
		Name:          name,
		Listen:        def.Listen,
		Path:          def.Path,
		ServerName:    def.ServerName,
		ServerVersion: def.ServerVersion,
		TLSConfig:     tlsCfg,
		Auth:          def.Auth,
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
