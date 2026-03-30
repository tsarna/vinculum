//go:build unix

package platform

import (
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type Signal = unix.Signal

func SignalNum(name string) Signal {
	return unix.SignalNum(name)
}

func FromOsSignal(sig os.Signal) Signal {
	if s, ok := sig.(syscall.Signal); ok {
		return Signal(s)
	}

	return Signal(0)
}

// AllSignals returns a map of signal name → number for every signal known on
// this platform, discovered by probing numbers 1..64 with unix.SignalName.
// Only entries whose name starts with "SIG" are included.
func AllSignals() map[string]Signal {
	signals := make(map[string]Signal)
	for i := 1; i <= 64; i++ {
		s := unix.Signal(i)
		if name := unix.SignalName(s); strings.HasPrefix(name, "SIG") {
			signals[name] = Signal(s)
		}
	}
	return signals
}
