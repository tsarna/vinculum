package httpserver_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	"go.uber.org/zap"
)

// freePort returns a port nothing is listening on, for a config that has to
// name its address before the server exists.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// startDrainServer builds a one-route server on a real port and starts it. The
// route's handler is replaced with h, so a test can control exactly how long a
// request takes without needing a sleeping VCL action. Start wraps whatever
// handler is present, so the substitution sits inside the same otelhttp and
// real_ip layers a production request goes through.
func startDrainServer(t *testing.T, shutdownTimeout string, h http.Handler) (*httpserver.HttpServer, *cfg.Config, int) {
	t.Helper()

	port := freePort(t)
	timeoutAttr := ""
	if shutdownTimeout != "" {
		timeoutAttr = fmt.Sprintf("shutdown_timeout = %q", shutdownTimeout)
	}
	vcl := fmt.Sprintf(`
server "http" "main" {
    listen = "127.0.0.1:%d"
    %s

    handle "/hello" {
        action = "hi"
    }
}
`, port, timeoutAttr)

	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	srv := c.Servers["http"]["main"].(*httpserver.HttpServer)
	if h != nil {
		srv.Server.Handler = h
	}
	require.NoError(t, srv.Start())
	t.Cleanup(func() { _ = srv.Server.Close() })

	// Wait for the listener to be up before returning; Start listens in a
	// goroutine, so a request sent immediately can beat it.
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 5*time.Second, 10*time.Millisecond, "server should start listening")

	return srv, c, port
}

// A server "http" block must register itself as a Drainable, or shutdown()
// never learns it exists and the port stays open through teardown.
func TestHttpServerRegistersAsDrainable(t *testing.T) {
	_, c, _ := startDrainServer(t, "", nil)
	require.Len(t, c.Drainables, 1, "the http server should be the one Drainable")
}

// The point of the whole feature: after Drain the port stops accepting. Until
// this worked, requests kept arriving while the clients and buses their
// handlers depend on were being stopped.
func TestDrainClosesTheListener(t *testing.T) {
	srv, _, port := startDrainServer(t, "", nil)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello", port))
	require.NoError(t, err, "server should serve before draining")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	require.NoError(t, drainWithin(t, 5*time.Second, srv.Drain))

	_, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello", port))
	assert.Error(t, err, "the listener must be closed once Drain returns")
}

// Draining is not just closing: a request already being served has to finish,
// because the runtime it depends on is torn down as soon as Drain returns.
func TestDrainWaitsForAnInFlightRequest(t *testing.T) {
	release := make(chan struct{})
	served := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(served)
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("finished"))
	})

	srv, _, port := startDrainServer(t, "30s", handler)

	respErr := make(chan error, 1)
	status := make(chan int, 1)
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello", port))
		if err != nil {
			respErr <- err
			return
		}
		defer resp.Body.Close()
		status <- resp.StatusCode
		respErr <- nil
	}()

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran")
	}

	drained := make(chan error, 1)
	go func() { drained <- srv.Drain(context.Background()) }()

	select {
	case <-drained:
		t.Fatal("Drain returned while a request was still in flight")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-drained:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not return after the request finished")
	}

	require.NoError(t, <-respErr, "the in-flight request should complete normally")
	assert.Equal(t, http.StatusOK, <-status)
}

// A handler that never returns must not be able to hold shutdown open forever:
// once shutdown_timeout expires the connection is closed out from under it.
func TestDrainForcesCloseWhenTheTimeoutExpires(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	served := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(served)
		<-block
	})

	srv, _, port := startDrainServer(t, "150ms", handler)

	go func() {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello", port))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran")
	}

	start := time.Now()
	drained := make(chan error, 1)
	go func() { drained <- srv.Drain(context.Background()) }()

	select {
	case err := <-drained:
		require.NoError(t, err, "a forced close is the expected outcome, not an error")
		assert.Less(t, time.Since(start), 3*time.Second,
			"Drain must give up at shutdown_timeout rather than wait on the handler")
	case <-time.After(5 * time.Second):
		t.Fatal("Drain hung on a handler that outlasted shutdown_timeout")
	}
}

// An invalid shutdown_timeout is a config error, not a silent fallback to the
// default — the operator asked for a specific grace period.
func TestInvalidShutdownTimeoutIsADiagnostic(t *testing.T) {
	vcl := fmt.Sprintf(`
server "http" "main" {
    listen           = "127.0.0.1:%d"
    shutdown_timeout = "not-a-duration"

    handle "/hello" {
        action = "hi"
    }
}
`, freePort(t))

	_, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors(), "an unparseable shutdown_timeout should fail the build")
}

// helpers ---------------------------------------------------------------------

// drainWithin runs fn under a deadline and fails the test rather than hanging
// if it never returns.
func drainWithin(t *testing.T, d time.Duration, fn func(context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()

	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatal("drain did not return in time")
		return nil
	}
}
