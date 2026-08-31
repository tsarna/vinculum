// Package otlp provides the client "otlp" implementation, which configures the
// OpenTelemetry SDK with an OTLP/HTTP trace exporter.
package otlp

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/contrib/processors/baggagecopy"
	"go.opentelemetry.io/otel"
	otelbaggage "go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

func init() {
	cfg.RegisterClientType("otlp", process, cfg.WithSchema(otlpClientSchema))
}

// ─── HCL definition struct ────────────────────────────────────────────────────

var otlpClientSchema = cfg.TypeSchema{
	Sample:  &otlpClientDefinition{},
	Summary: "An OpenTelemetry exporter for traces and metrics.",
	DocPage: "client-otlp.md",
	Doc: `Exports spans, and optionally metrics, to an OTLP collector. Blocks that emit
telemetry reference it as ` + "`tracing = client.<name>`" + ` or
` + "`metrics = client.<name>`" + `, or pick it up automatically when it is the only
backend of its kind.`,
	Attrs: map[string]cfg.AttrMeta{
		"endpoint": {
			Summary: "OTLP collector endpoint to export traces to.",
			Doc: "Give the collector's base URL — `http://collector:4318` — and each signal's " +
				"default OTLP path is appended to it: `/v1/traces` here, `/v1/metrics` for " +
				"`metric_endpoint`. That is what lets one value serve both signals. An endpoint " +
				"written with a path of its own is used exactly as given, for a collector behind " +
				"a prefix or a vendor endpoint that is not spec-shaped.",
			Hint: cfg.HintURL,
		},
		"service_name": {
			Summary: "Value of the `service.name` resource attribute.",
			Doc:     "This is how the exported telemetry identifies this process.",
		},
		"service_version": {
			Summary: "Value of the `service.version` resource attribute.",
		},
		"sampling_ratio": {
			Summary: "Fraction of traces to sample, from 0 to 1.",
			Doc: "Head-based, and applied when a root span starts — a span with a sampled " +
				"parent is kept regardless.",
			Default: "1.0",
		},
		"default": {
			Summary: "Make this the default tracing backend.",
			Doc:     "Blocks that emit traces without naming one use the default. A single otlp client is the default automatically.",
			Hint:    cfg.HintBool,
		},
		"headers": {
			Summary: "Headers sent with each export request.",
			Doc:     "Typically an API key for a hosted collector.",
		},
		"record_baggage": {
			Summary: "Baggage keys to copy onto each span as attributes.",
			Doc:     "Nothing is copied when omitted, since baggage can carry data that should not reach a trace backend.",
			Default: "[]",
		},
		"metric_endpoint": {
			Summary: "Separate endpoint for metrics.",
			Doc: "`endpoint` is used when omitted. Setting either this or `metric_interval` " +
				"is what enables metric export at all. Paths work as they do for `endpoint`, " +
				"except that the default appended to a path-less value is `/v1/metrics`.",
			Hint: cfg.HintURL,
		},
		"metric_interval": {
			Summary: "How often metrics are pushed to the collector.",
			Hint:    cfg.HintDuration,
			Default: "60s",
		},
		"include_go_metrics": {
			Summary: "Export Go runtime metrics.",
			Doc:     "Set false to omit goroutine, memory, and GC metrics.",
			Hint:    cfg.HintBool,
			Default: "true",
		},
		"default_metrics": {
			Summary: "Make this the default metrics backend.",
			Doc:     "The same rule as `default`, but for metrics: at most one backend — this or a `server \"metrics\"` — may claim it.",
			Hint:    cfg.HintBool,
		},
	},
}

type otlpClientDefinition struct {
	Endpoint       string         `hcl:"endpoint"`
	ServiceName    string         `hcl:"service_name"`
	ServiceVersion string         `hcl:"service_version,optional"`
	SamplingRatio  *float64       `hcl:"sampling_ratio,optional"`
	Default        bool           `hcl:"default,optional"`
	Headers        hcl.Expression `hcl:"headers,optional"`
	RecordBaggage  []string       `hcl:"record_baggage,optional"`
	TLS            *cfg.TLSConfig `hcl:"tls,block"`
	DefRange       hcl.Range      `hcl:",def_range"`

	MetricEndpoint   *string `hcl:"metric_endpoint,optional"`
	MetricInterval   *string `hcl:"metric_interval,optional"`
	IncludeGoMetrics *bool   `hcl:"include_go_metrics,optional"`
	DefaultMetrics   bool    `hcl:"default_metrics,optional"`
}

// ─── Client struct ────────────────────────────────────────────────────────────

