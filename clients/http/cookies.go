// Cookie jar for the HTTP client.
//
// When the cookies sub-block on a
// `client "http"` is present and enabled, the client carries an in-memory
// net/http/cookiejar.Jar behind its transport. Cookies set by responses
// are stored and re-sent on subsequent matching requests, optionally
// scoped per the public-suffix list.
//
// Per-call opts.cookies are sent in addition to whatever the jar would
// supply and do not modify the jar — that handling lives in package
// functions, but this file holds the building blocks both layers share.

package http

import (
	"net/http/cookiejar"

	"github.com/hashicorp/hcl/v2"
	"golang.org/x/net/publicsuffix"
)

// cookiesBlock is the HCL-decoded form of a `cookies { ... }` sub-block.
//
// Both fields default such that simply writing `cookies {}` is enough to
// turn on the jar with the public-suffix list.
type cookiesBlock struct {
	// Enabled toggles the jar. Defaults to true when the cookies block
	// is present, so `cookies {}` is sufficient to opt in. Explicitly
	// setting `enabled = false` is equivalent to omitting the block.
	Enabled *bool `hcl:"enabled,optional"`

	// PublicSuffix selects whether the jar's eTLD+1 scoping is taken
	// from the public-suffix list. Defaults to true alongside Enabled.
	PublicSuffix *bool `hcl:"public_suffix,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

// buildCookieJar constructs a *cookiejar.Jar according to b. Returns
// (nil, nil) when the jar should not be installed (block absent or
// `enabled = false`).
func buildCookieJar(b *cookiesBlock) (*cookiejar.Jar, error) {
	if b == nil {
		return nil, nil
	}
	enabled := true
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	if !enabled {
		return nil, nil
	}

	usePSL := true
	if b.PublicSuffix != nil {
		usePSL = *b.PublicSuffix
	}

	opts := &cookiejar.Options{}
	if usePSL {
		opts.PublicSuffixList = publicsuffix.List
	}
	return cookiejar.New(opts)
}
