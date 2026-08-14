package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/internal/suggest"
)

// Reporting a type label that names no registered type: the `htp` of
// `server "htp" "web"`.
//
// Every typed block dispatches on its first label through a registry, and the
// registry is the list of answers — so the diagnostic offers the nearest one,
// or names them all when there are few enough to read. The alternative, which
// this replaces, was to repeat the label back to the author, who could already
// see it.

// typeListMax is how many type names a diagnostic spells out before pointing at
// `vinculum man` instead. Every block but `client` is under it today; eighteen
// client types read as a wall, and burying the summary under one helps nobody.
const typeListMax = 12

// unknownTypeDiag reports a block type label that no registered type matches.
//
// available is the set the caller can actually dispatch to, which is not always
// the set that exists: a conditional type — one a factory contributes only when
// some feature is on — is absent from it in a configuration that does not
// enable the feature. Reporting that as a bad name would be a lie, so it is
// told apart and sent to the page that says what the type needs.
func unknownTypeDiag(blockType, label string, available []string, subject hcl.Range) *hcl.Diagnostic {
	if conditionalTypes[blockType][label] {
		return &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("Unavailable %s type", blockType),
			Detail: fmt.Sprintf(
				"The %s type %q is not available in this configuration. It exists, but only "+
					"some configurations enable it — run `vinculum man %s %s` for what it needs.",
				blockType, label, blockType, label),
			Subject: &subject,
		}
	}

	detail := fmt.Sprintf("There is no %s type %q.", blockType, label)
	nearest := suggest.Nearest(label, available)
	switch {
	case nearest != "":
		detail += fmt.Sprintf(" Did you mean %q?", nearest)
	case len(available) == 0:
		detail += fmt.Sprintf(" No %s types are registered in this binary.", blockType)
	case len(available) > typeListMax:
		detail += fmt.Sprintf(" There are %d %s types; run `vinculum man %s` for the list.",
			len(available), blockType, blockType)
	default:
		detail += fmt.Sprintf(" Available types: %s.", joinNames(available))
	}

	return &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  fmt.Sprintf("Unknown %s type", blockType),
		Detail:   detail,
		Subject:  &subject,
	}
}
