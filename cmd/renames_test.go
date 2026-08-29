package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// TestRenameTableMatchesTheLanguage is the drift guard for config/renames.go.
//
// It lives here for the same reason TestNoFunctionNameCollisions does: the cmd
// package blank-imports every subsystem, so this is the only place the whole
// function set exists to check a table of replacement names against. Package
// config sees none of the plugins that provide `time::now` or `sky::sunrise`.
//
// The invariant is worth a test of its own because a wrong entry is worse than
// a missing one. It is stated as fact rather than offered as a guess, and it
// suppresses the nearest-match suggestion that might have been right — so an
// entry pointing at a name that no longer exists sends an upgrade somewhere it
// cannot go, silently and confidently.
func TestRenameTableMatchesTheLanguage(t *testing.T) {
	cfg, diags := config.NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())
	for _, b := range cfg.Buses {
		b.Stop() //nolint:errcheck
	}

	assert.Empty(t, cfg.RenameProblems(),
		"config/renames.go no longer matches the language; see the comment on RenameProblems")
}

// The diagnostics the table exists to produce, end to end through a real
// config. Each of these reported nothing useful before it: two said nothing at
// all, and `now` and `since` confidently suggested an unrelated function.
func TestRenamedFunctionIsReportedWithItsNewName(t *testing.T) {
	for _, tc := range []struct{ call, want string }{
		{"sunrise(1, 2)", `It was renamed "sky::sunrise" in 0.43.0.`},
		{"nextzoneserial(1)", `It was renamed "dns::next_zone_serial" in 0.43.0.`},
		{"now()", `It was renamed "time::now" in 0.43.0.`},
		{"since(1)", `It was renamed "time::since" in 0.43.0.`},
		{"basicauth(1, 2)", `It was renamed "http::basic_auth" in 0.43.0.`},
		{"serialize(1)", `It was renamed "wire::serialize" in 0.43.0.`},
	} {
		t.Run(tc.call, func(t *testing.T) {
			got := buildRefCheck(t, `
bus "main" {}
subscription "s" {
    target = bus.main
    topics = ["a"]
    action = `+tc.call+`
}
`)
			assert.Contains(t, got, tc.want)
			assert.NotContains(t, got, "Did you mean",
				"a stated rename must replace the guess, not sit beside it")
		})
	}
}

// A rename the suggester already finds is deliberately absent from the table,
// so the guess is what an author still gets. This pins the line the table's own
// comment draws — the entries earn their place by being what edit distance
// cannot reach.
func TestRenameTheSuggesterFindsIsLeftToTheSuggester(t *testing.T) {
	got := buildRefCheck(t, `
bus "main" {}
subscription "s" {
    target = bus.main
    topics = ["a"]
    action = randint(1, 10)
}
`)
	assert.Contains(t, got, `Did you mean "rand::int"?`)
}

func TestRenamedCtxFieldNamesTheReceiversOwnField(t *testing.T) {
	got := buildRefCheck(t, `
bus "main" {}

client "kafka" "k" {
  brokers = ["localhost:9092"]

  receiver "r" {
    group_id   = "g"
    subscriber = bus.main

    subscription "kafka.topic" {
      vinculum_topic = "in/${ctx.topic}"
    }
  }
}
`)
	assert.Contains(t, got, `Unknown ctx field "topic"`)
	assert.Contains(t, got, "It was removed in 0.46.0.")
	// The replacement is per receiver, so the useful half is the field list.
	assert.Contains(t, got, "kafka_topic")
}

// `ctx.topic` is renamed out of one context shape, not out of the language. A
// subscription's action still has it, and must not be told otherwise.
func TestCtxTopicIsStillFineWhereItStillExists(t *testing.T) {
	got := buildRefCheck(t, `
bus "main" {}
subscription "s" {
    target = bus.main
    topics = ["a"]
    action = ctx.topic
}
`)
	assert.Empty(t, got)
}
