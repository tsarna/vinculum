package config

import (
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"
)

type ServerDefinition struct {
	Type string `hcl:",label"`
	Name string `hcl:",label"`

	Disabled      bool      `hcl:"disabled,optional"`
	DefRange      hcl.Range `hcl:",def_range"`
	RemainingBody hcl.Body  `hcl:",remain"`
}

type ServerBlockHandler struct {
	BlockHandlerBase
}

func NewServerBlockHandler() *ServerBlockHandler {
	return &ServerBlockHandler{}
}

func (h *ServerBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "server." + block.Labels[1], nil
}

func (h *ServerBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	serverDef := ServerDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &serverDef)
	if diags.HasErrors() {
		return diags
	}

	if serverDef.Disabled {
		return nil
	}

	servers, ok := config.Servers[block.Labels[0]]
	if !ok {
		servers = make(map[string]Listener)
		config.Servers[block.Labels[0]] = servers
	}

	if _, ok := config.CtyServerMap[block.Labels[1]]; ok {
		existingDef := servers[block.Labels[1]]
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Server already defined",
				Detail:   fmt.Sprintf("Server %s already defined at %s", block.Labels[1], existingDef.GetDefRange()),
				Subject:  &serverDef.DefRange,
			},
		}
	}

	var server Listener

	switch block.Labels[0] {
	case "http":
		server, diags = ProcessHttpServerBlock(config, block, serverDef.RemainingBody)

	case "vws":
		server, diags = ProcessVinculumWebsocketsServerBlock(config, block, serverDef.RemainingBody)

	case "websocket":
		server, diags = ProcessWebsocketsServerBlock(config, block, serverDef.RemainingBody)

	default:
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid server type",
				Detail:   fmt.Sprintf("Invalid server type: %s", block.Labels[0]),
				Subject:  &block.DefRange,
			},
		}
	}

	if diags.HasErrors() {
		return diags
	}

	config.Servers[block.Labels[0]][block.Labels[1]] = server
	config.CtyServerMap[block.Labels[1]] = NewServerCapsule(server)
	config.evalCtx.Variables["server"] = cty.ObjectVal(config.CtyServerMap)

	return nil
}

type Listener interface {
	GetName() string
	GetDefRange() hcl.Range
}

type BaseServer struct {
	Name     string
	DefRange hcl.Range
}

func (s *BaseServer) GetName() string {
	return s.Name
}

func (s *BaseServer) GetDefRange() hcl.Range {
	return s.DefRange
}

// ServerCapsuleType is a cty capsule type for wrapping Server instances
var ServerCapsuleType = cty.CapsuleWithOps("server", reflect.TypeOf((*any)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("server(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "server"
	},
})

// NewEventBusCapsule creates a new cty capsule value wrapping an EventBus
func NewServerCapsule(server Listener) cty.Value {
	return cty.CapsuleVal(ServerCapsuleType, server)
}

// GetServerFromCapsule extracts an Server from a cty capsule value
func GetServerFromCapsule(val cty.Value) (Listener, error) {
	if val.Type() != ServerCapsuleType {
		return nil, fmt.Errorf("expected Server capsule, got %s", val.Type().FriendlyName())
	}

	encapsulated := val.EncapsulatedValue()
	server, ok := encapsulated.(Listener)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not an Server, got %T", encapsulated)
	}
	return server, nil
}

func GetServerFromExpression(config *Config, busExpr hcl.Expression) (Listener, hcl.Diagnostics) {
	serverCapsule, diags := busExpr.Value(config.evalCtx)
	if diags.HasErrors() {
		return nil, diags
	}

	server, err := GetServerFromCapsule(serverCapsule)
	if err != nil {
		exprRange := busExpr.Range()

		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to get server from expression",
				Detail:   err.Error(),
				Subject:  &exprRange,
			},
		}
	}

	return server, nil
}
