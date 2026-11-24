package ambient

import (
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterAmbientProvider("env", func(c *cfg.Config) cty.Value {
		return hclutil.EnvObjectFrom(c.Environ())
	}, cfg.WithNamespaceSchema(envNamespace))
}

// envNamespace describes `env`. Its members are free because they are the
// environment of whichever process is running: enumerating them would describe
// the machine that generated the document, and would report a `vinculum check`
// on a build machine as broken for naming a variable only the deployment sets.
var envNamespace = cfg.NamespaceSchema{
	Summary: "Environment variables of the running process.",
	Doc: "`env.HOME` is the value of the `HOME` environment variable. Only variables that are " +
		"actually set are present, so reading an unset one is an error — write " +
		"`try(env.PORT, \"8080\")` or `lookup(env, \"PORT\", \"8080\")` for a fallback. `env` is a " +
		"map keyed by the variables' real names, so a name that is not a valid identifier is read " +
		"by index: `env[\"A.B\"]`.",
	DocPage:     "config.md#variables",
	FreeMembers: true,
}
