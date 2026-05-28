package hclutil

import (
	"os"
	"strings"

	"github.com/zclconf/go-cty/cty"
)

// EnvObject returns a cty object whose attributes are the current
// process's environment variables. Variable names are sanitized to be
// valid HCL attribute names: any character not in [A-Za-z0-9_-] (or
// not in [A-Za-z_] for the first character) is replaced with an
// underscore. An empty name becomes "_". An empty environment returns
// cty.EmptyObjectVal.
func EnvObject() cty.Value {
	envMap := make(map[string]cty.Value)
	for _, envVar := range os.Environ() {
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) != 2 {
			continue
		}
		envMap[sanitizeEnvVarName(parts[0])] = cty.StringVal(parts[1])
	}
	if len(envMap) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(envMap)
}

// sanitizeEnvVarName converts an environment variable name into a valid
// HCL attribute name. HCL attribute names start with a letter or
// underscore and contain only letters, digits, underscores, and hyphens.
func sanitizeEnvVarName(name string) string {
	if name == "" {
		return "_"
	}
	var b strings.Builder
	for i, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		if i > 0 {
			valid = valid || (r >= '0' && r <= '9') || r == '-'
		}
		if valid {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
