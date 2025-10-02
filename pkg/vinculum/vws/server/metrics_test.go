package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tsarna/vinculum-bus/o11y"
)

// testMetricsProvider implements MetricsProvider for testing
type testMetricsProvider struct {
	counters   map[string]*testCounter
	histograms map[string]*testHistogram
	gauges     map[string]*testGauge
	mu         sync.RWMutex
}

func newTestMetricsProvider() *testMetricsProvider {
	return &testMetricsProvider{
		counters:   make(map[string]*testCounter),
		histograms: make(map[string]*testHistogram),
		gauges:     make(map[string]*testGauge),
	}
}

func (p *testMetricsProvider) Counter(name string) o11y.Counter {
	p.mu.Lock()
	defer p.mu.Unlock()
	if counter, exists := p.counters[name]; exists {
		return counter
	}
	counter := &testCounter{}
	p.counters[name] = counter
	return counter
}

func (p *testMetricsProvider) Histogram(name string) o11y.Histogram {
	p.mu.Lock()
	defer p.mu.Unlock()
	if histogram, exists := p.histograms[name]; exists {
		return histogram
	}
	histogram := &testHistogram{}
	p.histograms[name] = histogram
	return histogram
}

func (p *testMetricsProvider) Gauge(name string) o11y.Gauge {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gauge, exists := p.gauges[name]; exists {
		return gauge
	}
	gauge := &testGauge{}
	p.gauges[name] = gauge
	return gauge
}

// Test metric implementations
type testCounter struct {
	value  int64
	labels []o11y.Label
	mu     sync.RWMutex
}

func (c *testCounter) Add(ctx context.Context, value int64, labels ...o11y.Label) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value += value
	c.labels = labels
}

func (c *testCounter) getValue() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.value
}

func (c *testCounter) getLabels() []o11y.Label {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.labels
}

type testHistogram struct {
	values []float64
	labels []o11y.Label
	mu     sync.RWMutex
}

func (h *testHistogram) Record(ctx context.Context, value float64, labels ...o11y.Label) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.values = append(h.values, value)
	h.labels = labels
}

func (h *testHistogram) getValues() []float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]float64(nil), h.values...)
}

func (h *testHistogram) getLabels() []o11y.Label {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.labels
}

type testGauge struct {
	value  float64
	labels []o11y.Label
	mu     sync.RWMutex
}

func (g *testGauge) Set(ctx context.Context, value float64, labels ...o11y.Label) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value = value
	g.labels = labels
}

func (g *testGauge) getValue() float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.value
}

func (g *testGauge) getLabels() []o11y.Label {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.labels
}

func TestNewWebSocketMetrics(t *testing.T) {
	t.Run("with provider", func(t *testing.T) {
		provider := newTestMetricsProvider()
		metrics := NewWebSocketMetrics(provider)

		if metrics == nil {
			t.Fatal("Expected metrics to be non-nil")
		}

		// Verify all metrics are initialized
		if metrics.activeConnections == nil {
			t.Error("activeConnections should be initialized")
		}
		if metrics.totalConnections == nil {
			t.Error("totalConnections should be initialized")
		}
		if metrics.connectionDuration == nil {
			t.Error("connectionDuration should be initialized")
		}
		if metrics.messagesReceived == nil {
			t.Error("messagesReceived should be initialized")
		}
		if metrics.messagesSent == nil {
			t.Error("messagesSent should be initialized")
		}
	})

	t.Run("with nil provider", func(t *testing.T) {
		metrics := NewWebSocketMetrics(nil)
		if metrics != nil {
			t.Error("Expected metrics to be nil when provider is nil")
		}
	})
}

