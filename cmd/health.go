package cmd

import (
	"fmt"
	"net"
	"net/http"

	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// startHealthListener serves /readyz, /livez, and /healthz on --health-listen,
// or returns nil when the flag was not given.
//
// It is a plain net.Listen plus a three-route ServeMux, deliberately not a
// synthesized `server "http"` block, and it publishes no `server.<name>`.
// Consistency argues otherwise and consistency is usually right; four things
// weigh against it here.
//
// Its lifecycle is the inverse of every other component's. It must bind before
// the Startables loop, so a probe during boot gets "503 starting" rather than a
// refused connection, and it must outlive every Stoppable so that draining
// answers 503 while in-flight work finishes. Teardown runs in reverse
// registration order, which is exactly wrong for something that must be first
// up and last down.
//
// It bypasses everything `server "http"` adds — auth (a kubelet cannot
// authenticate), baggage filtering, real_ip, request logging, otel spans, and
// TLS, which a single flag has no way to express. Modelling it as a full server
// would mean instantiating that pipeline and then suppressing nearly all of it.
//
// Layering forbids the tidy version anyway: servers/http imports config, so
// config cannot construct an HttpServer, and it would have to be built here
// regardless. "Make it a real server" would relocate a constructor, not
// consolidate a code path.
//
// And a published name would cost more than it returns: it would have to be
// reserved against a config declaring it, described in the namespace schema,
// and special-cased in `vinculum check`, for a name nothing can act on, since a
// flag-created server accepts no handle blocks.
//
// What the instinct is right about is the handler. There is exactly one
// implementation of the three endpoints, and both mounting paths serve it.
func startHealthListener(cfg *config.Config, logger *zap.Logger) (*healthListener, error) {
	if healthListen == "" {
		return nil, nil
	}

	// Bind synchronously: an operator who asked for probes and did not get
	// them has a pod that never becomes ready, for a reason nothing reports.
	ln, err := net.Listen("tcp", healthListen)
	if err != nil {
		return nil, fmt.Errorf(
			"--health-listen %s: %w (set VINCULUM_HEALTH_LISTEN= to an empty value to turn it off)",
			healthListen, err)
	}

	srv := &http.Server{Handler: cfg.HealthMux(healthVerbose)}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Error("Health listener stopped", zap.Error(err))
		}
	}()

	logger.Info("Serving health endpoints",
		zap.String("addr", ln.Addr().String()),
		zap.Bool("verbose", healthVerbose),
	)
	return &healthListener{srv: srv, addr: ln.Addr().String()}, nil
}

// healthListener is the running probe listener. It carries its resolved address
// because the flag may name port 0, and an http.Server does not expose the
// listener it was handed.
type healthListener struct {
	srv  *http.Server
	addr string
}

func (h *healthListener) Addr() string { return h.addr }

// Close stops serving. It runs after the whole teardown sequence, so a probe
// arriving mid-drain still gets an honest 503 rather than a refused connection.
func (h *healthListener) Close() error {
	if h == nil {
		return nil
	}
	return h.srv.Close()
}
