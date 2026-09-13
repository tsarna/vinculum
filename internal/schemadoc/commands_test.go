package schemadoc

import (
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
)

// commandTreeTestMu serializes the tests that replace the process-global
// command tree, so one test's tree is never read by another.
var commandTreeTestMu sync.Mutex

// withCommandTree registers root as the command corpus for the duration of a
// test, and restores whatever was registered before.
func withCommandTree(t *testing.T, root *cobra.Command) {
	t.Helper()
	commandTreeTestMu.Lock()
	saved := commandTree
	if root == nil {
		commandTree = nil
	} else {
		RegisterCommandTree(func() *cobra.Command { return root })
	}
	t.Cleanup(func() {
		commandTree = saved
		commandTreeTestMu.Unlock()
	})
}

// testCommands builds a small tree shaped like the real one: a root with a
// global flag, a command whose name is also a block type in testDoc (the `check`
// collision is real), a command with a feature-enabling flag, one with
// subcommands, and a hidden one that must never be documented.
func testCommands() *cobra.Command {
	root := &cobra.Command{Use: "vinculum", Short: "Vinculum event server"}
	root.PersistentFlags().BoolP("verbose", "v", false, "verbose output")

	serve := &cobra.Command{
		Use:   "serve [config-files-or-directories...]",
		Short: "Run a configuration",
		Long: "Run the configuration and serve until stopped.\n\n" +
			"  vinculum serve app.vcl        one file\n" +
			"  vinculum serve conf/          a directory",
		Annotations: map[string]string{CommandDocPageAnnotation: "config.md"},
		Run:         func(*cobra.Command, []string) {},
	}
	serve.Flags().StringP("file-path", "f", "", "base directory for file functions")
	_ = serve.Flags().SetAnnotation("file-path", FlagFeatureAnnotation, []string{"readfiles"})
	serve.Flags().String("format", "auto", "output format")

	subscription := &cobra.Command{
		Use:   "subscription",
		Short: "Clashes with the subscription block",
		Run:   func(*cobra.Command, []string) {},
	}

	plugins := &cobra.Command{Use: "plugins", Short: "Inspect plugins"}
	plugins.AddCommand(&cobra.Command{Use: "list", Short: "List loaded plugins", Run: func(*cobra.Command, []string) {}})

	hidden := &cobra.Command{Use: "secret", Short: "Not for readers", Hidden: true, Run: func(*cobra.Command, []string) {}}

	root.AddCommand(serve, subscription, plugins, hidden)
	return root
}

// gatedCatalog is a catalog holding one function behind the readfiles feature
// and one that always exists.
type gatedCatalog struct{}

func (gatedCatalog) FuncNames() []string { return []string{"file", "upper"} }
func (gatedCatalog) FuncDoc(name string) (config.FuncDoc, bool) {
	switch name {
	case "file":
		return config.FuncDoc{Name: "file", Signatures: []string{"file(path)"}, Doc: "Reads a file.", Features: []string{"readfiles"}}, true
	case "upper":
		return config.FuncDoc{Name: "upper", Signatures: []string{"upper(s)"}, Doc: "Upper-cases."}, true
	}
	return config.FuncDoc{}, false
}
func (gatedCatalog) FuncNameCandidates(string) []string { return nil }

// withBuiltinFuncs substitutes the catalog BuiltinFuncs returns.
func withBuiltinFuncs(t *testing.T, cat FuncCatalog) {
	t.Helper()
	saved := builtinFuncs
	builtinFuncs = func() FuncCatalog { return cat }
	t.Cleanup(func() { builtinFuncs = saved })
}

