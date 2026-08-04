package pager

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// env builds a Getenv over a fixed map, so a test never has to mutate the
// process environment.
func env(pairs map[string]string) func(string) string {
	return func(name string) string { return pairs[name] }
}

func TestPagerCommandPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set falls back to less", nil, "less"},
		{"PAGER is used", map[string]string{"PAGER": "more"}, "more"},
		{
			"VINCULUM_PAGER wins over PAGER",
			map[string]string{"PAGER": "more", "VINCULUM_PAGER": "bat"},
			"bat",
		},
		{
			"an empty setting does not count as set",
			map[string]string{"VINCULUM_PAGER": "  ", "PAGER": "more"},
			"more",
		},
		{"a command line, not just a name", map[string]string{"PAGER": "less -R"}, "less -R"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, pagerCommand(Options{Getenv: env(tc.env)}))
		})
	}
}

func TestChildEnvSuppliesLessDefaults(t *testing.T) {
	// -F is the one that matters: without it a four-line page traps the
	// reader in a pager.
	got := childEnv("less", Options{Getenv: env(nil)})
	require.NotNil(t, got)
	assert.Contains(t, got, "LESS=FRX")

	// Set by the user, left alone.
	assert.Nil(t, childEnv("less", Options{Getenv: env(map[string]string{"LESS": "R"})}))

	// Only less is configured this way; another pager inherits unchanged.
	assert.Nil(t, childEnv("more", Options{Getenv: env(nil)}))
	// Recognized by the program name, not the whole command line.
	assert.NotNil(t, childEnv("/usr/bin/less -i", Options{Getenv: env(nil)}))
}

func TestNoPagerWhenOutputIsNotATerminal(t *testing.T) {
	var buf bytes.Buffer
	// A buffer is not a terminal, which is what makes redirection and piping
	// behave like every other command's.
	assert.Nil(t, command(&buf, Options{Getenv: env(nil)}))
}

func TestNoPagerWhenDisabledOrDeclined(t *testing.T) {
	// os.Stdout under `go test` is not a terminal, so these cannot distinguish
	// their own branch from the terminal test. They assert the contract that
	// matters either way: no pager process is built.
	assert.Nil(t, command(os.Stdout, Options{Disabled: true, Getenv: env(nil)}),
		"--no-pager")
	assert.Nil(t, command(os.Stdout, Options{Getenv: env(map[string]string{"PAGER": "cat"})}),
		"PAGER=cat is how a user says they do not want paging")
}

func TestPageWritesDirectlyWhenNotPaging(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, Page(&buf, "hello\n", Options{Getenv: env(nil)}))
	assert.Equal(t, "hello\n", buf.String())
}

func TestPageDeliversLongOutput(t *testing.T) {
	var buf bytes.Buffer
	text := strings.Repeat("line\n", 10000)
	require.NoError(t, Page(&buf, text, Options{Disabled: true, Getenv: env(nil)}))
	assert.Equal(t, text, buf.String())
}
