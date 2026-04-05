//go:build !unix

package line

import (
	"fmt"
	"os"
)

// acquireFileLock is not supported on non-Unix platforms; it always returns an error.
func acquireFileLock(path string) (*os.File, error) {
	return nil, fmt.Errorf("file locking is not supported on this platform")
}
