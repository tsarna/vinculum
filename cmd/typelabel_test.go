package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A typed block dispatches on its first label, and the registry it dispatches
// through is the list of answers — so a label that misses offers the nearest
// one. These run in cmd because that is where every subsystem is imported and
// the registries hold their real contents; package config's own test binary
// knows only the types config itself declares.
func TestUnknownTypeLabelSuggests(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			name: "server",
			src:  `server "htp" "web" { listen = ":8080" }`,
			want: `There is no server type "htp". Did you mean "http"?`,
		},
		{
			name: "client",
			src:  `client "mqqt" "m" { brokers = ["tcp://localhost:1883"] }`,
			want: `There is no client type "mqqt". Did you mean "mqtt"?`,
		},
		{
			name: "trigger",
			src:  "trigger \"cronn\" \"c\" {\n  action = 1\n}",
			want: `There is no trigger type "cronn". Did you mean "cron"?`,
		},
		{
			name: "condition",
			src:  "condition \"timerr\" \"c\" {\n  input = true\n}",
			want: `There is no condition type "timerr". Did you mean "timer"?`,
		},
		{
			name: "metric",
			src:  `metric "guage" "m" { help = "x" }`,
			want: `There is no metric type "guage". Did you mean "gauge"?`,
		},
		{
			name: "wire_format",
			src:  `wire_format "protobufff" "w" {}`,
			want: `There is no wire_format type "protobufff". Did you mean "protobuf"?`,
		},
		{
			name: "editor",
			src:  `editor "lines" "e" {}`,
			want: `There is no editor type "lines". Did you mean "line"?`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := buildRefCheck(t, tc.src)
			require.NotEmpty(t, err, "an unknown type label must fail the load")
			assert.Contains(t, err, tc.want)
		})
	}
}

// With nothing close enough to be a correction, the answer is what the author
// may write instead — until there are too many to read, when it is where to
// find them. `client` is the one block over the line, at eighteen types.
func TestUnknownTypeLabelListsOrPoints(t *testing.T) {
	err := buildRefCheck(t, `server "totally_bogus" "s" {}`)
	assert.Contains(t, err, `There is no server type "totally_bogus". `+
		`Available types: http, mcp, metrics, vws, websocket.`)

	err = buildRefCheck(t, `client "totally_bogus" "c" {}`)
	assert.Contains(t, err, "client types; run `vinculum man client` for the list.")
	assert.NotContains(t, err, "Available types:", "a list that long is not a list to read")
}

// A conditional type is registered only by a configuration that enables it, so
// its absence from the registry is not the author misspelling anything. Saying
// "no such type" would send them looking for a typo that is not there.
func TestUnavailableTypeLabelSaysSo(t *testing.T) {
	err := buildRefCheck(t, "trigger \"file\" \"w\" {\n  path = \"/tmp\"\n  action = 1\n}")

	require.NotEmpty(t, err)
	assert.Contains(t, err, `The trigger type "file" is not available in this configuration.`)
	assert.Contains(t, err, "run `vinculum man trigger file` for what it needs")
	assert.NotContains(t, err, "Did you mean", "the name is right; only the configuration is missing")
}
