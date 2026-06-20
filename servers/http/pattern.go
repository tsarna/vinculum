package httpserver

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// splitPattern parses "[METHOD ][HOST]/[PATH]" into its components, following
// Go 1.22's net/http.ServeMux pattern grammar.
//
// host is "" when the pattern is host-less (matches all hosts). ok is false
// when there is no path segment (no '/'), which is a configuration error for
// blocks that require a path (e.g. files).
func splitPattern(pattern string) (method, host, path string, ok bool) {
	rest := strings.TrimSpace(pattern)
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		method = rest[:sp]
		rest = strings.TrimLeft(rest[sp+1:], " ")
	}
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return method, "", "", false
	}
	return method, rest[:i], rest[i:], true
}

// safeMuxHandle registers a handler with the mux, converting the panic that
// ServeMux.Handle raises on a malformed pattern or a conflicting registration
// into an hcl.Diagnostic anchored to the block. Without this, a host typo or
// route overlap would crash config processing instead of producing a clean
// error.
func safeMuxHandle(mux *http.ServeMux, pattern string, h http.Handler, def hcl.Range) (diags hcl.Diagnostics) {
	defer func() {
		if r := recover(); r != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid or conflicting HTTP route pattern",
				Detail:   fmt.Sprintf("pattern %q: %v", pattern, r),
				Subject:  &def,
			})
		}
	}()
	mux.Handle(pattern, h)
	return
}
