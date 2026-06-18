package repl

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap/zapcore"
)

// assignRe matches a bare assignment "NAME = EXPR". The trailing group captures
// everything after the first '='; the caller rejects it as a comparison ("==")
// when that group begins with '='. HCL's expression grammar has no top-level
// '=', so "NAME = …" is never itself a valid expression.
var assignRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$`)

// resultNameRe matches the managed numbered-result names (_1, _2, …).
var resultNameRe = regexp.MustCompile(`^_[0-9]+$`)

// parseAssignment reports whether src is a bare assignment and, if so, returns
// the target name and the right-hand expression source. A "==" comparison is
// not an assignment.
func parseAssignment(src string) (name, rhs string, ok bool) {
	m := assignRe.FindStringSubmatch(src)
	if m == nil {
		return "", "", false
	}
	if strings.HasPrefix(m[2], "=") {
		return "", "", false // "==" is a comparison expression, not an assignment
	}
	return m[1], m[2], true
}

// handleMeta dispatches a meta-command line (first non-space char is ':'). It
// returns true if the REPL should exit.
func (s *Session) handleMeta(line string) (exit bool) {
	fields := strings.Fields(line)
	switch fields[0] {
	case ":quit", ":q", ":exit":
		return true
	case ":help", ":?":
		s.printHelp()
	case ":set":
		s.metaSet(line)
	case ":unset":
		s.metaUnset(fields)
	case ":vars":
		s.printVars()
	case ":loglevel":
		s.metaLoglevel(fields)
	case ":quiet":
		s.muteLogs()
	case ":logs":
		s.metaLogs(fields)
	default:
		fmt.Fprintf(s.errOut, "unknown command: %s (try :help)\n", fields[0])
	}
	return false
}

// setBinding evaluates rhsSrc and binds the result to name, after rejecting
// reserved names. A non-null result is also numbered into _/_N and echoed; a
// null result still creates the named binding but prints nothing and leaves _
// untouched.
func (s *Session) setBinding(name, rhsSrc string) {
	if err := s.checkAssignable(name); err != nil {
		fmt.Fprintf(s.errOut, "%s\n", err)
		return
	}
	val, ok := s.parseAndEval(rhsSrc)
	if !ok {
		return
	}
	s.bindings[name] = val
	s.echoAndNumber(val)
}

// checkAssignable returns an error if name is reserved and may not be bound:
// the managed result names (_ and _N), the ctx variable, or any built-in
// top-level namespace (bus, server, client, env, http_status, …).
func (s *Session) checkAssignable(name string) error {
	if name == "_" || resultNameRe.MatchString(name) {
		return fmt.Errorf("cannot assign %q: managed result name", name)
	}
	if name == "ctx" {
		return fmt.Errorf("cannot assign %q: reserved name", name)
	}
	if _, ok := s.cfg.EvalCtx().Variables[name]; ok {
		return fmt.Errorf("cannot assign %q: built-in namespace", name)
	}
	return nil
}

func (s *Session) metaSet(line string) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ":set"))
	name, rhs, ok := parseAssignment(rest)
	if !ok {
		fmt.Fprintln(s.errOut, "usage: :set NAME = EXPR")
		return
	}
	s.setBinding(name, rhs)
}

func (s *Session) metaUnset(fields []string) {
	if len(fields) != 2 {
		fmt.Fprintln(s.errOut, "usage: :unset NAME")
		return
	}
	name := fields[1]
	if name == "_" || resultNameRe.MatchString(name) {
		fmt.Fprintf(s.errOut, "cannot unset %q: managed result name\n", name)
		return
	}
	if _, ok := s.bindings[name]; !ok {
		fmt.Fprintf(s.errOut, "no such binding: %s\n", name)
		return
	}
	delete(s.bindings, name)
}

// printVars lists the user's named session bindings (the managed _ / _N results
// are omitted), with each value's type and a truncated rendering.
func (s *Session) printVars() {
	names := make([]string, 0, len(s.bindings))
	for k := range s.bindings {
		if k == "_" || resultNameRe.MatchString(k) {
			continue
		}
		names = append(names, k)
	}
	if len(names) == 0 {
		fmt.Fprintln(s.out, "(no bindings)")
		return
	}
	sort.Strings(names)

	width := 0
	for _, n := range names {
		if len(n) > width {
			width = len(n)
		}
	}
	for _, n := range names {
		v := s.bindings[n]
		fmt.Fprintf(s.out, "%-*s : %s = %s\n", width, n,
			v.Type().FriendlyName(), truncateOneLine(formatValue(v), 60))
	}
}

func (s *Session) metaLoglevel(fields []string) {
	if len(fields) != 2 {
		fmt.Fprintln(s.errOut, "usage: :loglevel debug|info|warn|error")
		return
	}
	lvl, ok := parseLogLevel(fields[1])
	if !ok {
		fmt.Fprintf(s.errOut, "unknown log level: %s\n", fields[1])
		return
	}
	s.muted = false
	s.logging.level.SetLevel(lvl)
}

func (s *Session) metaLogs(fields []string) {
	if len(fields) != 2 {
		fmt.Fprintln(s.errOut, "usage: :logs on|off")
		return
	}
	switch fields[1] {
	case "on":
		s.unmuteLogs()
	case "off":
		s.muteLogs()
	default:
		fmt.Fprintln(s.errOut, "usage: :logs on|off")
	}
}

// muteLogs raises the level above everything emitted (idiomatic zap muting),
// remembering the prior level so unmuteLogs can restore it.
func (s *Session) muteLogs() {
	if !s.muted {
		s.savedLevel = s.logging.level.Level()
		s.muted = true
	}
	s.logging.level.SetLevel(zapcore.FatalLevel + 1)
}

func (s *Session) unmuteLogs() {
	if s.muted {
		s.logging.level.SetLevel(s.savedLevel)
		s.muted = false
	}
}

func parseLogLevel(s string) (zapcore.Level, bool) {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel, true
	case "info":
		return zapcore.InfoLevel, true
	case "warn", "warning":
		return zapcore.WarnLevel, true
	case "error":
		return zapcore.ErrorLevel, true
	default:
		return 0, false
	}
}

func (s *Session) printHelp() {
	const help = `Commands:
  :help, :?              show this help
  :quit, :q, :exit       exit the REPL and shut down the server
  :set NAME = EXPR       bind EXPR to NAME (same as bare "NAME = EXPR")
  :unset NAME            remove a session binding
  :vars                  list session bindings
  :loglevel LEVEL        set async log level (debug|info|warn|error)
  :quiet                 mute async logs
  :logs on|off           unmute / mute async logs

Type any VCL expression to evaluate it. Results are bound to _ and _1.._N.`
	fmt.Fprintln(s.out, help)
}

// truncateOneLine collapses a (possibly multi-line) rendering to a single line
// and truncates it to max runes, appending an ellipsis when shortened.
func truncateOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
