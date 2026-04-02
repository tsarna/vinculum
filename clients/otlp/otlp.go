// Package otlp provides the client "otlp" implementation, which configures the
// OpenTelemetry SDK with an OTLP/HTTP trace exporter.
package otlp

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

func init() {
	cfg.RegisterClientType("otlp", process)
}

// ─── HCL definition struct ────────────────────────────────────────────────────

type otlpClientDefinition struct {
	Endpoint       string         `hcl:"endpoint"`
	ServiceName    string         `hcl:"service_name"`
	ServiceVersion string         `hcl:"service_version,optional"`
	SamplingRatio  *float64       `hcl:"sampling_ratio,optional"`
	Default        bool           `hcl:"default,optional"`
	Headers        hcl.Expression `hcl:"headers,optional"`
	TLS            *cfg.TLSConfig `hcl:"tls,block"`
	DefRange       hcl.Range      `hcl:",def_range"`
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
	tlsConfig      *cfg.TLSConfig
	baseDir        string
	isDefault      bool

	tracerProvider *sdktrace.TracerProvider
}

// ─── cfg.OtlpClient interface ─────────────────────────────────────────────────

func (c *OtlpClientImpl) GetTracerProvider() trace.TracerProvider {
	return c.tracerProvider
}

func (c *OtlpClientImpl) IsDefaultClient() bool {
	return c.isDefault
}

// ─── Startable / Stoppable ────────────────────────────────────────────────────

func (c *OtlpClientImpl) Start() error {
	ctx := context.Background()

	// --- Build exporter options ---
	traceOptions := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(c.endpoint),
	}

	if c.tlsConfig != nil {
		tlsCfg, err := c.tlsConfig.BuildTLSClientConfig(c.baseDir)
		if err != nil {
			return fmt.Errorf("client \"otlp\" %q: TLS config: %w", c.Name, err)
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
		return fmt.Errorf("client \"otlp\" %q: trace exporter: %w", c.Name, err)
	}

	samplingRatio := 1.0
	if c.samplingRatio != 0 {
		samplingRatio = c.samplingRatio
	}

	c.tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(samplingRatio))),
	)

	// Set globals so otel.Tracer() / otel.GetTextMapPropagator() resolve correctly.
	otel.SetTracerProvider(c.tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return nil
}

func (c *OtlpClientImpl) Stop() error {
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
	diags := gohcl.DecodeBody(body, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}

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

	client := &OtlpClientImpl{
		BaseClient: cfg.BaseClient{
			Name:     name,
			DefRange: def.DefRange,
		},
		endpoint:       def.Endpoint,
		serviceName:    def.ServiceName,
		serviceVersion: def.ServiceVersion,
		samplingRatio:  samplingRatio,
		headers:        headers,
		tlsConfig:      def.TLS,
		baseDir:        config.BaseDir,
		isDefault:      def.Default,
	}

	if config.OtlpClients == nil {
		config.OtlpClients = make(map[string]cfg.OtlpClient)
	}
	config.OtlpClients[name] = client

	config.Startables = append(config.Startables, client)
	config.Stoppables = append(config.Stoppables, client)

	return client, nil
}
