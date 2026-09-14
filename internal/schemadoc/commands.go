package schemadoc

import (
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Commands as a third corpus: cobra's own tree.
//
// A config is only half of what it takes to run one. The file functions exist
// only with --file-path, an editor writes only with --write-path, and a plugin
// loads only with --plugin-path — so a reader who has written a config perfectly
// still needs the command line to boot it, and --help is not somewhere an agent
// reading the reference would think to look.
//
// The tree lives in cmd, which imports this package, so it arrives the way
// help()'s resolver leaves: registered from an init(). A program that links this
// package without cmd has no command kind, and every lookup behaves as it did.
//
// Unlike the function corpus, it needs no built Config and is the same for
// every caller, which is why it is searched inside Resolve rather than unioned at
// each front door.

// CommandDocPageAnnotation is the cobra.Command annotation naming the
// hand-written page for a command, relative to doc/ — the analogue of a block's
// DocPage.
const CommandDocPageAnnotation = "vinculum.docpage"

// FlagFeatureAnnotation is the pflag annotation naming the config features a
// flag enables — `readfiles` on --file-path. It is what lets a function's page
// name the flag it needs, and a command's page name the functions a flag turns
// on, without a list of either kept anywhere else.
const FlagFeatureAnnotation = "vinculum.feature"

// commandDoc is one command, copied out of cobra.
//
// Rendering works from this copy rather than from the tree, because cobra's
// accessors are not read-only. Asking a command for its local or inherited flags
// merges its parents' flag sets into its own, and UseLine's "[flags]" depends on
// whether that has happened yet — so reading the live tree from concurrent man::
// calls races, and the same page could render differently depending on what was
// rendered before it. The copy is taken once, with every merge done first.
type commandDoc struct {
	Name string
	// Path is the topic path: the words after the root, or the root's own name.
	Path []string
	// CommandPath is the command as typed, e.g. "vinculum serve".
	CommandPath string
	// UseLine is empty for a command that does not run on its own.
	UseLine   string
	Short     string
	Long      string
	DocPage   string
	Flags     []commandFlag
	Inherited []commandFlag
	// Subs are the commands a reader can run under this one, sorted.
	Subs []*commandDoc
}

// commandFlag is one documented flag, with the features it enables.
type commandFlag struct {
	FlagRow
	Features []string
}

// commandTree returns the copy of the registered tree, or nil.
var commandTree func() *commandDoc

// RegisterCommandTree registers the CLI's command tree as the command corpus.
//
// It takes a function rather than the root so that registering can happen from
// an init() that runs before every command has been added. The function is
// called once, on the first lookup, and the tree is copied then; later changes
// to the tree are not seen.
func RegisterCommandTree(tree func() *cobra.Command) {
	commandTree = sync.OnceValue(func() *commandDoc {
		root := tree()
		if root == nil {
			return nil
		}
		return copyCommand(root)
	})
}

func commandRoot() *commandDoc {
	if commandTree == nil {
		return nil
	}
	return commandTree()
}

// copyCommand copies c and every command a reader can run under it: not hidden,
// not deprecated, and not cobra's generated `help`, which documents --help
// rather than anything a config needs and would make `help` ambiguous with the
// help() function for nothing.
func copyCommand(c *cobra.Command) *commandDoc {
	// Flags first: asking for them is what merges the parents' persistent flags
	// into this command, and UseLine reads the merged set.
	d := &commandDoc{
		Flags:     copyFlags(c.LocalFlags()),
		Inherited: copyFlags(c.InheritedFlags()),
	}
	d.Name = c.Name()
	d.Path = commandPath(c)
	d.CommandPath = c.CommandPath()
	if c.Runnable() {
		d.UseLine = c.UseLine()
	}
	d.Short = c.Short
	d.Long = c.Long
	d.DocPage = c.Annotations[CommandDocPageAnnotation]

	for _, sub := range c.Commands() {
		if sub.IsAvailableCommand() {
			d.Subs = append(d.Subs, copyCommand(sub))
		}
	}
	sort.Slice(d.Subs, func(i, j int) bool { return d.Subs[i].Name < d.Subs[j].Name })
	return d
}

// commandPath is the path that names a command: its words after the root, so
// `serve` rather than `vinculum serve`, and the root's own name for the root.
func commandPath(c *cobra.Command) []string {
	if !c.HasParent() {
		return []string{c.Name()}
	}
	var path []string
	for ; c.HasParent(); c = c.Parent() {
		path = append([]string{c.Name()}, path...)
	}
	return path
}

// copyFlags copies the flags a page lists: not hidden, not deprecated, and not
// cobra's own --help, which every command has and no reader needs told.
func copyFlags(flags *pflag.FlagSet) []commandFlag {
	var out []commandFlag
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Deprecated != "" || f.Name == "help" {
			return
		}
		typ, usage := pflag.UnquoteUsage(f)
		out = append(out, commandFlag{
			FlagRow: FlagRow{
				Name:      f.Name,
				Shorthand: f.Shorthand,
				Type:      typ,
				Default:   flagDefault(f),
				Usage:     usage,
			},
			Features: f.Annotations[FlagFeatureAnnotation],
		})
	})
	return out
}

