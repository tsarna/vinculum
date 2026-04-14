package ambient

import (
	"os"
	"os/user"
	"runtime"
	"strconv"
	"time"

	timecty "github.com/tsarna/time-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/platform"
	"github.com/zclconf/go-cty/cty"
)

// processStartTime is captured once at package initialization so that
// sys.starttime reflects the true process start time even after a config
// rebuild (e.g. on SIGHUP).
var processStartTime = time.Now()

func init() {
	cfg.RegisterAmbientProvider("sys", func(c *cfg.Config) cty.Value {
		return GetSysObject(c.BaseDir, c.WriteDir, c.EnabledFeatureNames())
	})
}

// GetSysObject returns a cty object containing process and host identity
// information, suitable for providing to an HCL evaluation context as "sys".
// All values are captured once at config-build time. baseDir is the value of
// the --file-path flag, or empty string if it was not specified.
// writeDir is the value of the --write-path flag, or empty string if not set.
// features is the sorted list of enabled feature flag names.
func GetSysObject(baseDir string, writeDir string, features []string) cty.Value {
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

	// Base directory for file write functions (--write-path flag); empty if not set
	sysMap["writepath"] = cty.StringVal(writeDir)

	// Approximate process start time (captured at package init, not config-build time)
	sysMap["starttime"] = timecty.NewTimeCapsule(processStartTime)

	// System boot time (platform-specific; falls back to processStartTime on unsupported OSes)
	sysMap["boottime"] = timecty.NewTimeCapsule(getBootTime())

	// Enabled feature flags by name (e.g. "readfiles", "writefiles", "allowkill")
	if len(features) == 0 {
		sysMap["features"] = cty.ListValEmpty(cty.String)
	} else {
		featureVals := make([]cty.Value, len(features))
		for i, f := range features {
			featureVals[i] = cty.StringVal(f)
		}
		sysMap["features"] = cty.ListVal(featureVals)
	}

	// Registered plugin names (e.g. "ambient.sys", "client.kafka", "server.mcp")
	pluginNames := cfg.RegisteredPlugins()
	if len(pluginNames) == 0 {
		sysMap["plugins"] = cty.ListValEmpty(cty.String)
	} else {
		pluginVals := make([]cty.Value, len(pluginNames))
		for i, n := range pluginNames {
			pluginVals[i] = cty.StringVal(n)
		}
		sysMap["plugins"] = cty.ListVal(pluginVals)
	}

	// Signals: one Number attribute per signal name + "bynumber" reverse map.
	allSigs := platform.AllSignals()
	sigObjMap := make(map[string]cty.Value, len(allSigs)+1)
	byNumber := make(map[string]cty.Value, len(allSigs))
	for name, num := range allSigs {
		sigObjMap[name] = cty.NumberIntVal(int64(num))
		byNumber[strconv.Itoa(int(num))] = cty.StringVal(name)
	}
	sigObjMap["bynumber"] = cty.MapVal(byNumber)
	sysMap["signals"] = cty.ObjectVal(sigObjMap)

	return cty.ObjectVal(sysMap)
}