func TestWebSocketMetrics_ConnectionLifecycle(t *testing.T) {
	provider := newTestMetricsProvider()
	metrics := NewWebSocketMetrics(provider)
	ctx := context.Background()

	// Test connection start
	metrics.RecordConnectionStart(ctx)

	totalConn := provider.counters["websocket_connections_total"]
	if totalConn.getValue() != 1 {
		t.Errorf("Expected total connections to be 1, got %d", totalConn.getValue())
	}

	// Test active connections
	metrics.RecordConnectionActive(ctx, 5)

	activeConn := provider.gauges["websocket_active_connections"]
	if activeConn.getValue() != 5.0 {
		t.Errorf("Expected active connections to be 5.0, got %f", activeConn.getValue())
	}

	// Test connection end
	duration := 30 * time.Second
	metrics.RecordConnectionEnd(ctx, duration)

	connDuration := provider.histograms["websocket_connection_duration_seconds"]
	values := connDuration.getValues()
	if len(values) != 1 {
		t.Errorf("Expected 1 duration value, got %d", len(values))
	}
	if values[0] != 30.0 {
		t.Errorf("Expected duration to be 30.0 seconds, got %f", values[0])
	}

	// Test connection error
	metrics.RecordConnectionError(ctx, "upgrade_failed")

	connErrors := provider.counters["websocket_connection_errors_total"]
	if connErrors.getValue() != 1 {
		t.Errorf("Expected connection errors to be 1, got %d", connErrors.getValue())
	}

	labels := connErrors.getLabels()
	if len(labels) != 1 || labels[0].Key != "error_type" || labels[0].Value != "upgrade_failed" {
		t.Errorf("Expected error_type label with value 'upgrade_failed', got %v", labels)
	}
}

func TestWebSocketMetrics_Messages(t *testing.T) {
	provider := newTestMetricsProvider()
	metrics := NewWebSocketMetrics(provider)
	ctx := context.Background()

	// Test message received
	metrics.RecordMessageReceived(ctx, 256, "subscribe")

	msgReceived := provider.counters["websocket_messages_received_total"]
	if msgReceived.getValue() != 1 {
		t.Errorf("Expected messages received to be 1, got %d", msgReceived.getValue())
	}

	labels := msgReceived.getLabels()
	if len(labels) != 1 || labels[0].Key != "kind" || labels[0].Value != "subscribe" {
		t.Errorf("Expected kind label with value 'subscribe', got %v", labels)
	}

	msgSize := provider.histograms["websocket_message_size_bytes"]
	values := msgSize.getValues()
	if len(values) != 1 || values[0] != 256.0 {
		t.Errorf("Expected message size to be 256.0, got %v", values)
	}

	// Test message sent
	metrics.RecordMessageSent(ctx, 512, "event")

	msgSent := provider.counters["websocket_messages_sent_total"]
	if msgSent.getValue() != 1 {
		t.Errorf("Expected messages sent to be 1, got %d", msgSent.getValue())
	}

	// Test message error
	metrics.RecordMessageError(ctx, "parse_error", "unknown")

	msgErrors := provider.counters["websocket_message_errors_total"]
	if msgErrors.getValue() != 1 {
		t.Errorf("Expected message errors to be 1, got %d", msgErrors.getValue())
	}

	errorLabels := msgErrors.getLabels()
	expectedLabels := map[string]string{
		"error_type": "parse_error",
		"kind":       "unknown",
	}

	if len(errorLabels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(errorLabels))
	}

	for _, label := range errorLabels {
		if expectedValue, exists := expectedLabels[label.Key]; !exists || expectedValue != label.Value {
			t.Errorf("Unexpected label %s=%s", label.Key, label.Value)
		}
	}
}

func TestWebSocketMetrics_Requests(t *testing.T) {
	provider := newTestMetricsProvider()
	metrics := NewWebSocketMetrics(provider)
	ctx := context.Background()

	// Test successful request
	recordCompletion := metrics.RecordRequest(ctx, "subscribe")

	// Simulate some processing time
	time.Sleep(10 * time.Millisecond)

	recordCompletion(nil) // No error

	requestsTotal := provider.counters["websocket_requests_total"]
	if requestsTotal.getValue() != 1 {
		t.Errorf("Expected requests total to be 1, got %d", requestsTotal.getValue())
	}

	requestDuration := provider.histograms["websocket_request_duration_seconds"]
	durations := requestDuration.getValues()
	if len(durations) != 1 {
		t.Errorf("Expected 1 duration value, got %d", len(durations))
	}
	if durations[0] < 0.01 { // Should be at least 10ms
		t.Errorf("Expected duration to be at least 0.01 seconds, got %f", durations[0])
	}

	// Test request with error
	recordCompletion2 := metrics.RecordRequest(ctx, "event")
	recordCompletion2(errors.New("topic required")) // With error

	requestErrors := provider.counters["websocket_request_errors_total"]
	if requestErrors.getValue() != 1 {
		t.Errorf("Expected request errors to be 1, got %d", requestErrors.getValue())
	}
}