func TestResolveCommand(t *testing.T) {
	withCommandTree(t, testCommands())

	got := Resolve(testDoc(), "", []string{"serve"})
	require.Len(t, got, 1)
	assert.Equal(t, KindCommand, got[0].Kind)
	assert.Equal(t, []string{"serve"}, got[0].Path)

	// The way a command is typed names the same page.
	got = Resolve(testDoc(), "", []string{"vinculum", "serve"})
	require.Len(t, got, 1)
	assert.Equal(t, []string{"serve"}, got[0].Path)

	got = Resolve(testDoc(), "", []string{"plugins", "list"})
	require.Len(t, got, 1)
	assert.Equal(t, []string{"plugins", "list"}, got[0].Path)

	root := Resolve(testDoc(), "", []string{"vinculum"})
	require.Len(t, root, 1)
	assert.Equal(t, []string{"vinculum"}, root[0].Path)

	assert.Empty(t, Resolve(testDoc(), "", []string{"secret"}), "a hidden command is not a topic")
	assert.Empty(t, Resolve(testDoc(), "", []string{"serve", "nope"}))
	assert.Empty(t, Resolve(testDoc(), KindBlock, []string{"serve"}), "--type restricts commands out")

	// No document is still a command lookup.
	assert.Len(t, Resolve(nil, "", []string{"serve"}), 1)
}

func TestResolveCommandWithNoTreeRegistered(t *testing.T) {
	withCommandTree(t, nil)
	assert.Empty(t, Resolve(testDoc(), "", []string{"serve"}))
	assert.Empty(t, Topics(nil, KindCommand))
}

// A command named like a block is the `check` case, and it is answered the way
// `assert` is: a menu that names each kind.
func TestCommandAmbiguityWithABlock(t *testing.T) {
	withCommandTree(t, testCommands())

	got := Resolve(testDoc(), "", []string{"subscription"})
	require.Len(t, got, 2)

	menu := MenuFor([]string{"subscription"}, got, PathSpeller)
	assert.Equal(t, []string{"block:subscription", "command:subscription"}, menu.Items)

	cmdMenu := MenuFor([]string{"subscription"}, got, CommandSpeller)
	assert.Contains(t, cmdMenu.Items, "vinculum man --type command subscription")
}

func TestCommandLeadingNamesMembersAndSuggest(t *testing.T) {
	withCommandTree(t, testCommands())

	names := LeadingNames(testDoc(), KindCommand)
	assert.Equal(t, []string{"plugins", "serve", "subscription", "vinculum"}, names)
	assert.Contains(t, LeadingNames(testDoc(), ""), "serve")
	assert.NotContains(t, LeadingNames(testDoc(), KindBlock), "serve")

	assert.Equal(t, []string{"list"}, Members(testDoc(), KindCommand, []string{"plugins"}))
	assert.Equal(t, []string{"plugins", "serve", "subscription"}, Members(testDoc(), KindCommand, []string{"vinculum"}))

	near := Suggest(testDoc(), "", []string{"serv"})
	require.NotEmpty(t, near)
	assert.Equal(t, []string{"serve"}, near[0].Path)
}

func TestWalkCommand(t *testing.T) {
	withCommandTree(t, testCommands())
	withBuiltinFuncs(t, gatedCatalog{})

	serve := Resolve(testDoc(), KindCommand, []string{"serve"})[0]
	got := renderNode(serve, WalkOptions{})

	assert.Contains(t, got, "# `vinculum serve`")
	assert.Contains(t, got, "```sh\nvinculum serve [config-files-or-directories...] [flags]\n```")
	assert.Contains(t, got, "Run a configuration")
	// The column-aligned examples in Long survive as a code block.
	assert.Contains(t, got, "```text\nvinculum serve app.vcl        one file\nvinculum serve conf/          a directory\n```")

	assert.Contains(t, got, "## Flags")
	assert.Contains(t, got, "| Flag | Type | Default | Description |")
	assert.Contains(t, got, "| `-f, --file-path` | string |  | base directory for file functions |")
	assert.Contains(t, got, "| `--format` | string | `auto` | output format |")
	assert.Contains(t, got, "Functions available only with `--file-path`: `file()`.",
		"the flag names the functions it enables, from the catalog")
	assert.NotContains(t, got, "`upper()`")

	assert.Contains(t, got, "## Global flags")
	assert.Contains(t, got, "`-v, --verbose`")
	assert.NotContains(t, got, "--help")

	assert.Contains(t, got, "## See also")
	assert.Contains(t, got, "config.md")
}

