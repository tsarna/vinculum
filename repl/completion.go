package repl

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// historyFilePath returns where readline should persist command history:
// $XDG_STATE_HOME/vinculum/history when XDG_STATE_HOME is set, else
// ~/.vinculum_history. Returns "" (history disabled) if no location is usable.
func historyFilePath() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		dir := filepath.Join(x, "vinculum")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			return filepath.Join(dir, "history")
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".vinculum_history")
	}
	return ""
}

// metaCommands are the completion candidates offered when the word begins ':'.
var metaCommands = []string{
	":help", ":quit", ":exit", ":set", ":unset", ":vars",
	":loglevel", ":quiet", ":logs",
}

// completer implements readline.AutoCompleter, completing the word under the
// cursor against meta-commands, built-in top-level names, function names, and
// session bindings. Nested attribute completion (bus., object fields) is out of
// scope (see Future Work).
type completer struct{ s *Session }

// Do returns the candidate suffixes for the current word plus the length of the
// already-typed prefix, per readline's AutoCompleter contract.
func (c *completer) Do(line []rune, pos int) ([][]rune, int) {
	start := pos
	for start > 0 && isWordRune(line[start-1]) {
		start--
	}
	// Don't attempt nested-attribute completion (e.g. after "bus.").
	if start > 0 && line[start-1] == '.' {
		return nil, 0
	}

	word := string(line[start:pos])
	cands := c.s.completions(word)

	out := make([][]rune, 0, len(cands))
	for _, cand := range cands {
		out = append(out, []rune(cand[len(word):]))
	}
	return out, len([]rune(word))
}

func isWordRune(r rune) bool {
	return r == '_' || r == ':' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

// completions returns the sorted candidate names with the given prefix. A word
// starting with ':' completes meta-commands; otherwise top-level variables,
// functions, and session bindings (excluding the managed _/_N results).
func (s *Session) completions(word string) []string {
	var cands []string

	if strings.HasPrefix(word, ":") {
		cands = append(cands, metaCommands...)
	} else {
		evalCtx := s.cfg.EvalCtx()
		for name := range evalCtx.Variables {
			cands = append(cands, name)
		}
		for name := range evalCtx.Functions {
			cands = append(cands, name)
		}
		cands = append(cands, "ctx")
		for name := range s.bindings {
			if name == "_" || resultNameRe.MatchString(name) {
				continue
			}
			cands = append(cands, name)
		}
	}

	matched := cands[:0]
	for _, c := range cands {
		if strings.HasPrefix(c, word) {
			matched = append(matched, c)
		}
	}
	sort.Strings(matched)
	return matched
}
