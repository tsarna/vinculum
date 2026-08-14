package cmd

import "github.com/hashicorp/hcl/v2"

// jsonRange is how every machine-readable report spells a source location, so
// a consumer reading more than one of them — `test --json` and
// `check --format json` are both for editor tooling — learns the shape once.
//
// Line and column are 1-based, as hcl reports them and as an editor expects.
type jsonRange struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"end_line"`
	EndColumn int    `json:"end_column"`
}

func rangeToJSON(r hcl.Range) *jsonRange {
	return &jsonRange{
		File:      r.Filename,
		Line:      r.Start.Line,
		Column:    r.Start.Column,
		EndLine:   r.End.Line,
		EndColumn: r.End.Column,
	}
}

// rangePtrToJSON converts an optional range — a diagnostic's Subject or Context
// — leaving it absent from the report rather than emitting a zero location.
func rangePtrToJSON(r *hcl.Range) *jsonRange {
	if r == nil {
		return nil
	}
	return rangeToJSON(*r)
}
