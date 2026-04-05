//go:build unix

package line

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// acquireFileLock opens (or creates) a sibling .lock file for path and acquires
// an exclusive flock on it. The caller must close the returned *os.File when the
// edit is complete; closing releases the lock automatically. The .lock file is
// left on disk (harmless, empty) so that subsequent callers can flock it without
// recreating it.
func acquireFileLock(path string) (*os.File, error) {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file %s: %w", lockPath, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquiring lock on %s: %w", lockPath, err)
	}
	return f, nil
}
