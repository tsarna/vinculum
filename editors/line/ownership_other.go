//go:build !unix

package line

import "os"

func fileOwnership(fi os.FileInfo) (uid, gid int, ok bool) {
	return 0, 0, false
}
