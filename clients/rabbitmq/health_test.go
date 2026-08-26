package rabbitmq

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"

	// Registers the generic functions, so an on_connect expression in a fixture
	// can have an observable side effect. No cycle: functions imports config,
	// not this package.
	_ "github.com/tsarna/vinculum/functions"
	"github.com/tsarna/vinculum/types"
	"go.uber.org/zap"
)

// healthReports captures what the client pushes to the health subsystem.
type healthReports struct {
	mu   sync.Mutex
	errs []error
}

func (r *healthReports) reporter() cfg.ReadyReporter {
	return func(err error) {
		r.mu.Lock()
		r.errs = append(r.errs, err)
		r.mu.Unlock()
	}
}

func (r *healthReports) all() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

func buildRMQ(t *testing.T, src string) (*RMQClientWrapper, *cfg.Config) {
	t.Helper()
	config, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "%v", diags)
	return config.Clients["rabbitmq"]["broker"].(*RMQClientWrapper), config
}

const rmqNoHooks = `
bus "main" {}

client "rabbitmq" "broker" {
  brokers = ["amqp://127.0.0.1:1/"]

  receiver "in" {
    queue      = "q"
    subscriber = bus.main
  }
}
`

func TestLifecycleHooksExistWithoutVCLHooks(t *testing.T) {
	// They used to be nil unless the configuration declared on_connect /
	// on_disconnect, so a client with neither reported nothing. Health
	// reporting does not depend on the user having asked for a hook.
	c, _ := buildRMQ(t, rmqNoHooks)

	require.NotNil(t, c.clientCfg.OnConnect, "OnConnect must be wired for health")
	require.NotNil(t, c.clientCfg.OnDisconnect, "OnDisconnect must be wired for health")
}

func TestTheLifecycleHooksReportHealth(t *testing.T) {
	c, _ := buildRMQ(t, rmqNoHooks)

	var r healthReports
	c.SetReadyReporter(r.reporter())

	ctx := context.Background()
	c.clientCfg.OnDisconnect(ctx)
	c.clientCfg.OnConnect(ctx)

	got := r.all()
	require.Len(t, got, 2)
	assert.EqualError(t, got[0], "not connected")
	assert.NoError(t, got[1])
}

func TestALifecycleHookStillEvaluatesTheUsersExpression(t *testing.T) {
	c, config := buildRMQ(t, `
bus "main" {}

var "connected" { value = false }

client "rabbitmq" "broker" {
  brokers    = ["amqp://127.0.0.1:1/"]
  on_connect = set(var.connected, true)

  receiver "in" {
    queue      = "q"
    subscriber = bus.main
  }
}
`)

	var r healthReports
	c.SetReadyReporter(r.reporter())
	c.clientCfg.OnConnect(context.Background())

	// Both happen: health is told, and the configuration's own hook still runs.
	assert.Len(t, r.all(), 1)

	v, err := types.GetVariableFromCapsule(config.CtyVarMap["connected"])
	require.NoError(t, err)
	got, err := v.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, got.True())
}

func TestReportingIsSafeBeforeRegistration(t *testing.T) {
	// The reporter arrives at registration, after the client is built. A
	// callback firing before then must be a no-op rather than a panic.
	c, _ := buildRMQ(t, rmqNoHooks)
	assert.NotPanics(t, func() { c.clientCfg.OnDisconnect(context.Background()) })
}
