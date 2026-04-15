// Package redis implements the `client "redis"` block — a passive connection
// manager that holds shared config (addresses, auth, TLS, pool tuning) and
// exposes a redis.UniversalClient for child clients (redis_pubsub,
// redis_stream, redis_kv) to reference via `connection = client.<name>`.

package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	goredis "github.com/redis/go-redis/v9"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("redis", process)
}

// RedisConnectionDefinition is the HCL schema for `client "redis" "<name>"`.
type RedisConnectionDefinition struct {
	Mode             string         `hcl:"mode,optional"`
	Address          string         `hcl:"address,optional"`
	Addresses        []string       `hcl:"addresses,optional"`
	MasterName       string         `hcl:"master_name,optional"`
	Database         *int           `hcl:"database,optional"`
	Username         string         `hcl:"username,optional"`
	Password         hcl.Expression `hcl:"password,optional"`
	SentinelUsername string         `hcl:"sentinel_username,optional"`
	SentinelPassword hcl.Expression `hcl:"sentinel_password,optional"`
	TLS              *cfg.TLSConfig `hcl:"tls,block"`
	PoolSize         *int           `hcl:"pool_size,optional"`
	MinIdleConns     *int           `hcl:"min_idle_conns,optional"`
	DialTimeout      hcl.Expression `hcl:"dial_timeout,optional"`
	DefRange         hcl.Range      `hcl:",def_range"`
}

// RedisConnector is the interface child clients (redis_pubsub, redis_stream,
// redis_kv) use to obtain the shared go-redis client from the base block.
type RedisConnector interface {
	cfg.Client
	UniversalClient() goredis.UniversalClient
}

// RedisClient is the runtime representation of a `client "redis"` block.
// It owns the go-redis UniversalClient and closes it on Stop().
type RedisClient struct {
	cfg.BaseClient
	client goredis.UniversalClient
}

func (c *RedisClient) UniversalClient() goredis.UniversalClient {
	return c.client
}

// Start pings the server to fail fast on bad credentials / bad address.
// go-redis's pool is lazy otherwise, so a silent misconfiguration would
// only surface at the first child-client call.
func (c *RedisClient) Start() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis client %q: ping failed: %w", c.Name, err)
	}
	return nil
}

func (c *RedisClient) Stop() error {
	if c.client == nil {
		return nil
	}
	return c.client.Close()
}

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := RedisConnectionDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}

	clientName := block.Labels[1]

	mode := def.Mode
	if mode == "" {
		mode = "standalone"
	}

	addrs, modeDiags := validateModeAndAddrs(mode, &def)
	if modeDiags.HasErrors() {
		return nil, modeDiags
	}

	var tlsCfg *tls.Config
	if def.TLS != nil && def.TLS.Enabled {
		c, tlsErr := def.TLS.BuildTLSClientConfig(config.BaseDir)
		if tlsErr != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: invalid TLS config",
				Detail:   tlsErr.Error(),
				Subject:  &def.TLS.DefRange,
			}}
		}
		tlsCfg = c
	}

	password, pwDiags := evalOptionalString(config, def.Password, "password")
	if pwDiags.HasErrors() {
		return nil, pwDiags
	}
	sentinelPassword, spDiags := evalOptionalString(config, def.SentinelPassword, "sentinel_password")
	if spDiags.HasErrors() {
		return nil, spDiags
	}

	uopts := &goredis.UniversalOptions{
		Addrs:            addrs,
		Username:         def.Username,
		Password:         password,
		TLSConfig:        tlsCfg,
		MasterName:       def.MasterName,
		SentinelUsername: def.SentinelUsername,
		SentinelPassword: sentinelPassword,
	}
	if def.Database != nil && mode != "cluster" {
		uopts.DB = *def.Database
	}
	if def.PoolSize != nil {
		uopts.PoolSize = *def.PoolSize
	}
	if def.MinIdleConns != nil {
		uopts.MinIdleConns = *def.MinIdleConns
	}
	if cfg.IsExpressionProvided(def.DialTimeout) {
		d, dDiags := config.ParseDuration(def.DialTimeout)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		uopts.DialTimeout = d
	}

	client := goredis.NewUniversalClient(uopts)

	wrapper := &RedisClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		client: client,
	}

	config.Startables = append(config.Startables, wrapper)
	config.Stoppables = append(config.Stoppables, wrapper)

	return wrapper, nil
}

// CtyValue exposes the base client as a plain client capsule. Child clients
// extract the RedisConnector via a type assertion; HCL expressions that
// reference `client.<name>` directly from user code should be rare (the
// base block is not itself a bus.Subscriber and has no messaging behavior),
// but the capsule form keeps it addressable for dep-graph resolution.
func (c *RedisClient) CtyValue() cty.Value {
	return cfg.NewClientCapsule(c)
}

// validateModeAndAddrs enforces the mode-specific attribute rules and
// returns the address list to feed into UniversalOptions.
func validateModeAndAddrs(mode string, def *RedisConnectionDefinition) ([]string, hcl.Diagnostics) {
	switch mode {
	case "standalone":
		if def.Address == "" {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: address is required",
				Detail:   "standalone mode requires the `address` attribute (e.g. \"localhost:6379\")",
				Subject:  &def.DefRange,
			}}
		}
		if len(def.Addresses) > 0 {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: addresses not valid in standalone mode",
				Detail:   "use `address` for standalone mode; `addresses` is for cluster/sentinel",
				Subject:  &def.DefRange,
			}}
		}
		if def.MasterName != "" {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: master_name only valid in sentinel mode",
				Subject:  &def.DefRange,
			}}
		}
		return []string{def.Address}, nil

	case "cluster":
		if def.Address != "" {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: address not valid in cluster mode",
				Detail:   "use `addresses = [...]` in cluster mode",
				Subject:  &def.DefRange,
			}}
		}
		if len(def.Addresses) == 0 {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: cluster mode requires `addresses`",
				Subject:  &def.DefRange,
			}}
		}
		if def.MasterName != "" {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: master_name only valid in sentinel mode",
				Subject:  &def.DefRange,
			}}
		}
		if def.Database != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: database not supported in cluster mode",
				Detail:   "cluster mode uses database 0 on every node",
				Subject:  &def.DefRange,
			}}
		}
		return def.Addresses, nil

	case "sentinel":
		if def.Address != "" {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: address not valid in sentinel mode",
				Detail:   "use `addresses = [...]` in sentinel mode",
				Subject:  &def.DefRange,
			}}
		}
		if len(def.Addresses) == 0 {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: sentinel mode requires `addresses`",
				Detail:   "list the sentinel node addresses in `addresses = [...]`",
				Subject:  &def.DefRange,
			}}
		}
		if def.MasterName == "" {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis: sentinel mode requires `master_name`",
				Subject:  &def.DefRange,
			}}
		}
		return def.Addresses, nil

	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis: invalid mode",
			Detail:   fmt.Sprintf("mode must be \"standalone\", \"cluster\", or \"sentinel\"; got %q", mode),
			Subject:  &def.DefRange,
		}}
	}
}

func evalOptionalString(config *cfg.Config, expr hcl.Expression, name string) (string, hcl.Diagnostics) {
	if !cfg.IsExpressionProvided(expr) {
		return "", nil
	}
	val, diags := expr.Value(config.EvalCtx())
	if diags.HasErrors() {
		return "", diags
	}
	if val.IsNull() {
		return "", nil
	}
	if val.Type() != cty.String {
		r := expr.Range()
		return "", hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("redis: %s must be a string", name),
			Subject:  &r,
		}}
	}
	return val.AsString(), nil
}