// OtlpClientImpl is the runtime representation of a client "otlp" block.
type OtlpClientImpl struct {
	cfg.BaseClient
	endpoint       string
	serviceName    string
	serviceVersion string
	samplingRatio  float64
	headers        map[string]string
	recordBaggage  []string
	tlsConfig      *cfg.TLSConfig
	baseDir        string
	isDefault      bool

	metricEndpoint   string
	metricInterval   time.Duration
	includeGoMetrics bool
	isDefaultMetrics bool

	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
}

// ─── cfg.OtlpClient interface ─────────────────────────────────────────────────

func (c *OtlpClientImpl) GetTracerProvider() trace.TracerProvider {
	return c.tracerProvider
}

func (c *OtlpClientImpl) IsDefaultClient() bool {
	return c.isDefault
}

func (c *OtlpClientImpl) GetMeterProvider() otelmetric.MeterProvider {
	return c.meterProvider
}

func (c *OtlpClientImpl) IsDefaultMetricsBackend() bool {
	return c.isDefaultMetrics
}

// Default OTLP/HTTP paths per signal, from the OTLP specification. Add one
// here for any further HTTP exporter (logs, profiles) wired up later.
const (
	defaultTracesPath  = "/v1/traces"
	defaultMetricsPath = "/v1/metrics"
)

// withSignalPath appends the signal's default OTLP path when the configured
// endpoint carries none, matching the spec convention that a generic endpoint
// gets the signal path appended while an explicit one is used verbatim.
//
// The exporters used to do this themselves. Since v1.45.0 WithEndpointURL
// pins a path-less URL to "/" instead, so `endpoint = "http://collector:4318"`
// POSTed to the collector's root and got a 404 for every export. Doing it here
// rather than telling users to spell the path out keeps a single `endpoint`
// usable for both signals: it feeds traces and metrics alike, so no one literal
// value could satisfy both.
//
// Only an empty path counts as "no path" — an explicit trailing "/" targeted
// the root before v1.45.0 too, so it is left alone.
func withSignalPath(endpoint, defaultPath string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Path != "" || u.Opaque != "" {
		return endpoint
	}
	u.Path = defaultPath
	return u.String()
}

// buildProviders constructs the trace and meter providers. It is called during
// the config Process phase so that metric blocks (which run after this client
// in dependency order) can obtain a non-nil MeterProvider.
func (c *OtlpClientImpl) buildProviders() error {
	ctx := context.Background()

	// --- Build exporter options ---
	traceOptions := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(withSignalPath(c.endpoint, defaultTracesPath)),
	}

	if c.tlsConfig != nil {
		tlsCfg, err := c.tlsConfig.BuildTLSClientConfig(c.baseDir)
		if err != nil {
			return fmt.Errorf("TLS config: %w", err)
		}
		if tlsCfg != nil {
			traceOptions = append(traceOptions, otlptracehttp.WithTLSClientConfig(tlsCfg))
		}
	}

	if len(c.headers) > 0 {
		traceOptions = append(traceOptions, otlptracehttp.WithHeaders(c.headers))
	}

	// --- Resource ---
	res, err := sdkresource.New(ctx,
		sdkresource.WithAttributes(
			semconv.ServiceName(c.serviceName),
			semconv.ServiceVersion(c.serviceVersion),
		),
		sdkresource.WithOS(),
		sdkresource.WithProcess(),
	)
	if err != nil {
		res = sdkresource.Default()
	}

	// --- Trace provider ---
	traceExporter, err := otlptracehttp.New(ctx, traceOptions...)
	if err != nil {
		return fmt.Errorf("trace exporter: %w", err)
	}

	samplingRatio := 1.0
	if c.samplingRatio != 0 {
		samplingRatio = c.samplingRatio
	}

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(samplingRatio))),
	}
	// Project selected baggage entries onto every locally-started span as
	// attributes named exactly those keys.
	if len(c.recordBaggage) > 0 {
		allow := make(map[string]struct{}, len(c.recordBaggage))
		for _, k := range c.recordBaggage {
			allow[k] = struct{}{}
		}
		filter := func(m otelbaggage.Member) bool {
			_, ok := allow[m.Key()]
			return ok
		}
		tpOpts = append(tpOpts, sdktrace.WithSpanProcessor(baggagecopy.NewSpanProcessor(filter)))
	}
	c.tracerProvider = sdktrace.NewTracerProvider(tpOpts...)

	// --- Metric provider ---
	metricEndpoint := c.metricEndpoint
	if metricEndpoint == "" {
		metricEndpoint = c.endpoint
	}

	metricOptions := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpointURL(withSignalPath(metricEndpoint, defaultMetricsPath)),
	}

	if c.tlsConfig != nil {
		tlsCfg, err := c.tlsConfig.BuildTLSClientConfig(c.baseDir)
		if err != nil {
			return fmt.Errorf("metric TLS config: %w", err)
		}
		if tlsCfg != nil {
			metricOptions = append(metricOptions, otlpmetrichttp.WithTLSClientConfig(tlsCfg))
		}
	}

	if len(c.headers) > 0 {
		metricOptions = append(metricOptions, otlpmetrichttp.WithHeaders(c.headers))
	}

	metricExporter, err := otlpmetrichttp.New(ctx, metricOptions...)
	if err != nil {
		return fmt.Errorf("metric exporter: %w", err)
	}

	c.meterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
			sdkmetric.WithInterval(c.metricInterval),
		)),
		sdkmetric.WithResource(res),
	)

	return nil
}

