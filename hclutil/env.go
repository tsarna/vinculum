package hclutil

import (
	"os"
	"strings"

	"github.com/zclconf/go-cty/cty"
)

// EnvObject returns a cty map of strings holding the current process's
// environment variables, keyed by their real names. Names are not
// sanitized: `env.HOME` reads a valid identifier with attribute syntax, and
// `env["A.B"]` reaches any other. An empty environment returns an empty map.
func EnvObject() cty.Value {
	return EnvObjectFrom(os.Environ())
}

// EnvObjectFrom is EnvObject over the given environment, in os.Environ's
// KEY=value form, rather than the process's.
//
// The separator is searched for from the second byte, so a name that begins
// with "=" — Windows lists per-drive working directories as "=C:=C:\dir" —
// keeps it. When a name appears more than once the first entry wins, as it
// does for os.Getenv.
func EnvObjectFrom(environ []string) cty.Value {
	envMap := make(map[string]cty.Value)
	for _, envVar := range environ {
		if envVar == "" {
			continue
		}
		i := strings.IndexByte(envVar[1:], '=')
		if i < 0 {
			continue
		}
		name, value := envVar[:i+1], envVar[i+2:]
		if _, seen := envMap[name]; seen {
			continue
		}
		envMap[name] = cty.StringVal(value)
	}
	if len(envMap) == 0 {
		return cty.MapValEmpty(cty.String)
	}
	return cty.MapVal(envMap)
}
