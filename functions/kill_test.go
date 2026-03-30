//go:build unix

package functions

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"

	_ "github.com/tsarna/vinculum/ambient"
	_ "github.com/tsarna/vinculum/triggers/signals"
	_ "github.com/tsarna/vinculum/triggers/start"
)

// TestKillIntegration verifies the kill() VCL function end-to-end:
//   - A "start" trigger calls kill(sys.pid, SIGUSR1) during config build.
//   - A "signals" trigger listens for SIGUSR1 and relays it as SIGUSR2 via kill().
//
// The test confirms that SIGUSR1 arrives (proving kill() works) and that SIGUSR2
// subsequently arrives (proving the signals trigger round-trip works).
func TestKillIntegration(t *testing.T) {
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(sigCh)

	src := []byte(fmt.Sprintf(`
		trigger "start" "send_sig" {
			action = kill(sys.pid, %d)
		}
		trigger "signals" "relay" {
			SIGUSR1 = kill(sys.pid, %d)
		}
	`, int(syscall.SIGUSR1), int(syscall.SIGUSR2)))

	conf, diags := cfg.NewConfig().
		WithSources(src).
		WithLogger(zap.NewNop()).
		WithFeature("allowkill", "true").
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// Start the signal handler goroutine so the signals trigger can relay
	// the next SIGUSR1 as SIGUSR2.
	require.NoError(t, conf.SigActions.Start())

	// Send SIGUSR1 again; the signals trigger relay will fire and send SIGUSR2.
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGUSR1))

	// waitFor drains the channel until the wanted signal arrives, discarding
	// any duplicate or out-of-order signals.
	waitFor := func(want os.Signal) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case got := <-sigCh:
				if got == want {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %v", want)
			}
		}
	}

	waitFor(syscall.SIGUSR1) // sent by kill() in the start trigger during Build
	waitFor(syscall.SIGUSR2) // relayed by kill() in the signals trigger
}
