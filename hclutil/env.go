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
// keeps it; an entry with no later separator, such as "=value", has the
// empty name. When a name appears more than once the first entry wins, as it
// does for os.Getenv. cty keys maps by NFC-normalized strings, so names that
// differ only in Unicode composition count as the same name.
func EnvObjectFrom(environ []string) cty.Value {
	envMap := make(map[string]cty.Value, len(environ))
	for _, envVar := range environ {
		if envVar == "" {
			continue
		}
		var name, value string
		if i := strings.IndexByte(envVar[1:], '='); i >= 0 {
			name, value = envVar[:i+1], envVar[i+2:]
		} else if envVar[0] == '=' {
			name, value = "", envVar[1:]
		} else {
			continue
		}
		name = cty.NormalizeString(name)
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
