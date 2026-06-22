package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// realIPConfig is the decoded `real_ip` sub-block of a `server "http"` block.
// It mirrors nginx's real_ip module: trust a set of proxy networks, read the
// real client address from a forwarded header, and (optionally) walk a
// forwarded chain recursively.
type realIPConfig struct {
	TrustedProxies []string  `hcl:"trusted_proxies"`
	Header         string    `hcl:"header,optional"`
	Recursive      bool      `hcl:"recursive,optional"`
	Disabled       bool      `hcl:"disabled,optional"`
	DefRange       hcl.Range `hcl:",def_range"`
}

// realIPResolver is the compiled form of realIPConfig used at request time.
type realIPResolver struct {
	trusted   []*net.IPNet
	header    string
	recursive bool
}

// compileRealIP validates a decoded real_ip block and compiles it into a
// resolver, parsing each trusted_proxies entry as a CIDR or a bare IP.
func compileRealIP(c *realIPConfig) (*realIPResolver, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	r := &realIPResolver{
		header:    "X-Forwarded-For",
		recursive: c.Recursive,
	}
	if c.Header != "" {
		r.header = c.Header
	}

	if len(c.TrustedProxies) == 0 {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "real_ip requires trusted_proxies",
				Detail:   "a real_ip block must list at least one trusted proxy network in trusted_proxies",
				Subject:  &c.DefRange,
			},
		}
	}

	for _, entry := range c.TrustedProxies {
		ipnet, err := parseCIDROrIP(entry)
		if err != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid trusted_proxies entry",
				Detail:   fmt.Sprintf("%q is not a valid IP address or CIDR: %v", entry, err),
				Subject:  &c.DefRange,
			})
			continue
		}
		r.trusted = append(r.trusted, ipnet)
	}
	if diags.HasErrors() {
		return nil, diags
	}

	return r, nil
}

// parseCIDROrIP parses a CIDR ("10.0.0.0/8") or a bare IP ("10.0.0.7", treated
// as a host route) into an *net.IPNet.
func parseCIDROrIP(s string) (*net.IPNet, error) {
	if strings.Contains(s, "/") {
		_, ipnet, err := net.ParseCIDR(s)
		if err != nil {
			return nil, err
		}
		return ipnet, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("not an IP or CIDR")
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

// trusts reports whether ip falls in any trusted proxy network.
func (r *realIPResolver) trusts(ip net.IP) bool {
	for _, n := range r.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// wrap returns a middleware that rewrites the request's RemoteAddr to the real
// client IP when the immediate peer is a trusted proxy. It must run outermost
// (before tracing/logging/auth) so the whole chain sees the corrected address.
func (r *realIPResolver) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if clientIP := r.resolve(req); clientIP != "" {
			req.RemoteAddr = clientIP
		}
		next.ServeHTTP(w, req)
	})
}

// resolve returns the real client IP for req, or "" to leave RemoteAddr
// untouched. The substitution only happens when the immediate peer is trusted;
// otherwise the forwarded header is attacker-controlled and is ignored.
func (r *realIPResolver) resolve(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !r.trusts(peer) {
		return ""
	}

	// Flatten the header into an ordered list of addresses (leftmost =
	// original client, rightmost = nearest proxy). The header may appear on
	// multiple lines, each itself comma-separated.
	var addrs []string
	for _, v := range req.Header.Values(r.header) {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				addrs = append(addrs, part)
			}
		}
	}
	if len(addrs) == 0 {
		return ""
	}

	if !r.recursive {
		// Non-recursive: the rightmost address (nginx real_ip_recursive off).
		return addrs[len(addrs)-1]
	}

	// Recursive: walk right-to-left, skipping trusted proxies; the first
	// untrusted address is the real client. If every address is trusted, fall
	// back to the leftmost.
	for i := len(addrs) - 1; i >= 0; i-- {
		ip := net.ParseIP(addrs[i])
		if ip == nil {
			continue
		}
		if !r.trusts(ip) {
			return addrs[i]
		}
	}
	return addrs[0]
}
