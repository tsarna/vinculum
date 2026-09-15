package redis

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// startGate holds back every dial until the client has started, or has been
// asked to run a command.
//
// The go-redis client has to exist when the block is processed, because the
// child clients (redis_pubsub, redis_stream, redis_kv) take it then. But with
// min_idle_conns set, constructing it starts dialing straight away — so building
// a config without running it, as `vinculum check` and man::check do, would
// connect to whatever address the config names. The gate keeps construction
// where the children need it and the network behind the first reason to use it.
//
// A command is such a reason as well as Start, because a config can be used
// without being started: `vinculum test --no-serve` runs its tests against a
// built config, and a client there has always connected on first use. Those
// eager dials are not commands, so they wait.
type startGate struct {
	started, stopped chan struct{}
	start, stop      sync.Once
}

func newStartGate() *startGate {
	return &startGate{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (g *startGate) open()  { g.start.Do(func() { close(g.started) }) }
func (g *startGate) close() { g.stop.Do(func() { close(g.stopped) }) }

var errClientStopped = errors.New("redis client stopped")

// wait returns nil once the client has started, and an error if it stops
// first or ctx ends.
func (g *startGate) wait(ctx context.Context) error {
	// A stopped client refuses even if it had started, which the select below
	// would otherwise choose between at random.
	select {
	case <-g.stopped:
		return errClientStopped
	default:
	}
	select {
	case <-g.started:
		return nil
	case <-g.stopped:
		return errClientStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ProcessHook and ProcessPipelineHook open the gate for the first command, and
// DialHook passes through; together they make startGate a goredis.Hook.
func (g *startGate) DialHook(next goredis.DialHook) goredis.DialHook { return next }

func (g *startGate) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		g.open()
		return next(ctx, cmd)
	}
}

func (g *startGate) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		g.open()
		return next(ctx, cmds)
	}
}

// dialer is go-redis's own dialer behind the gate, so a connection is made
// exactly as it would be without one: the same timeout default, the same TLS.
func (g *startGate) dialer(opts *goredis.UniversalOptions) func(context.Context, string, string) (net.Conn, error) {
	timeout := opts.DialTimeout
	if timeout == 0 {
		timeout = 5 * time.Second // go-redis's default, applied when Dialer is nil
	}
	dial := goredis.NewDialer(&goredis.Options{DialTimeout: timeout, TLSConfig: opts.TLSConfig})
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if err := g.wait(ctx); err != nil {
			return nil, err
		}
		return dial(ctx, network, addr)
	}
}
