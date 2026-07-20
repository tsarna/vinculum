package config

import (
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	richcty "github.com/tsarna/rich-cty-types"
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

func (h *ClientBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	// Lazy attributes: action, auth, and on_decode_error expressions are
	// evaluated at request time against a per-request eval context, not
	// at config load. Excluding them from dependency extraction lets a
	// client safely reference itself or other clients in those slots
	// (e.g. an OAuth client whose auth hook calls back through
	// http_post(ctx, client.<self>, "/token", ...), or a receiver whose
	// on_decode_error publishes to its own dead-letter bus).
	return ExtractBlockDependencies(block, "action", "auth", "on_decode_error"), nil
}

func (h *ClientBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	clientDef := ClientDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &clientDef)
	if diags.HasErrors() {
		return diags
	}
	clientDef.DefRange = block.DefRange

	if clientDef.Disabled {
		return nil
	}

	clients, ok := config.Clients[block.Labels[0]]
	if !ok {
		clients = make(map[string]Client)
		config.Clients[block.Labels[0]] = clients
	}

	if _, ok := config.CtyClientMap[block.Labels[1]]; ok {
		// The existing entry may live under a different type bucket,
		// e.g. when `client "http" "foo"` and `client "httpmock" "foo"`
		// are both enabled. Walk all type buckets to find it so the
		// error message can point at the right def range.
		var existingDef Client
		for _, byName := range config.Clients {
			if c, exists := byName[block.Labels[1]]; exists {
				existingDef = c
				break
			}
		}
		var detail string
		if existingDef != nil {
			detail = fmt.Sprintf("Client %s already defined at %s", block.Labels[1], existingDef.GetDefRange())
		} else {
			detail = fmt.Sprintf("Client %s already defined", block.Labels[1])
		}
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Client already defined",
				Detail:   detail,
				Subject:  block.DefRange.Ptr(),
			},
		}
	}

	var client Client

	processor, ok := clientRegistry[block.Labels[0]]
	if !ok {
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid client type",
				Detail:   fmt.Sprintf("Invalid client type: %s", block.Labels[0]),
				Subject:  &block.DefRange,
			},
		}
	}
	client, diags = processor(config, block, clientDef.RemainingBody)

	if diags.HasErrors() {
		return diags
	}

	// Ensure the client's DefRange is set from the block header.
	// The hcl:",def_range" tag does not work with gohcl.DecodeBody on a
	// body (only on nested blocks), so processor-set def.DefRange may be
	// zero. Overwrite with the block's DefRange which is always correct.
	if bc, ok := client.(interface{ SetDefRange(hcl.Range) }); ok {
		bc.SetDefRange(block.DefRange)
	}

	config.Clients[block.Labels[0]][block.Labels[1]] = client
	if cv, ok := client.(CtyValuer); ok {
		config.CtyClientMap[block.Labels[1]] = cv.CtyValue()
	} else {
		config.CtyClientMap[block.Labels[1]] = NewClientCapsule(client)
	}
	config.evalCtx.Variables["client"] = cty.ObjectVal(config.CtyClientMap)

	return nil
}

// Client is the base identity interface for all client types.
type Client interface {
	GetName() string
	GetDefRange() hcl.Range
}

// ClientProcessor is a function that processes a client block and returns a Client.
type ClientProcessor func(config *Config, block *hcl.Block, body hcl.Body) (Client, hcl.Diagnostics)

var clientRegistry = map[string]ClientProcessor{}

// RegisterClientType registers a processor for a named client type.
// Sub-packages call this from their init() function.
func RegisterClientType(typeName string, p ClientProcessor) {
	recordPlugin("client." + typeName)
	clientRegistry[typeName] = p
}

// BusClient is a client backed by the vinculum-bus pub/sub system (e.g. VWS).
type BusClient interface {
	Client
	Build() (bus.Client, error)
	GetClient() bus.Client
	GetSubscriber() bus.Subscriber
	SetSubscriber(subscriber bus.Subscriber)
}

// BaseClient holds the minimal fields common to all client types.
type BaseClient struct {
	Name     string
	DefRange hcl.Range
}

func (s *BaseClient) GetName() string {
	return s.Name
}

func (s *BaseClient) GetDefRange() hcl.Range {
	return s.DefRange
}

func (s *BaseClient) SetDefRange(r hcl.Range) {
	s.DefRange = r
}

// BaseBusClient is the base struct for bus-backed clients (VWS, etc.).
type BaseBusClient struct {
	BaseClient
	Subscriber bus.Subscriber
	Client     bus.Client
}

func (s *BaseBusClient) GetSubscriber() bus.Subscriber {
	return s.Subscriber
}

func (s *BaseBusClient) SetSubscriber(subscriber bus.Subscriber) {
	s.Subscriber = subscriber
}

func (s *BaseBusClient) GetClient() bus.Client {
	return s.Client
}

// CtyValuer is an optional interface that Client implementations may satisfy
// to provide a custom cty value for the eval context. If not implemented, the
// client is wrapped in a plain ClientCapsule.
type CtyValuer interface {
	CtyValue() cty.Value
}

// ClientCapsuleType is a cty capsule type for wrapping Client instances.
var ClientCapsuleType = cty.CapsuleWithOps("client", reflect.TypeOf((*any)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("client(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "client"
	},
})

// NewClientCapsule creates a new cty capsule value wrapping a Client.
func NewClientCapsule(client Client) cty.Value {
	return cty.CapsuleVal(ClientCapsuleType, client)
}

// GetClientFromCapsule extracts a Client from a cty capsule value. Any capsule
// whose encapsulated value implements Client is accepted (the generic client
// capsule, or a client-specific capsule like http_client/sql_client), mirroring
// GetSubscriberFromCapsule — so client-specific capsule types dispatch here too.
func GetClientFromCapsule(val cty.Value) (Client, error) {
	if val.Type().IsCapsuleType() {
		if client, ok := val.EncapsulatedValue().(Client); ok {
			return client, nil
		}
	}
	return nil, fmt.Errorf("expected Client capsule, got %s", val.Type().FriendlyName())
}

// IsClient reports whether val is (or carries under a _capsule attribute) a
// capsule whose encapsulated value implements Client. It backs the functy
// "client" open type, matching the generic client capsule, client-specific
// capsules (http_client/sql_client), and rich client objects (e.g. the sql client
// object) alike. Null is handled by the constraint before the predicate runs.
func IsClient(val cty.Value) error {
	enc, err := richcty.GetCapsuleFromValue(val)
	if err != nil {
		return err
	}
	if _, ok := enc.(Client); !ok {
		return fmt.Errorf("expected a client, got %T", enc)
	}
	return nil
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
				Summary:  "Failed to get client from expression",
				Detail:   err.Error(),
				Subject:  &exprRange,
			},
		}
	}

	return client, nil
}
