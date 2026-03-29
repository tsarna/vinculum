//go:build darwin

package ambient

import (
	"time"

	"golang.org/x/sys/unix"
)

// getBootTime returns the system boot time via the kern.boottime sysctl.
func getBootTime() time.Time {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return processStartTime
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000)
}
