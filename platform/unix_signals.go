//go:build unix

package platform

import (
	"os"
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
