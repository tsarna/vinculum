package server_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/o11y"
	"github.com/tsarna/vinculum/pkg/vinculum/vws/server"
	"go.uber.org/zap"
)

// ExampleWebSocketMetrics demonstrates how to set up a WebSocket server with metrics collection.
func ExampleWebSocketMetrics() {
	// Create a logger
	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	// Create an EventBus (using the default implementation)
	eventBus, err := bus.NewEventBus().
		WithLogger(logger).
		WithBufferSize(1000).
		Build()
	if err != nil {
		log.Fatalf("Build() returned error: %v", err)
	}
	defer eventBus.Stop()

	// Start the EventBus
	if err := eventBus.Start(); err != nil {
		log.Fatal(err)
	}

	// Create a metrics provider (using the standalone metrics provider)
	metricsProvider := o11y.NewStandaloneMetricsProvider(eventBus, &o11y.StandaloneMetricsConfig{
		Interval:     30 * time.Second,
		MetricsTopic: "$metrics",
		ServiceName:  "websocket-server",
	})

	// Start the metrics provider
	if err := metricsProvider.Start(); err != nil {
		log.Fatal(err)
	}
	defer metricsProvider.Stop()

	// Create the WebSocket listener with metrics
	listener, err := server.NewListener().
		WithEventBus(eventBus).
		WithLogger(logger).
		WithMetricsProvider(metricsProvider).
		WithQueueSize(512).
		WithPingInterval(30 * time.Second).
		WithEventAuth(server.AllowTopicPrefix("client/")).
		Build()
	if err != nil {
		log.Fatal(err)
	}

	// Set up HTTP server
	http.Handle("/ws", listener)

	// Start server
	server := &http.Server{
		Addr:    ":8080",
		Handler: http.DefaultServeMux,
	}

	fmt.Println("WebSocket server with metrics running on :8080")
	fmt.Println("Metrics will be published to topic '$metrics' every 30 seconds")
	fmt.Println("Connect to ws://localhost:8080/ws to test")

	// In a real application, you would handle graceful shutdown
	log.Fatal(server.ListenAndServe())
}

// Custom metrics provider that logs metrics instead of publishing them
type loggingMetricsProvider struct {
	logger *zap.Logger
}

func (p *loggingMetricsProvider) Counter(name string) o11y.Counter {
	return &loggingCounter{name: name, logger: p.logger}
}

func (p *loggingMetricsProvider) Histogram(name string) o11y.Histogram {
	return &loggingHistogram{name: name, logger: p.logger}
}

func (p *loggingMetricsProvider) Gauge(name string) o11y.Gauge {
	return &loggingGauge{name: name, logger: p.logger}
}

// Custom metric implementations that log instead of storing
type loggingCounter struct {
	name   string
	logger *zap.Logger
}

func (c *loggingCounter) Add(ctx context.Context, value int64, labels ...o11y.Label) {
	c.logger.Info("Counter incremented",
		zap.String("metric", c.name),
		zap.Int64("value", value),
		zap.Any("labels", labels),
	)
}

type loggingHistogram struct {
	name   string
	logger *zap.Logger
}

func (h *loggingHistogram) Record(ctx context.Context, value float64, labels ...o11y.Label) {
	h.logger.Info("Histogram recorded",
		zap.String("metric", h.name),
		zap.Float64("value", value),
		zap.Any("labels", labels),
	)
}

type loggingGauge struct {
	name   string
	logger *zap.Logger
}

func (g *loggingGauge) Set(ctx context.Context, value float64, labels ...o11y.Label) {
	g.logger.Info("Gauge set",
		zap.String("metric", g.name),
		zap.Float64("value", value),
		zap.Any("labels", labels),
	)
}

// ExampleListener_metricsWithCustomProvider shows how to use a custom metrics provider.
func ExampleListener_metricsWithCustomProvider() {

	// Create logger and EventBus
	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	eventBus, err := bus.NewEventBus().
		WithLogger(logger).
		WithBufferSize(1000).
		Build()
	if err != nil {
		log.Fatalf("Build() returned error: %v", err)
	}
	defer eventBus.Stop()

	if err := eventBus.Start(); err != nil {
		log.Fatal(err)
	}

	// Use custom logging metrics provider
	metricsProvider := &loggingMetricsProvider{logger: logger}

	// Create WebSocket listener
	listener, err := server.NewListener().
		WithEventBus(eventBus).
		WithLogger(logger).
		WithMetricsProvider(metricsProvider).
		Build()
	if err != nil {
		log.Fatal(err)
	}

	// Set up HTTP server
	http.Handle("/ws", listener)

	fmt.Println("WebSocket server with custom logging metrics provider running on :8080")
	fmt.Println("All metrics will be logged to the console")

	// Start server
	server := &http.Server{
		Addr:    ":8080",
		Handler: http.DefaultServeMux,
	}

	log.Fatal(server.ListenAndServe())
}

// ExampleListener_metricsDisabled shows how to run the WebSocket server without metrics.
func ExampleListener_metricsDisabled() {
	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	eventBus, err := bus.NewEventBus().
		WithLogger(logger).
		WithBufferSize(1000).
		Build()
	if err != nil {
		log.Fatalf("Build() returned error: %v", err)
	}
	defer eventBus.Stop()

	if err := eventBus.Start(); err != nil {
		log.Fatal(err)
	}

	// Create WebSocket listener without metrics provider
	listener, err := server.NewListener().
		WithEventBus(eventBus).
		WithLogger(logger).
		// No WithMetricsProvider() call - metrics will be disabled
		Build()
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/ws", listener)

	fmt.Println("WebSocket server running without metrics on :8080")

	server := &http.Server{
		Addr:    ":8080",
		Handler: http.DefaultServeMux,
	}

	log.Fatal(server.ListenAndServe())
}
