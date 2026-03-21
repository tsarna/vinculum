package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tsarna/vinculum/functions"
	"github.com/yosida95/uritemplate/v3"
	"github.com/zclconf/go-cty/cty"
)

// ResourceDef holds the parsed definition of a single MCP resource.
type ResourceDef struct {
	URI         string
	Name        string
	Description string
	MIMEType    string
	Action      hcl.Expression
	// Template is non-nil when URI contains {variables}.
	Template *uritemplate.Template
}

// IsTemplate reports whether this resource uses a URI template.
func (d *ResourceDef) IsTemplate() bool {
	return d.Template != nil
}

// ParseResourceTemplate parses the URI template if the URI contains '{'.
// Returns a ResourceDef with Template set, or an error.
func ParseResourceTemplate(uri string) (*uritemplate.Template, error) {
	if !strings.Contains(uri, "{") {
		return nil, nil
	}
	return uritemplate.New(uri)
}

func registerResources(s *Server, defs []ResourceDef) {
	for _, def := range defs {
		def := def // capture for closure
		if def.IsTemplate() {
			s.sdkServer.AddResourceTemplate(&sdkmcp.ResourceTemplate{
				URITemplate: def.URI,
				Name:        def.Name,
				Description: def.Description,
				MIMEType:    def.MIMEType,
			}, makeResourceHandler(s, def))
		} else {
			s.sdkServer.AddResource(&sdkmcp.Resource{
				URI:         def.URI,
				Name:        def.Name,
				Description: def.Description,
				MIMEType:    def.MIMEType,
			}, makeResourceHandler(s, def))
		}
	}
}

func makeResourceHandler(s *Server, def ResourceDef) sdkmcp.ResourceHandler {
	return func(goCtx context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		uri := req.Params.URI

		// Extract URI template variables
		templateVars := map[string]string{}
		if def.Template != nil {
			vals := def.Template.Match(uri)
			for name, val := range vals {
				templateVars[name] = val.String()
			}
		}

		evalCtx, diags := buildResourceEvalContext(goCtx, s.parentEvalCtx, s.name, uri, templateVars, s.mcpFuncs)
		if diags.HasErrors() {
			return nil, fmt.Errorf("building eval context: %s", diags.Error())
		}

		result, diags := def.Action.Value(evalCtx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("resource action: %s", diags.Error())
		}

		contents, err := ctyToResourceContents(uri, def.MIMEType, result)
		if err != nil {
			return nil, err
		}

		return &sdkmcp.ReadResourceResult{Contents: contents}, nil
	}
}

func ctyToResourceContents(uri, mimeType string, val cty.Value) ([]*sdkmcp.ResourceContents, error) {
	if val.Type() == cty.String {
		return []*sdkmcp.ResourceContents{{
			URI:      uri,
			Text:     val.AsString(),
			MIMEType: mimeType,
		}}, nil
	}

	if r := functions.GetMCPResult(val); r != nil {
		switch r.Kind {
		case "image":
			return []*sdkmcp.ResourceContents{{
				URI:      uri,
				Blob:     r.Data,
				MIMEType: r.MIMEType,
			}}, nil
		default:
			return nil, fmt.Errorf("mcp_result kind %q is not valid for resource content", r.Kind)
		}
	}

	return nil, fmt.Errorf("resource action returned unsupported type %s; expected string or mcp_image()", val.Type().FriendlyName())
}
