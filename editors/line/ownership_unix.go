//go:build unix

package line

import (
	"os"
	"syscall"
)

func fileOwnership(fi os.FileInfo) (uid, gid int, ok bool) {
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}