// flagDefault is the default worth stating: none for a zero value, which is
// what --help omits too, since "default false" on every switch says nothing.
func flagDefault(f *pflag.Flag) string {
	switch f.DefValue {
	case "", "false", "0", "[]":
		return ""
	}
	return f.DefValue
}

// each calls fn for d and every command under it, parents first.
func (d *commandDoc) each(fn func(*commandDoc)) {
	fn(d)
	for _, sub := range d.Subs {
		sub.each(fn)
	}
}

func (d *commandDoc) sub(name string) *commandDoc {
	for _, s := range d.Subs {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func commandNode(d *commandDoc) Node {
	return Node{Kind: KindCommand, Path: d.Path, shape: shapeCommand, cmd: d}
}

// resolveCommand resolves a path in the command tree.
//
// The root's name may lead the path, so `vinculum serve` — the way the command is
// typed, and the way an agent will write it — names the same page as `serve`.
func resolveCommand(path []string) []Node {
	root := commandRoot()
	if root == nil || len(path) == 0 {
		return nil
	}
	rest := path
	if rest[0] == root.Name {
		rest = rest[1:]
	}
	d := root
	for _, name := range rest {
		if d = d.sub(name); d == nil {
			return nil
		}
	}
	return []Node{commandNode(d)}
}

// commandTopics returns the top-level commands, for the index.
func commandTopics() []Node {
	root := commandRoot()
	if root == nil {
		return nil
	}
	out := make([]Node, 0, len(root.Subs))
	for _, sub := range root.Subs {
		out = append(out, commandNode(sub))
	}
	return out
}

// commandLeadingNames returns every name that can begin a command path.
func commandLeadingNames(add func(string)) {
	root := commandRoot()
	if root == nil {
		return
	}
	add(root.Name)
	for _, sub := range root.Subs {
		add(sub.Name)
	}
}

// commandSynopsis is how the command is invoked: its use line if it runs, and
// the form that takes a subcommand if it has any.
func commandSynopsis(d *commandDoc) Synopsis {
	var lines []string
	if d.UseLine != "" {
		lines = append(lines, d.UseLine)
	}
	if len(d.Subs) > 0 {
		lines = append(lines, d.CommandPath+" <command>")
	}
	return Synopsis{Lines: lines, Lang: "sh"}
}

// walkCommand renders one command: how it is invoked, what it does, its own
// flags, the flags it inherits, and the commands under it.
func (w *walker) walkCommand(n Node, level int) {
	d := n.cmd
	if syn, ok := synopsisOf(n); ok {
		w.emit(syn)
	}
	// cobra's Long conventionally opens by restating Short, and printing both
	// says the same thing twice. Whole words only: a Short of "Check" says
	// something a Long opening "Checks the …" does not.
	summary := d.Short
	if restates(d.Long, d.Short) {
		summary = ""
	}
	w.describe(summary, n.Description(), false)

	if len(d.Flags) > 0 {
		w.emit(Heading{Level: level + 1, Text: "Flags"})
		w.emit(FlagTable{Rows: flagRows(d.Flags)})
		w.emitFeatureFlags(d.Flags)
	}
	if len(d.Inherited) > 0 {
		w.emit(Heading{Level: level + 1, Text: "Global flags"})
		w.emit(FlagTable{Rows: flagRows(d.Inherited)})
	}

	if len(d.Subs) > 0 {
		rows := make([]BlockRow, 0, len(d.Subs))
		for _, sub := range d.Subs {
			rows = append(rows, BlockRow{Name: sub.Name, Summary: sub.Short, Path: sub.Path})
		}
		w.emit(Heading{Level: level + 1, Text: "Commands"})
		w.emit(BlockTable{Rows: rows})
	}
}

// restates reports whether long opens with short, ending at a word boundary.
func restates(long, short string) bool {
	long, short = strings.TrimSpace(long), strings.TrimSpace(short)
	if short == "" || !strings.HasPrefix(long, short) {
		return false
	}
	rest := long[len(short):]
	return rest == "" || !isWordByte(rest[0])
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func flagRows(flags []commandFlag) []FlagRow {
	rows := make([]FlagRow, len(flags))
	for i, f := range flags {
		rows[i] = f.FlagRow
	}
	return rows
}

// emitFeatureFlags names, under the flag table, the functions each
// feature-enabling flag makes callable — the half of the link a reader of the
// command page needs, as the note on a function's page is the other half.
func (w *walker) emitFeatureFlags(flags []commandFlag) {
	for _, f := range flags {
		if len(f.Features) == 0 {
			continue
		}
		names := functionsNeeding(BuiltinFuncs(), f.Features)
		if len(names) == 0 {
			continue
		}
		for i, name := range names {
			names[i] = "`" + name + "()`"
		}
		w.emit(Note{Text: "Functions available only with `--" + f.Name + "`: " + strings.Join(names, ", ") + "."})
	}
}

// functionsNeeding returns the functions in cat that need any of features.
func functionsNeeding(cat FuncCatalog, features []string) []string {
	if cat == nil {
		return nil
	}
	var out []string
	for _, name := range cat.FuncNames() {
		doc, ok := cat.FuncDoc(name)
		if !ok {
			continue
		}
		if intersects(doc.Features, features) {
			out = append(out, name)
		}
	}
	return out
}

func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// featureNote is the sentence on a function's page saying what it takes for the
// function to exist, naming the flag and the commands that accept it. With no
// command tree to consult it names the feature, which is still true, if less
// useful.
func featureNote(features []string) string {
	parts := make([]string, 0, len(features))
	for _, feature := range features {
		parts = append(parts, featureSpelling(feature))
	}
	return "Available only when run with " + strings.Join(parts, " and ") + "."
}

// featureSpelling names the flags that enable feature, each with the commands
// that take it. One flag is the normal case; should two commands ever spell it
// differently, both are named rather than one silently winning.
func featureSpelling(feature string) string {
	var flags []string
	commands := map[string][]string{}
	if root := commandRoot(); root != nil {
		root.each(func(d *commandDoc) {
			for _, f := range d.Flags {
				if !intersects(f.Features, []string{feature}) {
					continue
				}
				if _, seen := commands[f.Name]; !seen {
					flags = append(flags, f.Name)
				}
				commands[f.Name] = append(commands[f.Name], "`"+d.CommandPath+"`")
			}
		})
	}
	if len(flags) == 0 {
		return "the `" + feature + "` feature enabled"
	}
	parts := make([]string, 0, len(flags))
	for _, name := range flags {
		sort.Strings(commands[name])
		parts = append(parts, "`--"+name+"` ("+strings.Join(commands[name], ", ")+")")
	}
	return strings.Join(parts, " or ")
}

// commandDescription renders a command's Long text as Markdown.
//
// cobra's Long is written for a terminal: prose, and examples laid out as
// indented, column-aligned lines — often straight under an unindented
// "Examples:". Read as Markdown, a two-space indent is not a code block, so those
// examples would be reflowed into a paragraph and their columns lost. Each run of
// indented lines is therefore fenced, and the rest is left as prose.
func commandDescription(long string) string {
	lines := strings.Split(strings.Trim(long, "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); {
		if !isIndentedLine(lines[i]) {
			out = append(out, lines[i])
			i++
			continue
		}
		// A blank line belongs to the run only when the run continues after it.
		j := i
		for j < len(lines) && (isIndentedLine(lines[j]) ||
			strings.TrimSpace(lines[j]) == "" && j+1 < len(lines) && isIndentedLine(lines[j+1])) {
			j++
		}
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, "```text")
		out = append(out, dedentCommon(lines[i:j])...)
		out = append(out, "```")
		if j < len(lines) && strings.TrimSpace(lines[j]) != "" {
			out = append(out, "")
		}
		i = j
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isIndentedLine(l string) bool {
	return strings.TrimSpace(l) != "" && (l[0] == ' ' || l[0] == '\t')
}

// dedentCommon removes the indent the lines share, keeping their alignment.
func dedentCommon(lines []string) []string {
	common := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if common < 0 || n < common {
			common = n
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			out[i] = l[common:]
		}
	}
	return out
}

// commands searches every command, and every flag of each, for Apropos. A flag
// is not addressable on its own, so its hit names the command and says which
// flag matched — which is how `-k file-path` leads to the commands that take it.
func (s *search) commands() {
	root := commandRoot()
	if root == nil {
		return
	}
	root.each(func(d *commandDoc) {
		s.consider(KindCommand, d.Path, "", d.Name, d.Short)
		for _, f := range d.Flags {
			s.consider(KindCommand, d.Path, "--"+f.Name, f.Name, f.Usage)
		}
	})
}
