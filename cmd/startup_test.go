package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// starter records that it was reached, and fails with whatever it was given.
type starter struct {
	name string
	err  error
	log  *[]string
}

func (s *starter) Start() error {
	*s.log = append(*s.log, "start:"+s.name)
	return s.err
}

func (s *starter) PostStart() error {
	*s.log = append(*s.log, "poststart:"+s.name)
	return s.err
}

// The case the Startable contract exists for: a broker that is not listening
// yet must not stop the components after it from starting. Before this, one
// unreachable dependency could keep an HTTP listener from ever binding.
func TestStartAllContinuesPastARetriableFailure(t *testing.T) {
	var log []string
	broker := &starter{name: "broker", err: errors.New("dial tcp: connection refused"), log: &log}
	listener := &starter{name: "listener", log: &log}

	cfg := &config.Config{Startables: []config.Startable{broker, listener}}

	require.NoError(t, startAll(cfg, zap.NewNop()))
	assert.Equal(t, []string{"start:broker", "start:listener"}, log)
}

// A terminal failure stops boot where it happened. Anything after it is not
// started, so teardown has less to undo and the log names one cause rather than
// a cascade of components that failed because the first one did.
func TestStartAllAbortsOnATerminalFailure(t *testing.T) {
	var log []string
	bound := &starter{name: "bound", err: config.Terminal(errors.New("address already in use")), log: &log}
	later := &starter{name: "later", log: &log}

	cfg := &config.Config{Startables: []config.Startable{bound, later}}

	err := startAll(cfg, zap.NewNop())
	require.Error(t, err)
	assert.Equal(t, []string{"start:bound"}, log, "boot should stop at the terminal failure")

	var exit *ExitCodeError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 1, exit.Code)
	assert.True(t, exit.Reported, "the boot log is the explanation; main should not restate it")
	assert.Contains(t, err.Error(), "address already in use",
		"the wrapper must not hide the cause it was given")
}

// PostStart is subject to the same classification as Start — the phase is later
// but the question is the same one.
func TestStartAllAbortsOnATerminalPostStart(t *testing.T) {
	var log []string
	ok := &starter{name: "ok", log: &log}
	bad := &starter{name: "bad", err: config.Terminal(errors.New("nope")), log: &log}

	cfg := &config.Config{
		Startables:     []config.Startable{ok},
		PostStartables: []config.PostStartable{ok, bad},
	}

	require.Error(t, startAll(cfg, zap.NewNop()))
	assert.Equal(t, []string{"start:ok", "poststart:ok", "poststart:bad"}, log)
}

// Terminal is a classification carried alongside the failure, not a replacement
// for it: a caller that inspects the cause must see what it saw before.
func TestTerminalPreservesTheCause(t *testing.T) {
	cause := errors.New("no such driver")
	wrapped := config.Terminal(cause)

	assert.True(t, config.IsTerminal(wrapped))
	assert.ErrorIs(t, wrapped, cause)
	assert.Equal(t, cause.Error(), wrapped.Error())

	assert.Nil(t, config.Terminal(nil), "nil-safe, so a caller can wrap unconditionally")
	assert.False(t, config.IsTerminal(cause), "a plain error is retriable")
	assert.False(t, config.IsTerminal(nil))
}

// A component may add context on the way out without losing the
// classification, which is what makes wrapping at the call site safe.
func TestIsTerminalLooksThroughWrapping(t *testing.T) {
	err := fmt.Errorf("sql client %q: %w", "main", config.Terminal(errors.New("bad dsn")))
	assert.True(t, config.IsTerminal(err))
}
