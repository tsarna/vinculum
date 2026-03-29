//go:build !darwin && !linux

package ambient

import "time"

// getBootTime returns the process start time as a fallback on platforms where
// a native boot time API is not available.
func getBootTime() time.Time {
	return processStartTime
}
