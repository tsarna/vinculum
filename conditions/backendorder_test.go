package conditions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "github.com/tsarna/vinculum/clients/otlp" // register client "otlp" so a backend exists
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// End to end: a condition with no `tracing` attribute resolves the default
// tracing backend, so it has to be processed after the block that provides one
// — however the configuration is ordered. Resolving first silently yields no
// provider, and the hooks are then untraced for the life of the process.

const otlpBackend = `
client "otlp" "t" {
    endpoint     = "http://127.0.0.1:4318"
    service_name = "test"
}
`

const tracedTimer = `
condition "timer" "c" {
    on_activate = log::info("active")
}
`

func timerHooks(t *testing.T, src string) *HookDispatcher {
	t.Helper()
	c := buildConfig(t, []byte(src))
	require.Contains(t, c.CtyConditionMap, "c")
	cond := c.CtyConditionMap["c"].EncapsulatedValue().(*TimerCondition)
	require.NotNil(t, cond.hooks, "the fixture must configure a hook to dispatch")
	return cond.hooks
}

func TestConditionDeclaredBeforeTheBackendIsStillTraced(t *testing.T) {
	hooks := timerHooks(t, tracedTimer+otlpBackend)

	assert.NotNil(t, hooks.tracerProvider,
		"a condition declared before the otlp client got no tracer provider")
}

// The same configuration the other way round, which worked before the ordering
// rule existed and must keep working.
func TestConditionDeclaredAfterTheBackendIsTraced(t *testing.T) {
	hooks := timerHooks(t, otlpBackend+tracedTimer)

	assert.NotNil(t, hooks.tracerProvider)
}

// With no backend at all, resolution stays quiet and yields nothing: an
// untraced configuration must not acquire a diagnostic per condition.
func TestConditionWithoutABackendHasNoTracerProvider(t *testing.T) {
	hooks := timerHooks(t, tracedTimer)

	assert.Nil(t, hooks.tracerProvider)
}

// The new attribute, and the proof it is wired rather than decoded and dropped:
// naming a backend explicitly reaches the dispatcher, and naming something that
// is not a backend is rejected.
func TestConditionTracingAcceptsAnExplicitOtlpClient(t *testing.T) {
	hooks := timerHooks(t, `
condition "timer" "c" {
    tracing     = client.t
    on_activate = log::info("active")
}
`+otlpBackend)

	assert.NotNil(t, hooks.tracerProvider)
}

func TestConditionTracingRejectsSomethingThatIsNotAnOtlpClient(t *testing.T) {
	_, diags := cfg.NewConfig().WithSources([]byte(`
bus "main" {}
condition "timer" "c" {
    tracing     = bus.main
    on_activate = log::info("active")
}
`)).WithLogger(zap.NewNop()).Build()

	assert.True(t, diags.HasErrors(), "tracing = bus.main should be rejected")
}
