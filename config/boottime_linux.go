//go:build linux

package config

import (
	"syscall"
	"time"
)

// getBootTime returns the approximate system boot time via syscall.Sysinfo.
// Precision is ±1 second (Uptime is in whole seconds).
func getBootTime() time.Time {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return processStartTime
	}
	return time.Now().Add(-time.Duration(info.Uptime) * time.Second)
}
