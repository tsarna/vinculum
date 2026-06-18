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

	// After a '.', nested completion is suppressed.
	dotLine := []rune("bus.")
	if out, _ := c.Do(dotLine, len(dotLine)); out != nil {
		t.Fatalf("Do after '.' = %v, want nil", runesToStrings(out))
	}
}

func runesToStrings(rs [][]rune) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
