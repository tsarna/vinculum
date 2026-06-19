package repl

import (
	"slices"
	"testing"
)

func TestCompletions(t *testing.T) {
	s, _, _ := newTestSession(t, `bus "main" {}`)
	s.setBinding("myvar", "1")

	tests := []struct {
		word       string
		wantSubset []string
		wantNone   bool
	}{
		{word: ":he", wantSubset: []string{":help"}},
		{word: ":l", wantSubset: []string{":loglevel", ":logs"}},
		{word: "bu", wantSubset: []string{"bus"}},
		{word: "ct", wantSubset: []string{"ctx"}},
		{word: "len", wantSubset: []string{"length"}},
		{word: "myv", wantSubset: []string{"myvar"}},
		{word: "zzz_no_match", wantNone: true},
	}

	for _, tt := range tests {
		got := s.completions(tt.word)
		if tt.wantNone {
			if len(got) != 0 {
				t.Errorf("completions(%q) = %v, want none", tt.word, got)
			}
			continue
		}
		for _, want := range tt.wantSubset {
			if !slices.Contains(got, want) {
				t.Errorf("completions(%q) = %v, missing %q", tt.word, got, want)
			}
		}
	}

	// Managed result names are never offered.
	s.bindings["_1"] = s.bindings["myvar"]
	if slices.Contains(s.completions("_"), "_1") {
		t.Error("completions leaked managed result name _1")
	}
}

func TestCompleterDo(t *testing.T) {
	s, _, _ := newTestSession(t, `bus "main" {}`)
	c := &completer{s: s}

	// "bu" → "bus": suffix "s", prefix length 2.
	line := []rune("bu")
	got, length := c.Do(line, len(line))
	if length != 2 {
		t.Fatalf("Do length = %d, want 2", length)
	}
	if !slices.ContainsFunc(got, func(r []rune) bool { return string(r) == "s" }) {
		t.Fatalf("Do(%q) suffixes = %v, want to contain \"s\"", string(line), runesToStrings(got))
	}

	// After a '.', the final segment is completed against the base's attributes:
	// "bus." → the bus names, replacing nothing (length 0).
	dotLine := []rune("bus.")
	got, length = c.Do(dotLine, len(dotLine))
	if length != 0 {
		t.Fatalf("Do after '.' length = %d, want 0", length)
	}
	if !slices.ContainsFunc(got, func(r []rune) bool { return string(r) == "main" }) {
		t.Fatalf("Do(%q) = %v, want to contain \"main\"", string(dotLine), runesToStrings(got))
	}
}

func TestNestedCompletions(t *testing.T) {
	s, _, _ := newTestSession(t, `bus "alpha" {}`+"\n"+`bus "beta" {}`)

	// Built-in namespace: bus names, with prefix filtering. (A default "main"
	// bus also exists, so check membership rather than exact equality.)
	allBuses := s.attrCompletions("bus", "")
	if !slices.Contains(allBuses, "alpha") || !slices.Contains(allBuses, "beta") {
		t.Fatalf(`attrCompletions("bus","") = %v, want to contain alpha and beta`, allBuses)
	}
	if got := s.attrCompletions("bus", "a"); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf(`attrCompletions("bus","a") = %v, want [alpha]`, got)
	}

	// ctx attributes are available; the internal _ctx is hidden.
	ctxAttrs := s.attrCompletions("ctx", "")
	for _, want := range []string{"auth", "span_id", "trace_id"} {
		if !slices.Contains(ctxAttrs, want) {
			t.Errorf(`attrCompletions("ctx","") = %v, missing %q`, ctxAttrs, want)
		}
	}
	if slices.Contains(ctxAttrs, "_ctx") {
		t.Errorf("ctx completion leaked internal _ctx")
	}

	// A capsule value (a specific bus) has no completable attributes.
	if got := s.attrCompletions("bus.alpha", ""); len(got) != 0 {
		t.Errorf(`attrCompletions("bus.alpha","") = %v, want none`, got)
	}

	// Deep walk through nested objects via a session binding.
	s.setBinding("m", `{outer = {inner = 1}}`)
	if got := s.attrCompletions("m", ""); !slices.Equal(got, []string{"outer"}) {
		t.Fatalf(`attrCompletions("m","") = %v, want [outer]`, got)
	}
	if got := s.attrCompletions("m.outer", ""); !slices.Equal(got, []string{"inner"}) {
		t.Fatalf(`attrCompletions("m.outer","") = %v, want [inner]`, got)
	}

	// An unknown base resolves to nothing.
	if got := s.attrCompletions("nope", ""); got != nil {
		t.Errorf(`attrCompletions("nope","") = %v, want nil`, got)
	}
}

func TestNestedEnvCompletion(t *testing.T) {
	t.Setenv("REPL_COMPLETION_PROBE", "x")
	s, _, _ := newTestSession(t, `bus "main" {}`)

	got := s.attrCompletions("env", "REPL_COMPLETION")
	if !slices.Contains(got, "REPL_COMPLETION_PROBE") {
		t.Fatalf(`attrCompletions("env","REPL_COMPLETION") = %v, missing the probe var`, got)
	}
}

func runesToStrings(rs [][]rune) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
