package config

import (
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The builder options a build of submitted text uses — see man::check — and
// the teardown Build now does on itself when it fails late.

func buildBytes(t *testing.T, b *ConfigBuilder, src string) (*Config, string) {
	t.Helper()
	cfg, diags := b.WithLogger(zap.NewNop()).WithSources([]byte(src)).Build()
	if cfg != nil {
		t.Cleanup(cfg.Discard)
	}
	if diags.HasErrors() {
		return cfg, diags.Error()
	}
	return cfg, ""
}

func TestWithEnvironment(t *testing.T) {
	t.Setenv("VINCULUM_SANDBOX_SECRET", "hunter2")

	t.Run("the process environment by default", func(t *testing.T) {
		cfg, errs := buildBytes(t, NewConfig(), `const { x = env.VINCULUM_SANDBOX_SECRET }`)
		require.Empty(t, errs)
		assert.Equal(t, "hunter2", cfg.Constants["x"].AsString())
	})

	t.Run("an empty environment hides it", func(t *testing.T) {
		_, errs := buildBytes(t, NewConfig().WithEnvironment([]string{}),
			`const { x = tonumber(env.VINCULUM_SANDBOX_SECRET) }`)
		require.NotEmpty(t, errs)
		assert.NotContains(t, errs, "hunter2")

		cfg, errs := buildBytes(t, NewConfig().WithEnvironment([]string{}),
			`const { x = try(env.VINCULUM_SANDBOX_SECRET, "fallback") }`)
		require.Empty(t, errs)
		assert.Equal(t, "fallback", cfg.Constants["x"].AsString(), "try() still falls back")
	})

	t.Run("a given environment replaces it", func(t *testing.T) {
		cfg, errs := buildBytes(t, NewConfig().WithEnvironment([]string{"GIVEN=yes"}),
			`const {
  x = env.GIVEN
  y = try(env.VINCULUM_SANDBOX_SECRET, "absent")
}`)
		require.Empty(t, errs)
		assert.Equal(t, "yes", cfg.Constants["x"].AsString())
		assert.Equal(t, "absent", cfg.Constants["y"].AsString())
	})
}

func TestWithMaxCallDepth(t *testing.T) {
	t.Run("unbounded recursion is an error, not a stack overflow", func(t *testing.T) {
		// Without the limit this is fatal to the test binary.
		_, errs := buildBytes(t, NewConfig().WithMaxCallDepth(100), `
function "forever" {
  params = [x]
  result = forever(x)
}
const { y = forever(1) }
`)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs, `function "forever" was called more than 100 deep`)
		assert.Less(t, len(errs), 500, "said once, not once per level: %s", errs)
	})

	t.Run("a variadic function is limited too", func(t *testing.T) {
		_, errs := buildBytes(t, NewConfig().WithMaxCallDepth(100), `
function "spread" {
  params         = [x]
  variadic_param = rest
  result         = length(rest) > 5 ? spread(x, rest...) : spread(x, concat(rest, [x])...)
}
const { y = spread(1) }
`)
		assert.Contains(t, errs, `function "spread" was called more than 100 deep`)

		cfg, errs := buildBytes(t, NewConfig().WithMaxCallDepth(100), `
function "tally" {
  params         = [x]
  variadic_param = rest
  result         = x + length(rest)
}
const { y = tally(1, "a", "b") }
`)
		require.Empty(t, errs)
		assert.Equal(t, "3", cfg.Constants["y"].AsBigFloat().String())
	})

	t.Run("mutual recursion shares the count", func(t *testing.T) {
		_, errs := buildBytes(t, NewConfig().WithMaxCallDepth(100), `
function "ping" {
  params = [x]
  result = pong(x)
}
function "pong" {
  params = [x]
  result = ping(x)
}
const { y = ping(1) }
`)
		assert.Contains(t, errs, "was called more than 100 deep")
	})

	t.Run("recursion that terminates is linear, and gives its value", func(t *testing.T) {
		// Unlimited, the userfunc extension evaluates each level twice, so
		// 60 levels would be 2^60 evaluations. cond() evaluates only the branch
		// it takes, which is what lets it terminate at all.
		start := time.Now()
		cfg, errs := buildBytes(t, NewConfig().WithMaxCallDepth(100), `
function "countdown" {
  params = [n]
  result = cond(n == 0, "done", countdown(n - 1))
}
const { y = countdown(60) }
`)
		require.Empty(t, errs)
		assert.Equal(t, "done", cfg.Constants["y"].AsString())
		assert.Less(t, time.Since(start), 10*time.Second)
	})
}

func TestBuildDiscardsWhatItBuiltWhenItFailsLate(t *testing.T) {
	src := `
bus "one" {}
bus "two" {}
bus "three" {}
assert "fails" { condition = false }
`
	// Settle first, so a goroutine left over from an earlier test is not
	// counted against this one.
	runtime.GC()
	before := runtime.NumGoroutine()

	for range 10 {
		cfg, diags := NewConfig().WithLogger(zap.NewNop()).WithSources([]byte(src)).Build()
		require.Nil(t, cfg)
		require.True(t, diags.HasErrors())
	}

	// Ten failed builds of three buses would leave thirty dispatch goroutines.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= before+2
	}, 5*time.Second, 20*time.Millisecond,
		"goroutines: %d before, %d after", before, runtime.NumGoroutine())
}
