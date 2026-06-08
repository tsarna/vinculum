package sql

import (
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
)

// CommonDef holds the configuration shared by every SQL dialect block. A
// dialect package decodes its own definition struct (with hcl:",remain" to
// capture the rest of the body), then passes the remaining body to
// DecodeCommonDef and the result to ProcessCommon.
//
// gohcl does not flatten embedded structs, so CommonDef is decoded from the
// dialect struct's remaining body in a second pass rather than by embedding.
//
// Note: `disabled` is consumed by the top-level client handler before the
// dialect processor runs, so it is intentionally absent here.
type CommonDef struct {
	MaxOpenConns     *int    `hcl:"max_open_conns,optional"`
	MaxIdleConns     *int    `hcl:"max_idle_conns,optional"`
	ConnMaxLifetime  *string `hcl:"conn_max_lifetime,optional"`
	ConnMaxIdleTime  *string `hcl:"conn_max_idle_time,optional"`
	StatementTimeout *string `hcl:"statement_timeout,optional"`

	Queries []QueryDef `hcl:"query,block"`
}

// DecodeCommonDef decodes the dialect-agnostic configuration from the remaining
// body of a SQL client block (after the dialect has decoded its own fields via
// hcl:",remain").
func DecodeCommonDef(body hcl.Body, ctx *hcl.EvalContext) (*CommonDef, hcl.Diagnostics) {
	var c CommonDef
	diags := gohcl.DecodeBody(body, ctx, &c)
	if diags.HasErrors() {
		return nil, diags
	}
	return &c, nil
}

// ProcessCommon builds an *SQLClient from a decoded dialect definition and
// registers it as a Startable/Stoppable. The dialect package is responsible for
// decoding its own definition struct (embedding CommonDef) and constructing the
// Dialect before calling this.
func ProcessCommon(config *cfg.Config, block *hcl.Block, d Dialect, common *CommonDef, defaults PoolDefaults) (cfg.Client, hcl.Diagnostics) {
	name := block.Labels[1]

	client := &SQLClient{
		BaseClient:  cfg.BaseClient{Name: name, DefRange: block.DefRange},
		dialect:     d,
		queries:     map[string]*namedQuery{},
		maxOpen:     defaults.MaxOpenConns,
		maxIdle:     defaults.MaxIdleConns,
		connMaxLife: defaults.ConnMaxLifetime,
		connMaxIdle: defaults.ConnMaxIdleTime,
	}

	// ── pool knobs ──
	if common.MaxOpenConns != nil {
		client.maxOpen = *common.MaxOpenConns
	}
	if common.MaxIdleConns != nil {
		client.maxIdle = *common.MaxIdleConns
	}
	if d, diags := parseOptionalDuration(common.ConnMaxLifetime, "conn_max_lifetime", block.DefRange); diags.HasErrors() {
		return nil, diags
	} else {
		client.connMaxLife = d
	}
	if d, diags := parseOptionalDuration(common.ConnMaxIdleTime, "conn_max_idle_time", block.DefRange); diags.HasErrors() {
		return nil, diags
	} else {
		client.connMaxIdle = d
	}

	// ── client-default statement timeout ──
	if d, diags := parseOptionalDuration(common.StatementTimeout, "statement_timeout", block.DefRange); diags.HasErrors() {
		return nil, diags
	} else {
		client.stmtTO = d
	}

	// ── named queries ──
	for _, qd := range common.Queries {
		if qd.Disabled {
			continue
		}
		if _, dup := client.queries[qd.Name]; dup {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Duplicate query name",
				Detail:   fmt.Sprintf("query %q is declared more than once in client %q", qd.Name, name),
				Subject:  &qd.DefRange,
			}}
		}

		card, diags := parseCardinality(qd.Cardinality, qd.DefRange)
		if diags.HasErrors() {
			return nil, diags
		}

		onZeroError, diags := parseOnZero(qd.OnZero, card, qd.DefRange)
		if diags.HasErrors() {
			return nil, diags
		}

		stmtTO := client.stmtTO
		if to, diags := parseOptionalDuration(qd.StatementTimeout, "statement_timeout", qd.DefRange); diags.HasErrors() {
			return nil, diags
		} else if qd.StatementTimeout != nil {
			stmtTO = to
		}

		client.queries[qd.Name] = &namedQuery{
			client:      client,
			name:        qd.Name,
			sql:         qd.SQL,
			card:        card,
			onZeroError: onZeroError,
			stmtTO:      stmtTO,
			defRange:    qd.DefRange,
		}
		client.queryOrder = append(client.queryOrder, qd.Name)
	}

	config.Startables = append(config.Startables, client)
	config.Stoppables = append(config.Stoppables, client)

	return client, nil
}

// parseOnZero validates the on_zero attribute. It is only meaningful for
// zero_or_one queries; setting it elsewhere is a config error.
func parseOnZero(s string, card cardinality, r hcl.Range) (bool, hcl.Diagnostics) {
	if s == "" {
		return false, nil
	}
	if card != cardZeroOrOne {
		return false, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid on_zero",
			Detail:   "on_zero is only valid on a query with cardinality \"zero_or_one\"",
			Subject:  &r,
		}}
	}
	switch s {
	case "null":
		return false, nil
	case "error":
		return true, nil
	default:
		return false, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid on_zero",
			Detail:   fmt.Sprintf("on_zero must be \"null\" or \"error\"; got %q", s),
			Subject:  &r,
		}}
	}
}

// parseOptionalDuration parses an optional duration string (nil → 0).
func parseOptionalDuration(s *string, attr string, r hcl.Range) (time.Duration, hcl.Diagnostics) {
	if s == nil || *s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(*s)
	if err != nil {
		return 0, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid duration",
			Detail:   fmt.Sprintf("%s: %v", attr, err),
			Subject:  &r,
		}}
	}
	return d, nil
}