func TestWalkCommandWithSubcommands(t *testing.T) {
	withCommandTree(t, testCommands())

	root := Resolve(testDoc(), KindCommand, []string{"vinculum"})[0]
	got := renderNode(root, WalkOptions{})
	assert.Contains(t, got, "```sh\nvinculum <command>\n```", "the root does not run, so it has only the subcommand form")
	assert.Contains(t, got, "## Flags", "the root's persistent flags are its own")
	assert.NotContains(t, got, "## Global flags")
	assert.Contains(t, got, "## Commands")
	assert.Contains(t, got, "- `serve` — Run a configuration")
	assert.NotContains(t, got, "secret")

	syn, err := WalkSection(root, SectionSynopsis, WalkOptions{})
	require.NoError(t, err)
	assert.Len(t, syn, 1)
}

// A command with no flags of its own inherits the root's. Whether cobra's use
// line says "[flags]" depends on whether those have been merged in yet, so the
// first thing rendered in a process must agree with everything after it.
func TestCommandSynopsisDoesNotDependOnRenderOrder(t *testing.T) {
	withCommandTree(t, testCommands())

	list := Resolve(nil, KindCommand, []string{"plugins", "list"})
	require.Len(t, list, 1)
	first, err := WalkSection(list[0], SectionSynopsis, WalkOptions{})
	require.NoError(t, err)

	page := renderNode(list[0], WalkOptions{})
	again, err := WalkSection(list[0], SectionSynopsis, WalkOptions{})
	require.NoError(t, err)

	assert.Equal(t, first, again)
	assert.Equal(t, []string{"vinculum plugins list [flags]"}, first[0].(Synopsis).Lines)
	assert.Contains(t, page, "vinculum plugins list [flags]")
}

// man:: runs on whatever goroutine calls it — concurrent MCP requests, in the
// man-site example — so the command corpus must be safe to read in parallel,
// from a cold start. Run with -race to mean anything.
func TestCommandCorpusIsSafeConcurrently(t *testing.T) {
	withCommandTree(t, testCommands())
	withBuiltinFuncs(t, gatedCatalog{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, topic := range []string{"serve", "vinculum", "plugins list", "command:subscription"} {
				v, err := callPage(t, nil, topic)
				assert.NoError(t, err)
				assert.False(t, v.IsNull(), topic)
			}
			_ = renderNode(FuncNode(gatedCatalog{}, "file"), WalkOptions{})
			_ = Apropos(testDoc(), nil, "", []string{"file-path"})
		}()
	}
	wg.Wait()
}

func TestWalkCommandTerm(t *testing.T) {
	withCommandTree(t, testCommands())
	withBuiltinFuncs(t, nil)

	serve := Resolve(testDoc(), KindCommand, []string{"serve"})[0]
	got := RenderPlain(Walk(serve, WalkOptions{}), 100)
	assert.Contains(t, got, "-f, --file-path string")
	assert.Contains(t, got, "output format (default")
}

func TestFunctionPageNamesTheFlagItNeeds(t *testing.T) {
	withCommandTree(t, testCommands())

	got := renderNode(FuncNode(gatedCatalog{}, "file"), WalkOptions{})
	assert.Contains(t, got, "*Available only when run with `--file-path` (`vinculum serve`).*")
	// Before the prose, where a reader deciding whether to call it sees it.
	assert.Less(t, strings.Index(got, "Available only"), strings.Index(got, "Reads a file."))

	assert.NotContains(t, renderNode(FuncNode(gatedCatalog{}, "upper"), WalkOptions{}), "Available only")
}

func TestFunctionPageNamesTheFeatureWithNoTree(t *testing.T) {
	withCommandTree(t, nil)
	got := renderNode(FuncNode(gatedCatalog{}, "file"), WalkOptions{})
	assert.Contains(t, got, "Available only when run with the `readfiles` feature enabled.")
}

