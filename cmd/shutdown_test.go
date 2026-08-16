package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// recorder appends to a shared log as each teardown phase reaches it, so a test
// can assert the order the phases actually ran in.
type recorder struct {
	name string
	log  *[]string
}

func (r *recorder) Drain(context.Context) error {
	*r.log = append(*r.log, "drain:"+r.name)
	return nil
}

func (r *recorder) PreStop() error {
	*r.log = append(*r.log, "prestop:"+r.name)
	return nil
}

func (r *recorder) Stop() error {
	*r.log = append(*r.log, "stop:"+r.name)
	return nil
}

// The ordering is the whole point of the drain phase: every listener must stop
// accepting before any client, bus, or subscription is torn down. Reversing
// these two lets a request that arrives during shutdown run its handler against
// a closed SQL pool or a stopped bus.
func TestShutdownDrainsBeforeStopping(t *testing.T) {
	var log []string
	a := &recorder{name: "a", log: &log}
	b := &recorder{name: "b", log: &log}

	cfg := &config.Config{
		Drainables:    []config.Drainable{a, b},
		PreStoppables: []config.PreStoppable{a, b},
		Stoppables:    []config.Stoppable{a, b},
	}

	shutdown(cfg, zap.NewNop())

	require.Equal(t, []string{
		"drain:b", "drain:a",
		"prestop:b", "prestop:a",
		"stop:b", "stop:a",
	}, log)
}

// Reverse registration order is what makes a mounted server drain after the
// server "http" block hosting it: the mounted block is processed first, so it
// registers first and drains last.
func TestDrainRunsInReverseRegistrationOrder(t *testing.T) {
	var log []string
	mounted := &recorder{name: "mounted", log: &log}
	host := &recorder{name: "host", log: &log}

	cfg := &config.Config{Drainables: []config.Drainable{mounted, host}}
	drain(cfg, zap.NewNop())

	assert.Equal(t, []string{"drain:host", "drain:mounted"}, log)
}

// A Drainable that fails must not abort the phase — the remaining listeners
// still need closing, and the phases after this one still need to run.
func TestDrainContinuesPastAFailure(t *testing.T) {
	var log []string
	ok := &recorder{name: "ok", log: &log}

	cfg := &config.Config{
		Drainables: []config.Drainable{ok, failingDrainable{}},
		Stoppables: []config.Stoppable{ok},
	}

	shutdown(cfg, zap.NewNop())

	assert.Equal(t, []string{"drain:ok", "stop:ok"}, log,
		"a failed drain should not stop the teardown sequence")
}

type failingDrainable struct{}

func (failingDrainable) Drain(context.Context) error { return context.DeadlineExceeded }
