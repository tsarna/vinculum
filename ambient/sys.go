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
	"github.com/tsarna/vinculum/version"
	"github.com/zclconf/go-cty/cty"
)

// functyModulePath is the Go module path of the bundled functy (.cty) language,
// used to read its version from build info for sys.functy.version.
const functyModulePath = "github.com/tsarna/functy"

// processStartTime is captured once at package initialization so that
// sys.starttime reflects the true process start time even after a config
// rebuild (e.g. on SIGHUP).
var processStartTime = time.Now()

func init() {
	cfg.RegisterAmbientProvider("sys", func(c *cfg.Config) cty.Value {
		return GetSysObject(c.BaseDir, c.WriteDir, c.EnabledFeatureNames(), c.Testing, c.Health.ReadyValue())
	}, cfg.WithNamespaceSchema(sysNamespace))
}

// sysNamespace describes `sys`. The values are not constant — they describe the
// machine and the invocation — so no value is emitted, only what each member
// means. Every member GetSysObject builds must appear here, and nothing else
// may.
var sysNamespace = cfg.NamespaceSchema{
	Summary: "Process and host identity, and the runtime's own readiness.",
	Doc: "All values are read-only. Identity — the process, the host, the build, the invocation — " +
		"is captured once when the process starts rather than read afresh on each use. " +
		"`sys.ready` is the exception: it is live runtime state, read when something asks " +
		"for it. See [health](health.md).",
	DocPage: "config.md#variables",
	Members: map[string]cfg.MemberMeta{
		"pid":      {Summary: "Process ID of the running process."},
		"hostname": {Summary: "Hostname of the machine."},
		"user":     {Summary: "Username the process is running as."},
		"uid":      {Summary: "Numeric user ID."},
		"group":    {Summary: "Primary group name."},
		"gid":      {Summary: "Numeric primary group ID."},
		"os":       {Summary: "Operating system, e.g. `linux`, `darwin`, `windows`."},
		"arch":     {Summary: "CPU architecture, e.g. `amd64`, `arm64`."},
		"cpus":     {Summary: "Number of logical CPUs available."},
		"version":  {Summary: "Vinculum release version, or `dev` for a local build."},
		"commit":   {Summary: "Git commit the binary was built from, or empty if unknown."},
		"build_time": {
			Summary: "Build timestamp in RFC 3339, or empty if unknown.",
		},
		"modified": {
			Summary: "True if the working tree had uncommitted changes at build time.",
		},
		"functy": {
			Summary: "The bundled [functy](functy.md) (`.cty`) language.",
			Members: map[string]cfg.MemberMeta{
				"version": {
					Summary: "Version of the bundled functy language.",
					Doc: "Read from the binary's build info — `(devel)` in a workspace build, empty if " +
						"unavailable. Only the module version is recorded for a dependency, so functy's " +
						"own commit and build time are not available; `sys.commit` and `sys.build_time` " +
						"describe the Vinculum binary, not functy.",
				},
			},
		},
		"executable": {Summary: "Path to the running executable."},
		"cwd":        {Summary: "Working directory the process started in."},
		"homedir":    {Summary: "Home directory of the current user."},
		"tempdir":    {Summary: "Default directory for temporary files."},
		"ready": {
			Summary: "Whether the process is currently ready to serve traffic.",
			Doc: "Reads as a boolean with `get()` — `get(sys.ready)`, or `get(ctx, sys.ready)` where " +
				"a `ctx` is in scope, which is the better form since it carries the trace parent " +
				"and the caller's deadline. It is also watchable, so a reactive expression naming " +
				"it is re-evaluated when readiness flips.\n\n" +
				"A connected client reports a lost connection the moment it happens, so this goes " +
				"false promptly even with nothing probing. Recovery and `check` blocks are still " +
				"*sampled*: readiness is recomputed only when something asks — an HTTP probe, a " +
				"`health::` call, a metrics scrape — so those are seen at the next such moment. " +
				"Where that matters, poll at a cadence you control:\n\n" +
				"```hcl\n" +
				"trigger \"interval\" \"health_poll\" {\n" +
				"    delay  = \"10s\"\n" +
				"    action = health::refresh(ctx)\n" +
				"}\n" +
				"```\n\n" +
				"See [health](health.md).",
		},
		"testing": {
			Summary: "True when running under `vinculum test`.",
			Doc: "Write `disabled = sys.testing` to switch off real external connections while a " +
				"configuration is under test. See [testing](testing.md).",
		},
		"filepath": {
			Summary: "The `--file-path` directory, or empty if it was not given.",
			Doc: "The base directory the file read and write functions resolve against. See " +
				"[file functions](functions.md#file-functions).",
		},
		"writepath": {
			Summary: "The `--write-path` directory, or empty if it was not given.",
			Doc: "The base directory `filewrite` and `fileappend` resolve against; it must be within " +
				"`sys.filepath`. See [file write functions](functions.md#file-write-functions).",
		},
		"starttime": {
			Summary: "Approximate time the process started.",
			Doc:     "Captured when the process loads. `time::since(sys.starttime)` is process uptime.",
		},
		"boottime": {
			Summary: "Approximate time the host booted.",
			Doc: "Exact on macOS (`kern.boottime`), accurate to about a second on Linux " +
				"(`sysinfo(2)`), and equal to `sys.starttime` on platforms that expose neither. " +
				"`time::since(sys.boottime)` is host uptime.",
		},
		"plugins": {
			Summary: "Names of every registered plugin component.",
			Doc: "For example `[\"ambient.sys\", \"client.kafka\", \"functions.kill\", \"server.mcp\"]`. " +
				"Both in-tree components and those a `.vinit` [plugin](plugins.md) contributed are " +
				"listed, so this is how a configuration tells whether the binary it is running on has " +
				"what it needs.",
		},
		"features": {
			Summary: "Names of the enabled feature flags.",
			Doc: "Each CLI flag that gates an optional capability registers a name: `readfiles` " +
				"(`--file-path`), `writefiles` (`--write-path`), `allowkill` (`--allow-kill`). " +
				"`contains(sys.features, \"allowkill\")` branches on one.",
		},
		"signals": {
			Summary: "Signal numbers for the host OS, by name.",
			Doc: "`sys.signals.SIGUSR1` is the number of `SIGUSR1` on the current OS. Which signals " +
				"exist is OS-dependent — everything the OS enumerates in the range 1–64 is here — so " +
				"use these instead of hardcoding a number that is right on one platform:\n\n" +
				"```hcl\n" +
				"kill(sys.pid, sys.signals.SIGUSR1)   # portable signal reference\n" +
				"```",
			FreeMembers: true,
			Members: map[string]cfg.MemberMeta{
				"bynumber": {
					Summary: "Signal name, keyed by the number as a string.",
					Doc: "`sys.signals.bynumber[\"9\"]` is `\"SIGKILL\"`. HCL coerces an integer key to a " +
						"string, so `sys.signals.bynumber[9]` works too.",
				},
			},
		},
	},
}