func TestWebSocketMetrics_Health(t *testing.T) {
	provider := newTestMetricsProvider()
	metrics := NewWebSocketMetrics(provider)
	ctx := context.Background()

	// Test ping sent
	metrics.RecordPingSent(ctx)

	pingsSent := provider.counters["websocket_pings_sent_total"]
	if pingsSent.getValue() != 1 {
		t.Errorf("Expected pings sent to be 1, got %d", pingsSent.getValue())
	}

	// Test pong timeout
	metrics.RecordPongTimeout(ctx)

	pongTimeouts := provider.counters["websocket_pong_timeouts_total"]
	if pongTimeouts.getValue() != 1 {
		t.Errorf("Expected pong timeouts to be 1, got %d", pongTimeouts.getValue())
	}

	// Test write timeout
	metrics.RecordWriteTimeout(ctx)

	writeTimeouts := provider.counters["websocket_write_timeouts_total"]
	if writeTimeouts.getValue() != 1 {
		t.Errorf("Expected write timeouts to be 1, got %d", writeTimeouts.getValue())
	}
}

func TestWebSocketMetrics_NilSafety(t *testing.T) {
	// Test that all methods are safe to call on nil metrics
	var metrics *WebSocketMetrics
	ctx := context.Background()

	// These should not panic
	metrics.RecordConnectionStart(ctx)
	metrics.RecordConnectionActive(ctx, 1)
	metrics.RecordConnectionEnd(ctx, time.Second)
	metrics.RecordConnectionError(ctx, "test")
	metrics.RecordMessageReceived(ctx, 100, "test")
	metrics.RecordMessageSent(ctx, 100, "test")
	metrics.RecordMessageError(ctx, "test", "test")
	metrics.RecordPingSent(ctx)
	metrics.RecordPongTimeout(ctx)
	metrics.RecordWriteTimeout(ctx)

	// Request recording should return a no-op function
	recordCompletion := metrics.RecordRequest(ctx, "test")
	recordCompletion(nil) // Should not panic
}

func TestWebSocketMetrics_ConcurrentAccess(t *testing.T) {
	provider := newTestMetricsProvider()
	metrics := NewWebSocketMetrics(provider)
	ctx := context.Background()

	// Test concurrent access to metrics
	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				metrics.RecordConnectionStart(ctx)
				metrics.RecordMessageReceived(ctx, 100, "test")
				metrics.RecordMessageSent(ctx, 100, "test")
				recordCompletion := metrics.RecordRequest(ctx, "test")
				recordCompletion(nil)
			}
		}()
	}

	wg.Wait()

	// Verify final counts
	totalConn := provider.counters["websocket_connections_total"]
	expectedTotal := int64(numGoroutines * numOperations)
	if totalConn.getValue() != expectedTotal {
		t.Errorf("Expected total connections to be %d, got %d", expectedTotal, totalConn.getValue())
	}

	msgReceived := provider.counters["websocket_messages_received_total"]
	if msgReceived.getValue() != expectedTotal {
		t.Errorf("Expected messages received to be %d, got %d", expectedTotal, msgReceived.getValue())
	}

	msgSent := provider.counters["websocket_messages_sent_total"]
	if msgSent.getValue() != expectedTotal {
		t.Errorf("Expected messages sent to be %d, got %d", expectedTotal, msgSent.getValue())
	}

	requestsTotal := provider.counters["websocket_requests_total"]
	if requestsTotal.getValue() != expectedTotal {
		t.Errorf("Expected requests total to be %d, got %d", expectedTotal, requestsTotal.getValue())
	}
}
