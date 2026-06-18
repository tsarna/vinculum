package repl

import "testing"

func TestPromptTracksResultCounter(t *testing.T) {
	s, _, _ := newTestSession(t, `bus "main" {}`)

	// Before any result, the next result is _1.
	if got := s.primaryPrompt(); got != "1> " {
		t.Fatalf("initial prompt = %q, want %q", got, "1> ")
	}
	// Continuation is dotted and width-matched to the primary prompt.
	if got := s.continuationPrompt(); got != ".. " {
		t.Fatalf("initial continuation = %q, want %q", got, ".. ")
	}

	// A successful, non-null result advances the prompt.
	s.evalExpr("42")
	if got := s.primaryPrompt(); got != "2> " {
		t.Fatalf("prompt after one result = %q, want %q", got, "2> ")
	}

	// A top-level null does not advance it.
	s.evalExpr("null")
	if got := s.primaryPrompt(); got != "2> " {
		t.Fatalf("prompt after null = %q, want %q (unchanged)", got, "2> ")
	}

	// Width-matching scales with the number of digits.
	s.resultCounter = 11
	if got := s.primaryPrompt(); got != "12> " {
		t.Fatalf("prompt = %q, want %q", got, "12> ")
	}
	if got := s.continuationPrompt(); got != "... " {
		t.Fatalf("continuation = %q, want %q", got, "... ")
	}
}
