package config

import (
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/zclconf/go-cty/cty"
)

type ClientDefinition struct {
	Type string `hcl:",label"`
	Name string `hcl:",label"`

	Disabled      bool      `hcl:"disabled,optional"`
	DefRange      hcl.Range `hcl:",def_range"`
	RemainingBody hcl.Body  `hcl:",remain"`
}

type ClientBlockHandler struct {
	BlockHandlerBase
}

func NewClientBlockHandler() *ClientBlockHandler {
	return &ClientBlockHandler{}
}

func (h *ClientBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "client." + block.Labels[1], nil
}

func (h *ClientBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	clientDef := ClientDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &clientDef)
	if diags.HasErrors() {
		return diags
	}

	if clientDef.Disabled {
		return nil
	}

	clients, ok := config.Clients[block.Labels[0]]
	if !ok {
		clients = make(map[string]Client)
		config.Clients[block.Labels[0]] = clients
	}

	if _, ok := config.CtyClientMap[block.Labels[1]]; ok {
		existingDef := clients[block.Labels[1]]
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Client already defined",
				Detail:   fmt.Sprintf("Client %s already defined at %s", block.Labels[1], existingDef.GetDefRange()),
				Subject:  &clientDef.DefRange,
			},
		}
	}

	var client Client

	switch block.Labels[0] {
	case "vws":
		client, diags = ProcessVinculumWebsocketsClientBlock(config, block, clientDef.RemainingBody)

	default:
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid client type",
				Detail:   fmt.Sprintf("Invalid client type: %s", block.Labels[0]),
				Subject:  &block.DefRange,
			},
		}
	}

	if diags.HasErrors() {
		return diags
	}

	config.Clients[block.Labels[0]][block.Labels[1]] = client
	config.CtyClientMap[block.Labels[1]] = NewClientCapsule(client)
	config.evalCtx.Variables["client"] = cty.ObjectVal(config.CtyClientMap)

	return nil
}

type Client interface {
	Build() (bus.Client, error)
	GetClient() bus.Client
	GetName() string
	GetDefRange() hcl.Range
	GetSubscriber() bus.Subscriber
	SetSubscriber(subscriber bus.Subscriber)
}

type BaseClient struct {
	Name       string
	DefRange   hcl.Range
	Subscriber bus.Subscriber
	Client     bus.Client
}

func (s *BaseClient) GetName() string {
	return s.Name
}

func (s *BaseClient) GetDefRange() hcl.Range {
	return s.DefRange
}

func (s *BaseClient) GetSubscriber() bus.Subscriber {
	return s.Subscriber
}

func (s *BaseClient) SetSubscriber(subscriber bus.Subscriber) {
	s.Subscriber = subscriber
}

func (s *BaseClient) GetClient() bus.Client {
	return s.Client
}

// ServerCapsuleType is a cty capsule type for wrapping Server instances
var ClientCapsuleType = cty.CapsuleWithOps("client", reflect.TypeOf((*any)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("client(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "client"
	},
})

// NewEventBusCapsule creates a new cty capsule value wrapping an EventBus
func NewClientCapsule(client Client) cty.Value {
	return cty.CapsuleVal(ClientCapsuleType, client)
}

// GetServerFromCapsule extracts an Server from a cty capsule value
func GetClientFromCapsule(val cty.Value) (Client, error) {
	if val.Type() != ClientCapsuleType {
		return nil, fmt.Errorf("expected Client capsule, got %s", val.Type().FriendlyName())
	}

	encapsulated := val.EncapsulatedValue()
	client, ok := encapsulated.(Client)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not an Client, got %T", encapsulated)
	}
	return client, nil
}

func GetClientFromExpression(config *Config, clientExpr hcl.Expression) (Client, hcl.Diagnostics) {
	clientCapsule, diags := clientExpr.Value(config.evalCtx)
	if diags.HasErrors() {
		return nil, diags
	}

	client, err := GetClientFromCapsule(clientCapsule)
	if err != nil {
		exprRange := clientExpr.Range()

		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to get server from expression",
				Detail:   err.Error(),
				Subject:  &exprRange,
			},
		}
	}

	return client, nil
}
