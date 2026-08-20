package httpserver

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// parseExternalURL validates the server's external_url and returns it, or nil
// when the attribute was not set.
//
// It is a base URL, so it must carry a scheme and host and must not carry the
// per-request parts — a query or fragment on a base would silently vanish when
// something appends a path to it. A trailing slash is trimmed so that joining a
// path onto it cannot produce a doubled separator.
func parseExternalURL(raw string, defRange hcl.Range) (*url.URL, hcl.Diagnostics) {
	if raw == "" {
		return nil, nil
	}

	bad := func(detail string) hcl.Diagnostics {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid external_url",
			Detail:   detail,
			Subject:  &defRange,
		}}
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, bad(err.Error())
	}

	switch {
	case u.Scheme == "":
		return nil, bad(fmt.Sprintf("external_url must be an absolute URL including the scheme, "+
			"such as \"https://api.example.com\". Got %q.", raw))
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, bad(fmt.Sprintf("external_url must be an http or https URL. Got scheme %q.", u.Scheme))
	case u.Host == "":
		return nil, bad(fmt.Sprintf("external_url must include a host, such as "+
			"\"https://api.example.com\". Got %q.", raw))
	case u.RawQuery != "" || u.Fragment != "":
		return nil, bad("external_url is a base URL and may not carry a query string or fragment.")
	}

	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}
