package config

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	mcppkg "github.com/tsarna/vinculum/mcp"
)

// McpServer wraps an mcp.Server and implements the config Listener, Startable,
// and HandlerServer interfaces so it integrates with the vinculum config system.
type McpServer struct {
	BaseServer
	server *mcppkg.Server
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

// ProcessMcpServerBlock parses an "server mcp" block and creates an McpServer.
func ProcessMcpServerBlock(config *Config, block *hcl.Block, remainingBody hcl.Body) (Listener, hcl.Diagnostics) {
	var def McpServerDefinition
	diags := gohcl.DecodeBody(remainingBody, config.evalCtx, &def)
	if diags.HasErrors() {
		return nil, diags
	}

	if def.Disabled {
		return nil, nil
	}

	name := block.Labels[1]

	if def.Listen == "" {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Missing listen address",
			Detail:   "MCP server requires a 'listen' address (e.g. \":8080\")",
			Subject:  &def.DefRange,
		})
		return nil, diags
	}

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

	srv, err := mcppkg.New(mcppkg.ServerConfig{
		Name:          name,
		Listen:        def.Listen,
		Path:          def.Path,
		ServerName:    def.ServerName,
		ServerVersion: def.ServerVersion,
		ParentEvalCtx: config.evalCtx,
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
		BaseServer: BaseServer{Name: name, DefRange: def.DefRange},
		server:     srv,
	}

	config.Startables = append(config.Startables, mcpSrv)

	return mcpSrv, nil
}

func buildResourceDefs(defs []mcpResourceDefinition, block *hcl.Block) ([]mcppkg.ResourceDef, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	var result []mcppkg.ResourceDef
	for _, d := range defs {
		if d.Disabled {
			continue
		}
		if !IsExpressionProvided(d.Action) {
			defRange := d.DefRange
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing action",
				Detail:   fmt.Sprintf("Resource %q requires an 'action' expression", d.URI),
				Subject:  &defRange,
			})
			continue
		}
		tmpl, err := mcppkg.ParseResourceTemplate(d.URI)
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
		result = append(result, mcppkg.ResourceDef{
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

func buildToolDefs(defs []mcpToolDefinition, block *hcl.Block) ([]mcppkg.ToolDef, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	var result []mcppkg.ToolDef
	for _, d := range defs {
		if d.Disabled {
			continue
		}
		if !IsExpressionProvided(d.Action) {
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
		result = append(result, mcppkg.ToolDef{
			Name:        d.Name,
			Description: d.Description,
			Params:      params,
			Action:      d.Action,
		})
	}
	return result, diags
}

func buildPromptDefs(defs []mcpPromptDefinition, block *hcl.Block) ([]mcppkg.PromptDef, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	var result []mcppkg.PromptDef
	for _, d := range defs {
		if d.Disabled {
			continue
		}
		if !IsExpressionProvided(d.Action) {
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
		result = append(result, mcppkg.PromptDef{
			Name:        d.Name,
			Description: d.Description,
			Params:      params,
			Action:      d.Action,
		})
	}
	return result, diags
}

func buildParamDefs(defs []mcpParamDefinition) ([]mcppkg.ParamDef, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	result := make([]mcppkg.ParamDef, 0, len(defs))
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
		result = append(result, mcppkg.ParamDef{
			Name:        d.Name,
			Type:        d.Type,
			Description: d.Description,
			Required:    d.Required,
		})
	}
	return result, diags
}
