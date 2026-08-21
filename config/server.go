package config

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

type ServerDefinition struct {
	Type string `hcl:"type,label"`
	Name string `hcl:"name,label"`

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

// Schema describes the server block for `vinculum schema`. A typed block has
// no body of its own; each server type contributes its own via WithSchema.
func (h *ServerBlockHandler) Schema() TypeSchema {
	return TypeSchema{
		Summary: "A server that accepts inbound connections.",
		Doc: `The first label selects the server type and the second names it, making it
available in expressions as ` + "`server.<name>`" + `.

Servers that expose an HTTP handler — ` + "`mcp`" + `, ` + "`metrics`" + `, ` + "`vws`" + `,
` + "`websocket`" + ` — can either listen on their own address or be mounted on a route of
a ` + "`server \"http\"`" + ` block with ` + "`handler = server.<name>`" + `.`,
	}
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
	serverDef.DefRange = block.DefRange

	if serverDef.Disabled {
		return nil
	}

	servers, ok := config.Servers[block.Labels[0]]
	if !ok {
		servers = make(map[string]Listener)
		config.Servers[block.Labels[0]] = servers
	}

	if _, ok := config.CtyServerMap[block.Labels[1]]; ok {
		// Searched across every type, not just this block's: the colliding
		// server may well be of another type.
		where := "elsewhere in this configuration"
		if existing, found := findAcrossTypes(config.Servers, block.Labels[1]); found {
			where = existing.GetDefRange().String()
		}
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Server already defined",
				Detail: fmt.Sprintf("Server %q is already defined at %s. Server names are global: "+
					"server.%s names one server whatever its type, so two enabled blocks cannot "+
					"share a name. Rename one, or set disabled on the one this configuration "+
					"should not use.",
					block.Labels[1], where, block.Labels[1]),
				Subject: block.DefRange.Ptr(),
			},
		}
	}

	var server Listener

	processor, ok := serverRegistry[block.Labels[0]]
	if !ok {
		return hcl.Diagnostics{
			unknownTypeDiag("server", block.Labels[0], sortedKeys(serverRegistry), block.DefRange),
		}
	}
	server, diags = processor(config, block, serverDef.RemainingBody)

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

// HandlerServer is a Listener that can serve HTTP requests when mounted
// into an HTTP server block via a handle block.
type HandlerServer interface {
	Listener
	GetHandler() http.Handler
}

// DrainHTTPServer is the Drainable implementation shared by every server type
// that owns an *http.Server: stop accepting, wait out the requests already in
// flight, and force the rest closed when timeout expires. A zero timeout waits
// indefinitely.
//
// srv may be nil — a server that was never started has nothing to drain, and a
// mounted one does not own a listener at all.
//
// Note that Shutdown does not touch hijacked connections, so a server that
// hands connections off (WebSocket upgrades) needs those closed separately by
// whatever owns them.
func DrainHTTPServer(ctx context.Context, srv *http.Server, timeout time.Duration, logger *zap.Logger, kind, name string) error {
	if srv == nil {
		return nil
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	err := srv.Shutdown(ctx)
	if err == nil {
		return nil
	}

	// A deadline here is the expected outcome of a handler that outlasts the
	// grace period, not an operational failure: say what is being cut off and
	// cut it off, rather than letting a stuck request hold shutdown open.
	if errors.Is(err, context.DeadlineExceeded) {
		if logger != nil {
			logger.Warn("Timed out draining in-flight requests, closing connections",
				zap.String("server", kind),
				zap.String("name", name),
				zap.Duration("shutdown_timeout", timeout),
			)
		}
		return srv.Close()
	}

	return err
}

// ServerProcessor is a function that processes a server block and returns a Listener.
type ServerProcessor func(config *Config, block *hcl.Block, body hcl.Body) (Listener, hcl.Diagnostics)

var serverRegistry = map[string]ServerProcessor{}

// RegisterServerType registers a processor for a named server type.
// Sub-packages call this from their init() function, optionally passing
// WithSchema to describe the block for `vinculum schema`.
func RegisterServerType(typeName string, p ServerProcessor, opts ...RegisterOption) {
	recordPlugin("server." + typeName)
	serverRegistry[typeName] = p
	registerTypeSchema("server", typeName, opts)
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