// ─── Startable / Stoppable ────────────────────────────────────────────────────

func (c *OtlpClientImpl) Start() error {
	// Set globals so otel.Tracer() / otel.GetTextMapPropagator() resolve correctly.
	otel.SetTracerProvider(c.tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	otel.SetMeterProvider(c.meterProvider)

	if c.includeGoMetrics {
		if err := runtime.Start(runtime.WithMeterProvider(c.meterProvider)); err != nil {
			return fmt.Errorf("client \"otlp\" %q: runtime metrics: %w", c.Name, err)
		}
	}

	return nil
}

func (c *OtlpClientImpl) Stop() error {
	if c.meterProvider != nil {
		if err := c.meterProvider.Shutdown(context.Background()); err != nil {
			return fmt.Errorf("client \"otlp\" %q: metric shutdown: %w", c.Name, err)
		}
	}
	if c.tracerProvider != nil {
		if err := c.tracerProvider.Shutdown(context.Background()); err != nil {
			return fmt.Errorf("client \"otlp\" %q: trace shutdown: %w", c.Name, err)
		}
	}
	return nil
}

// ─── Block processor ──────────────────────────────────────────────────────────

func process(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Client, hcl.Diagnostics) {
	name := block.Labels[1]

	var def otlpClientDefinition
	diags := cfg.DecodeBody(body, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

	// Evaluate optional headers expression → map(string)
	headers := map[string]string{}
	if def.Headers != nil {
		val, valDiags := def.Headers.Value(config.EvalCtx())
		if valDiags.HasErrors() {
			return nil, valDiags
		}
		if !val.IsNull() && val.Type().IsMapType() && val.IsKnown() {
			for k, v := range val.AsValueMap() {
				if v.Type() == cty.String && v.IsKnown() && !v.IsNull() {
					headers[k] = v.AsString()
				}
			}
		}
	}

	samplingRatio := 1.0
	if def.SamplingRatio != nil {
		samplingRatio = *def.SamplingRatio
	}

	// Validate record_baggage keys at parse time.
	for _, k := range def.RecordBaggage {
		if k == "" {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid record_baggage key",
					Detail:   fmt.Sprintf("client \"otlp\" %q: record_baggage contains an empty key", name),
					Subject:  def.DefRange.Ptr(),
				},
			}
		}
	}

	metricInterval := 60 * time.Second
	if def.MetricInterval != nil {
		d, err := time.ParseDuration(*def.MetricInterval)
		if err != nil {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid metric_interval",
					Detail:   fmt.Sprintf("client \"otlp\" %q: metric_interval: %s", name, err),
					Subject:  def.DefRange.Ptr(),
				},
			}
		}
		metricInterval = d
	}

	var metricEndpoint string
	if def.MetricEndpoint != nil {
		metricEndpoint = *def.MetricEndpoint
	}

	includeGoMetrics := true
	if def.IncludeGoMetrics != nil {
		includeGoMetrics = *def.IncludeGoMetrics
	}

	client := &OtlpClientImpl{
		BaseClient: cfg.BaseClient{
			Name:     name,
			DefRange: def.DefRange,
		},
		endpoint:         def.Endpoint,
		serviceName:      def.ServiceName,
		serviceVersion:   def.ServiceVersion,
		samplingRatio:    samplingRatio,
		headers:          headers,
		recordBaggage:    def.RecordBaggage,
		tlsConfig:        def.TLS,
		baseDir:          config.BaseDir,
		isDefault:        def.Default,
		metricEndpoint:   metricEndpoint,
		metricInterval:   metricInterval,
		includeGoMetrics: includeGoMetrics,
		isDefaultMetrics: def.DefaultMetrics,
	}

	if err := client.buildProviders(); err != nil {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to build OTLP providers",
				Detail:   fmt.Sprintf("client \"otlp\" %q: %s", name, err),
				Subject:  def.DefRange.Ptr(),
			},
		}
	}

	if config.OtlpClients == nil {
		config.OtlpClients = make(map[string]cfg.OtlpClient)
	}
	config.OtlpClients[name] = client

	config.Startables = append(config.Startables, client)
	config.Stoppables = append(config.Stoppables, client)

	return client, nil
}
