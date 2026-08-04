package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/internal/pager"
	"github.com/tsarna/vinculum/internal/schemadoc"
)

var (
	manType     string
	manNoPager  bool
	manConfigs  []string
	manExamples = []string{
		"vinculum man subscription",
		"vinculum man client mqtt",
		"vinculum man server http handle",
	}
)

var manCmd = &cobra.Command{
	Use:   "man [topic ...]",
	Short: "Show reference documentation for the configuration language",
	Long: `Show reference documentation for one part of the VCL configuration language.

A topic is a path through the language: a block type, then a type label for the
blocks that take one, then an attribute or sub-block, to any depth.

  vinculum man var                    the var block
  vinculum man client                 the client block, listing its types
  vinculum man client mqtt            client "mqtt" in full
  vinculum man client mqtt tls        one sub-block of it
  vinculum man subscription action    one attribute, with the ctx it sees

A type label resolves on its own where it is unambiguous, so "vinculum man mqtt"
is the same page. Where it is not — http and vws are each both a client type and
a server type — the ambiguity is reported with the exact commands that resolve
it. Use --type to choose between kinds of topic rather than paths.

With no topic, lists what there is to read.

The documentation is generated from the same decode structs the parser uses, so
it describes exactly what this binary can parse. Give --config together with
--plugin-path to load plugins first and document the block types they add.

Output is paged when it is going to a terminal; see VINCULUM_PAGER and PAGER.`,
	Args:              cobra.ArbitraryArgs,
	RunE:              runMan,
	ValidArgsFunction: completeManTopic,
}

func init() {
	rootCmd.AddCommand(manCmd)

	manCmd.Flags().StringVar(&manType, "type", "", "restrict the search to one kind of topic (block, context)")
	manCmd.Flags().BoolVar(&manNoPager, "no-pager", false, "write to stdout instead of invoking a pager")
	manCmd.Flags().StringArrayVarP(&manConfigs, "config", "c", nil, "config path to search for .vinit plugin blocks (with --plugin-path)")
	manCmd.Flags().StringVar(&pluginPath, "plugin-path", "", "directory of Go plugin .so files; load the plugins declared by --config so their block types are described too")
}

func runMan(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	kind := schemadoc.Kind(manType)
	if manType != "" && !schemadoc.ValidKind(manType) {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("unknown --type %q (want one of %s)", manType, kindList())}
	}
	if pluginPath != "" && len(manConfigs) == 0 {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("--plugin-path needs --config paths to search for .vinit plugin blocks")}
	}
	if pluginPath == "" && len(manConfigs) > 0 {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("--config is only used to find plugins to load; pass --plugin-path as well")}
	}
	if _, err := loadSchemaPlugins(cmd, manConfigs); err != nil {
		return err
	}

	// Curation problems are the schema command's business, not a reader's:
	// `vinculum schema --strict` reports them and CI fails on them. Rendering
	// what is there is more useful here than refusing to.
	doc, _ := config.GenerateSchema(config.SchemaGenOptions{})

	if len(args) == 0 {
		events := schemadoc.Index(doc, schemadoc.WalkOptions{})
		events = append(events, schemadoc.Usage(manExamples...))
		return page(cmd, events)
	}

	candidates := schemadoc.Resolve(doc, kind, args)
	switch len(candidates) {
	case 1:
		return page(cmd, schemadoc.Walk(candidates[0], schemadoc.WalkOptions{}))
	case 0:
		return notFound(cmd, doc, kind, args)
	default:
		// The menu goes to stderr so that redirecting the output of a lookup
		// that turned out to be ambiguous does not write a menu into the file.
		render(cmd.ErrOrStderr(), []schemadoc.Event{
			schemadoc.MenuFor(args, candidates, schemadoc.CommandSpeller),
		})
		return &ExitCodeError{
			Code:     1,
			Err:      fmt.Errorf("%q is ambiguous", strings.Join(args, " ")),
			Reported: true,
		}
	}
}

// notFound reports a topic that does not exist, with near misses when there
// are any.
func notFound(cmd *cobra.Command, doc *config.SchemaDocument, kind schemadoc.Kind, args []string) error {
	query := strings.Join(args, " ")
	fmt.Fprintf(cmd.ErrOrStderr(), "no topic named %q\n\n", query)

	if near := schemadoc.Suggest(doc, kind, args); len(near) > 0 {
		render(cmd.ErrOrStderr(), []schemadoc.Event{
			schemadoc.SuggestMenu(near, schemadoc.CommandSpeller),
		})
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "Run `vinculum man` for a list of topics.\n")
	}
	return &ExitCodeError{
		Code:     1,
		Err:      fmt.Errorf("no topic named %q", query),
		Reported: true,
	}
}

// page renders events and sends them to the pager, or to stdout when output is
// not going to a terminal.
func page(cmd *cobra.Command, events []schemadoc.Event) error {
	text := schemadoc.RenderMarkdown(events, schemadoc.MarkdownOptions{})
	return pager.Page(cmd.OutOrStdout(), text, pager.Options{Disabled: manNoPager})
}

// render writes events straight to w, unpaged — for the diagnostics that
// accompany a failed lookup.
func render(w io.Writer, events []schemadoc.Event) {
	fmt.Fprintln(w, schemadoc.RenderMarkdown(events, schemadoc.MarkdownOptions{}))
}

// completeManTopic completes a topic path against the same names the resolver
// searches, so nothing it offers can then fail to resolve.
func completeManTopic(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	doc, _ := config.GenerateSchema(config.SchemaGenOptions{})
	kind := schemadoc.Kind(manType)

	if len(args) == 0 {
		return schemadoc.LeadingNames(doc, kind), cobra.ShellCompDirectiveNoFileComp
	}
	return schemadoc.Members(doc, kind, args), cobra.ShellCompDirectiveNoFileComp
}

func kindList() string {
	names := make([]string, 0, len(schemadoc.Kinds))
	for _, k := range schemadoc.Kinds {
		names = append(names, string(k))
	}
	return strings.Join(names, ", ")
}
