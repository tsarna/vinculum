package config

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// TrustedProxies is a set of networks whose assertions about a request are
// believed. It backs both `real_ip`, which trusts a forwarded address, and
// `auth "proxy"`, which trusts a forwarded identity.
//
// Both are the same judgement — "this peer is part of our infrastructure, so
// what it tells us about the client is true" — and getting it wrong has the
// same shape in both: a header anyone can set, believed.
type TrustedProxies struct {
	nets []*net.IPNet
}

// ParseTrustedProxies compiles a list of CIDRs and bare IPs. An empty list is
// an error at every call site, so it is one here: a trust list that trusts
// nothing is either a mistake or a feature that should not have been enabled.
func ParseTrustedProxies(entries []string, subject *hcl.Range, attr string) (*TrustedProxies, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	if len(entries) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Missing " + attr,
			Detail:   fmt.Sprintf("%s must list at least one network or address.", attr),
			Subject:  subject,
		}}
	}

	t := &TrustedProxies{}
	for _, entry := range entries {
		ipnet, err := parseCIDROrIP(entry)
		if err != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid " + attr + " entry",
				Detail:   fmt.Sprintf("%q is not a valid IP address or CIDR: %v", entry, err),
				Subject:  subject,
			})
			continue
		}
		t.nets = append(t.nets, ipnet)
	}
	if diags.HasErrors() {
		return nil, diags
	}
	return t, nil
}

// Trusts reports whether ip falls in any trusted network.
func (t *TrustedProxies) Trusts(ip net.IP) bool {
	if t == nil || ip == nil {
		return false
	}
	for _, n := range t.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// TrustsPeer reports whether the request's immediate peer is trusted.
//
// It reads RemoteAddr, which is the address the connection actually came from —
// not a forwarded header, which is the thing being decided about. When `real_ip`
// is also configured it has already rewritten RemoteAddr, having made this same
// check against the original peer first.
func (t *TrustedProxies) TrustsPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return t.Trusts(net.ParseIP(host))
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
