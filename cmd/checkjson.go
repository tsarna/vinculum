package cmd

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/hashicorp/hcl/v2"
)

// The machine-readable form of `vinculum check`.
//
// It exists because the alternative is scraping: an editor extension drawing
// squiggles needs a severity and a range per problem, and the text form spends
// its effort on being read by a person — quoting the line, wrapping the prose.
// Both describe the same diagnostics; only the audience differs.

type jsonCheckReport struct {
	// Valid is false when anything is an error. Warnings do not make a
	// configuration invalid, which is why this is not simply "no diagnostics".
	Valid       bool                  `json:"valid"`
	Diagnostics []jsonCheckDiagnostic `json:"diagnostics"`
	Summary     jsonCheckSummary      `json:"summary"`
}

type jsonCheckSummary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

type jsonCheckDiagnostic struct {
	Severity string `json:"severity"` // "error" or "warning"
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
	// Location is what the diagnostic is about, and Context the construct that
	// contains it — the block a bad attribute is in. Either may be absent: a
	// failure to read a file is about no particular line of one.
	Location *jsonRange `json:"location,omitempty"`
	Context  *jsonRange `json:"context,omitempty"`
}

// writeCheckJSON emits the report and returns the same exit status the text
// form would, marked Reported: the report is on stdout, and a trailing "Error:"
// line on stderr would say nothing the JSON does not already carry.
func writeCheckJSON(w io.Writer, diags hcl.Diagnostics) error {
	report := jsonCheckReport{
		Valid:       !diags.HasErrors(),
		Diagnostics: make([]jsonCheckDiagnostic, 0, len(diags)),
	}

	for _, d := range diags {
		severity := "error"
		if d.Severity == hcl.DiagWarning {
			severity = "warning"
			report.Summary.Warnings++
		} else {
			report.Summary.Errors++
		}
		report.Diagnostics = append(report.Diagnostics, jsonCheckDiagnostic{
			Severity: severity,
			Summary:  d.Summary,
			Detail:   d.Detail,
			Location: rangePtrToJSON(d.Subject),
			Context:  rangePtrToJSON(d.Context),
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return &ExitCodeError{Code: 2, Err: err}
	}

	if !report.Valid {
		return &ExitCodeError{Code: 1, Err: errors.New("invalid configuration"), Reported: true}
	}
	return nil
}