func TestAproposFindsCommandsAndFlags(t *testing.T) {
	withCommandTree(t, testCommands())

	hits := Apropos(testDoc(), nil, "", []string{"file-path"})
	require.Len(t, hits, 1)
	assert.Equal(t, KindCommand, hits[0].Kind)
	assert.Equal(t, []string{"serve"}, hits[0].Path)
	assert.Equal(t, "--file-path", hits[0].Detail)

	hits = Apropos(testDoc(), nil, KindCommand, []string{"plugins"})
	require.NotEmpty(t, hits)
	assert.Equal(t, []string{"plugins"}, hits[0].Path, "an exact name first")

	assert.Empty(t, Apropos(testDoc(), nil, KindCommand, []string{"secret"}))

	// Only the command matches, but `subscription` is also a block: the row has
	// to carry its kind, or the invocation it prints reads a menu instead.
	hits = Apropos(testDoc(), nil, "", []string{"clashes"})
	require.Len(t, hits, 1)
	rows := ResultsFor([]string{"clashes"}, hits, PathSpeller).Rows
	assert.Equal(t, "command:subscription", rows[0].Command)

	// And a search restricted to one kind still qualifies against the others.
	hits = Apropos(testDoc(), nil, KindBlock, []string{"subscribes"})
	require.NotEmpty(t, hits)
	rows = ResultsFor([]string{"subscribes"}, hits, PathSpeller).Rows
	assert.Equal(t, "block:subscription", rows[0].Command)
	assert.Empty(t, Apropos(testDoc(), nil, KindBlock, []string{"file-path"}))
}

func TestIndexListsCommandsButEverythingDoesNot(t *testing.T) {
	withCommandTree(t, testCommands())

	index := RenderMarkdown(Index(testDoc(), WalkOptions{}), MarkdownOptions{})
	assert.Contains(t, index, "## Commands")
	assert.Contains(t, index, "- `serve` — Run a configuration")
	assert.NotContains(t, index, "- `vinculum`", "the root is not a row; it is the page the rows are under")

	everything := RenderMarkdown(Everything(testDoc(), WalkOptions{}), MarkdownOptions{})
	assert.NotContains(t, everything, "## Commands")
}

func TestHelpTopicResolvesACommand(t *testing.T) {
	withCommandTree(t, testCommands())

	got, ok := helpResolver{}.HelpTopic("command", []string{"serve"})
	require.True(t, ok)
	assert.Contains(t, got, "vinculum serve")
	assert.Contains(t, got, "--file-path")
}

func TestManPageResolvesACommand(t *testing.T) {
	withCommandTree(t, testCommands())

	page := func(topic string) string {
		t.Helper()
		v, err := callPage(t, nil, topic)
		require.NoError(t, err)
		require.False(t, v.IsNull(), "%q resolved to nothing", topic)
		return v.AsString()
	}

	assert.Contains(t, page("vinculum serve"), "# `vinculum serve`")
	assert.Contains(t, page("subscription"), "command:subscription", "a block and a command get the menu")
	assert.Contains(t, page("command:subscription"), "Clashes with the subscription block")
}

func TestRestates(t *testing.T) {
	assert.True(t, restates("Start the server with these files.", "Start the server"))
	assert.True(t, restates("Start the server", "Start the server"))
	assert.False(t, restates("Checks the config.", "Check"), "a word boundary, not a prefix")
	assert.False(t, restates("Anything", ""))
	assert.False(t, restates("Run it.", "Start it"))
}

func TestCommandDescription(t *testing.T) {
	assert.Equal(t, "", commandDescription(""))
	assert.Equal(t, "One.\n\nTwo.", commandDescription("One.\n\nTwo.\n"))
	assert.Equal(t, "Intro:\n\n```text\na  one\n  b  nested\n```",
		commandDescription("Intro:\n\n  a  one\n    b  nested"))
	// cobra's own idiom: the examples straight under their label, then more prose.
	assert.Equal(t, "Examples:\n\n```text\nx one\n\nx two\n```\n\nAfter.",
		commandDescription("Examples:\n  x one\n\n  x two\nAfter."))
	assert.Equal(t, "Trailing:\n\n```text\nx\n```", commandDescription("Trailing:\n  x\n\n"))
}
