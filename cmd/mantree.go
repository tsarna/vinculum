package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/internal/schemadoc"
)

// The command tree as the reference's command corpus: `vinculum man serve`,
// `:man serve`, help("command:serve"), and man::page("serve") all render it.
//
// Registered from here rather than root.go so that everything the reference
// needs to know about a command — which flag enables which feature, which page
// documents it — is found by reading one file's callers.
func init() {
	schemadoc.RegisterCommandTree(commandTree)
}

// commandTree finishes the tree and returns it. schemadoc calls it once, on the
// first lookup, and copies what it needs.
//
// On first use rather than here, because most commands are added by init()
// functions that run after this one. Both steps are ones Execute also takes, and
// both are idempotent, so a lookup sees the same tree in a test binary — which
// never calls Execute — as in `vinculum man`.
func commandTree() *cobra.Command {
	rootCmd.InitDefaultCompletionCmd()
	annotateEnvUsage(rootCmd)
	return rootCmd
}

// enablesFeature records that a flag enables a config feature, which is what
// lets a function's page say "Available only when run with --file-path" and a
// command's page list the functions --file-path makes callable.
//
// A panic rather than an error, because the only failure is naming a flag the
// command does not have, which is a mistake in this package that every test run
// would hit.
func enablesFeature(c *cobra.Command, flag, feature string) {
	if err := c.Flags().SetAnnotation(flag, schemadoc.FlagFeatureAnnotation, []string{feature}); err != nil {
		panic(err)
	}
}

// documentedBy names a command's hand-written page, relative to doc/.
func documentedBy(c *cobra.Command, page string) {
	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	c.Annotations[schemadoc.CommandDocPageAnnotation] = page
}
