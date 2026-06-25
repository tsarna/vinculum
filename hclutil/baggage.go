package hclutil

import (
	"context"
	"net/http"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"go.opentelemetry.io/otel/baggage"
	"go.uber.org/zap"
)

// Default caps on incoming baggage, applied when a baggage{} block is present.
const (
	defaultBaggageMaxEntries = 64
	defaultBaggageMaxBytes   = 8192
)

// BaggageFilterConfig is the optional baggage{} sub-block on trust-boundary
// servers. It controls which inbound baggage keys are trusted — exposed in
// ctx.baggage and re-propagated downstream — after extraction from inbound
// headers but before action evaluation begins.
//
// Inbound baggage is UNTRUSTED INPUT, so the default (no block, or an empty
// block) is to strip it entirely. Trace propagation (traceparent) is
// unaffected, and baggage your own VCL sets via set(ctx.baggage, …) always
// propagates outbound regardless of this policy. A server opts into trusting
// upstream baggage with one of:
//
//	baggage { passthrough = true }      # trust all inbound baggage
//	baggage { allow = ["tenant_id"] }   # trust only the listed keys
//	baggage { deny  = ["internal."] }   # trust all but the listed key prefixes
type BaggageFilterConfig struct {
	// Passthrough trusts all inbound baggage. Mutually exclusive with Allow/Deny.
	Passthrough bool `hcl:"passthrough,optional"`
	// Allow lists keys to keep; all others are dropped. Mutually exclusive with Deny.
	Allow []string `hcl:"allow,optional"`
	// Deny lists key prefixes to drop; all others are kept. Mutually exclusive with Allow.
	Deny []string `hcl:"deny,optional"`
	// MaxEntries caps the total number of entries (default 64).
	MaxEntries *int `hcl:"max_entries,optional"`
	// MaxBytes caps the total serialized size in bytes (default 8192).
	MaxBytes *int `hcl:"max_bytes,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

// Validate reports config-time errors. Call at parse time. Safe on a nil receiver.
func (c *BaggageFilterConfig) Validate() hcl.Diagnostics {
	if c == nil {
		return nil
	}
	if len(c.Allow) > 0 && len(c.Deny) > 0 {
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Conflicting baggage filter",
			Detail:   "a baggage block may set either allow or deny, not both",
			Subject:  &c.DefRange,
		}}
	}
	if c.Passthrough && (len(c.Allow) > 0 || len(c.Deny) > 0) {
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Conflicting baggage filter",
			Detail:   "passthrough cannot be combined with allow or deny",
			Subject:  &c.DefRange,
		}}
	}
	return nil
}

// keyAllowed applies the allow/deny rules to a single key.
func (c *BaggageFilterConfig) keyAllowed(key string) bool {
	if len(c.Allow) > 0 {
		for _, a := range c.Allow {
			if key == a {
				return true
			}
		}
		return false
	}
	for _, d := range c.Deny {
		if strings.HasPrefix(key, d) {
			return false
		}
	}
	return true
}

// FilterContext applies the trust policy to ctx's inbound baggage. The default
// (nil receiver, an empty block, or no allow/deny/passthrough) is SECURE: all
// inbound baggage is stripped. passthrough=true trusts everything; otherwise
// allow/deny select which keys survive, subject to the size caps. Surplus
// entries beyond max_entries/max_bytes are dropped silently (matching the W3C
// spec's tolerance for oversized baggage) with a debug log.
func (c *BaggageFilterConfig) FilterContext(ctx context.Context, logger *zap.Logger) context.Context {
	if c != nil && c.Passthrough {
		return ctx // trust all inbound baggage
	}
	members := baggage.FromContext(ctx).Members()
	if len(members) == 0 {
		return ctx
	}

	// Secure default: with no allow/deny configured, trust nothing inbound.
	if c == nil || (len(c.Allow) == 0 && len(c.Deny) == 0) {
		if logger != nil {
			logger.Debug("stripped all inbound baggage (no trust configured)",
				zap.Int("dropped", len(members)))
		}
		return baggage.ContextWithBaggage(ctx, baggage.Baggage{})
	}

	maxEntries := defaultBaggageMaxEntries
	if c.MaxEntries != nil {
		maxEntries = *c.MaxEntries
	}
	maxBytes := defaultBaggageMaxBytes
	if c.MaxBytes != nil {
		maxBytes = *c.MaxBytes
	}

	kept := make([]baggage.Member, 0, len(members))
	size := 0
	dropped := 0
	for _, m := range members {
		if !c.keyAllowed(m.Key()) {
			dropped++
			continue
		}
		memLen := len(m.String())
		if len(kept) >= maxEntries || size+memLen > maxBytes {
			dropped++
			continue
		}
		kept = append(kept, m)
		size += memLen + 1 // +1 approximates the comma separator
	}

	if dropped == 0 {
		return ctx
	}
	if logger != nil {
		logger.Debug("dropped inbound baggage entries",
			zap.Int("dropped", dropped), zap.Int("kept", len(kept)))
	}
	filtered, err := baggage.New(kept...)
	if err != nil {
		// kept members were already valid, so this is unexpected; be safe.
		if logger != nil {
			logger.Debug("baggage filter rebuild failed; dropping all", zap.Error(err))
		}
		return baggage.ContextWithBaggage(ctx, baggage.Baggage{})
	}
	return baggage.ContextWithBaggage(ctx, filtered)
}

// Middleware wraps next so that inbound baggage is filtered per this config.
// It must be installed INSIDE the otelhttp handler so it runs after the W3C
// propagator has extracted baggage. A nil receiver applies the secure default
// (strip all), so this should be installed unconditionally on trust-boundary
// servers. When passthrough is set it is a cheap pass-through.
func (c *BaggageFilterConfig) Middleware(logger *zap.Logger, next http.Handler) http.Handler {
	if c != nil && c.Passthrough {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(c.FilterContext(r.Context(), logger)))
	})
}