// GetSysObject returns a cty object containing process and host identity
// information, suitable for providing to an HCL evaluation context as "sys".
// All values are captured once at config-build time. baseDir is the value of
// the --file-path flag, or empty string if it was not specified.
// writeDir is the value of the --write-path flag, or empty string if not set.
// features is the sorted list of enabled feature flag names.
// testing is true when the config is built for `vinculum test`, projected as
// sys.testing so a config can gate external I/O off under test.
// ready is the runtime's readiness handle, projected as sys.ready — the one
// member that is live state rather than startup identity.
func GetSysObject(baseDir string, writeDir string, features []string, testing bool, ready cty.Value) cty.Value {
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

	// Build identity
	sysMap["version"] = cty.StringVal(version.Version)
	sysMap["commit"] = cty.StringVal(version.Commit)
	sysMap["build_time"] = cty.StringVal(version.BuildTime)
	sysMap["modified"] = cty.BoolVal(version.Modified)

	// Bundled functy (.cty) language version, read from build info. Only the module
	// version is available for a dependency — commit/date are recorded for the main
	// module (above) only, not for imported modules.
	sysMap["functy"] = cty.ObjectVal(map[string]cty.Value{
		"version": cty.StringVal(version.ModuleVersion(functyModulePath)),
	})

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

	// True when running under `vinculum test`; use in `disabled = sys.testing`
	// to switch off real external connections while a config is under test.
	sysMap["testing"] = cty.BoolVal(testing)

	// Readiness of the running process. Unlike everything else here this is a
	// live handle, not a captured value: reading it asks the health aggregator.
	sysMap["ready"] = ready

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
