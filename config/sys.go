package config

import (
	"os"
	"os/user"
	"runtime"
	"strconv"

	"github.com/zclconf/go-cty/cty"
)

// GetSysObject returns a cty object containing process and host identity
// information, suitable for providing to an HCL evaluation context as "sys".
// All values are captured once at config-build time. baseDir is the value of
// the --file-path flag, or empty string if it was not specified.
func GetSysObject(baseDir string) cty.Value {
	sysMap := make(map[string]cty.Value)

	// Process ID
	sysMap["pid"] = cty.NumberIntVal(int64(os.Getpid()))

	// Hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	sysMap["hostname"] = cty.StringVal(hostname)

	// Current user info
	var username, groupName string
	var uid, gid int64
	if u, err := user.Current(); err == nil {
		username = u.Username
		if n, err := strconv.Atoi(u.Uid); err == nil {
			uid = int64(n)
		}
		if n, err := strconv.Atoi(u.Gid); err == nil {
			gid = int64(n)
		}
		if g, err := user.LookupGroupId(u.Gid); err == nil {
			groupName = g.Name
		}
	}
	sysMap["user"] = cty.StringVal(username)
	sysMap["uid"] = cty.NumberIntVal(uid)
	sysMap["group"] = cty.StringVal(groupName)
	sysMap["gid"] = cty.NumberIntVal(gid)

	// Platform info
	sysMap["os"] = cty.StringVal(runtime.GOOS)
	sysMap["arch"] = cty.StringVal(runtime.GOARCH)
	sysMap["cpus"] = cty.NumberIntVal(int64(runtime.NumCPU()))

	// Process paths
	executable, err := os.Executable()
	if err != nil {
		executable = ""
	}
	sysMap["executable"] = cty.StringVal(executable)

	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	sysMap["cwd"] = cty.StringVal(cwd)

	homedir, err := os.UserHomeDir()
	if err != nil {
		homedir = ""
	}
	sysMap["homedir"] = cty.StringVal(homedir)

	sysMap["tempdir"] = cty.StringVal(os.TempDir())

	// Base directory for file functions (--file-path flag); empty if not set
	sysMap["filepath"] = cty.StringVal(baseDir)

	return cty.ObjectVal(sysMap)
}
