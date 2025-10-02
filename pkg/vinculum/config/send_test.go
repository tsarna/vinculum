package config

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum-bus"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

const (
	timeout  = 1 * time.Second
	interval = 10 * time.Millisecond
)

// mockSubscriber captures published messages for testing
type mockSubscriber struct {
	bus.BaseSubscriber
	messages []mockMessage
}

type mockMessage struct {
	topic   string
	payload any
	fields  map[string]string
}

func (m *mockSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	m.messages = append(m.messages, mockMessage{
		topic:   topic,
		payload: message,
		fields:  fields,
	})
	return nil
}

func TestSendFunctions(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	// Create a test config with a bus
	config := &Config{
		Logger:         logger,
		Buses:          make(map[string]bus.EventBus),
		CtyBusMap:      make(map[string]cty.Value),
		Constants:      make(map[string]cty.Value),
		evalCtx:        &hcl.EvalContext{},
		BusCapsuleType: EventBusCapsuleType,
	}

	// Create a test bus
	eventBus, err := bus.NewEventBus().WithLogger(logger).WithName("test").Build()
	require.NoError(t, err)

	err = eventBus.Start()
	require.NoError(t, err)
	defer eventBus.Stop()

	config.Buses["test"] = eventBus
	config.CtyBusMap["test"] = NewEventBusCapsule(eventBus)

	// Create mock subscriber
	subscriber := &mockSubscriber{}
	err = eventBus.Subscribe(context.Background(), subscriber, "test/topic")
	require.NoError(t, err)

	// Create subscriber capsule for direct testing
	subscriberValue := NewSubscriberCapsule(subscriber)

	// Test data - using cty values directly to avoid conversion issues
	testCtyValue := cty.ObjectVal(map[string]cty.Value{
		"message": cty.StringVal("hello world"),
		"count":   cty.NumberIntVal(42),
		"active":  cty.BoolVal(true),
	})

	// Create context object
	ctx := context.Background()
	ctxValue, diags := NewContext(ctx).Build()
	require.False(t, diags.HasErrors(), "Context build should not have errors")
	busValue := config.CtyBusMap["test"]
	topicValue := cty.StringVal("test/topic")

	t.Run("send function (original)", func(t *testing.T) {
		subscriber.messages = nil // Reset messages

		sendFunc := SendFunction(config)
		result, err := sendFunc.Call([]cty.Value{ctxValue, busValue, topicValue, testCtyValue})

		require.NoError(t, err)
		assert.True(t, result.True())

		// Give some time for async processing
		assert.Eventually(t, func() bool {
			return len(subscriber.messages) > 0
		}, timeout, interval)

		// The original send should pass the cty.Value as-is
		assert.Equal(t, "test/topic", subscriber.messages[0].topic)
		assert.Equal(t, testCtyValue, subscriber.messages[0].payload)
	})

	t.Run("sendjson function", func(t *testing.T) {
		subscriber.messages = nil // Reset messages

		sendJSONFunc := SendJSONFunction(config)
		result, err := sendJSONFunc.Call([]cty.Value{ctxValue, busValue, topicValue, testCtyValue})

		require.NoError(t, err)
		assert.True(t, result.True())

		// Give some time for async processing
		assert.Eventually(t, func() bool {
			return len(subscriber.messages) > 0
		}, timeout, interval)

		// The sendjson should convert to JSON bytes
		assert.Equal(t, "test/topic", subscriber.messages[0].topic)
		jsonBytes, ok := subscriber.messages[0].payload.([]byte)
		require.True(t, ok, "Expected payload to be JSON bytes")

		// Verify it's valid JSON by unmarshaling
		var unmarshaled any
		err = json.Unmarshal(jsonBytes, &unmarshaled)
		require.NoError(t, err)

		// Verify the content matches our expected structure
		expected := map[string]any{
			"message": "hello world",
			"count":   float64(42), // JSON numbers become float64
			"active":  true,
		}
		assert.Equal(t, expected, unmarshaled)
	})

	t.Run("sendgo function", func(t *testing.T) {
		subscriber.messages = nil // Reset messages

		sendGoFunc := SendGoFunction(config)
		result, err := sendGoFunc.Call([]cty.Value{ctxValue, busValue, topicValue, testCtyValue})

		require.NoError(t, err)
		assert.True(t, result.True())

		// Give some time for async processing
		assert.Eventually(t, func() bool {
			return len(subscriber.messages) > 0
		}, timeout, interval)

		// The sendgo should convert to Go native types
		assert.Equal(t, "test/topic", subscriber.messages[0].topic)
		goValue, ok := subscriber.messages[0].payload.(map[string]any)
		require.True(t, ok, "Expected payload to be a Go map")

		// Verify the content matches our expected structure
		expectedGo := map[string]any{
			"message": "hello world",
			"count":   int64(42), // Our custom converter returns int64 for exact integers
			"active":  true,
		}
		assert.Equal(t, expectedGo, goValue)
	})

	t.Run("sendgo function with nested objects", func(t *testing.T) {
		subscriber.messages = nil // Reset messages

		// Create a complex nested structure
		nestedCtyValue := cty.ObjectVal(map[string]cty.Value{
			"user": cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("Alice"),
				"age":  cty.NumberIntVal(30),
				"profile": cty.ObjectVal(map[string]cty.Value{
					"email":    cty.StringVal("alice@example.com"),
					"verified": cty.BoolVal(true),
				}),
			}),
			"tags": cty.ListVal([]cty.Value{
				cty.StringVal("admin"),
				cty.StringVal("premium"),
			}),
			"scores": cty.TupleVal([]cty.Value{
				cty.NumberIntVal(95),
				cty.NumberFloatVal(87.5),
			}),
			"metadata": cty.ObjectVal(map[string]cty.Value{
				"source":    cty.StringVal("api"),
				"timestamp": cty.NumberIntVal(1234567890),
			}),
		})

		sendGoFunc := SendGoFunction(config)
		result, err := sendGoFunc.Call([]cty.Value{ctxValue, busValue, topicValue, nestedCtyValue})

		require.NoError(t, err)
		assert.True(t, result.True())

		// Give some time for async processing
		assert.Eventually(t, func() bool {
			return len(subscriber.messages) > 0
		}, timeout, interval)

		// The sendgo should convert to Go native types recursively
		assert.Equal(t, "test/topic", subscriber.messages[0].topic)
		goValue, ok := subscriber.messages[0].payload.(map[string]any)
		require.True(t, ok, "Expected payload to be a Go map")

		// Verify the nested structure
		expectedNested := map[string]any{
			"user": map[string]any{
				"name": "Alice",
				"age":  int64(30),
				"profile": map[string]any{
					"email":    "alice@example.com",
					"verified": true,
				},
			},
			"tags":   []any{"admin", "premium"},
			"scores": []any{int64(95), 87.5},
			"metadata": map[string]any{
				"source":    "api",
				"timestamp": int64(1234567890),
			},
		}
		assert.Equal(t, expectedNested, goValue)
	})

	t.Run("sendgo function with capsule types", func(t *testing.T) {
		subscriber.messages = nil // Reset messages

		// Create a structure containing a capsule (context)
		capsuleValue := cty.ObjectVal(map[string]cty.Value{
			"context": NewContextCapsule(context.Background()),
			"message": cty.StringVal("test with capsule"),
		})

		sendGoFunc := SendGoFunction(config)
		result, err := sendGoFunc.Call([]cty.Value{ctxValue, busValue, topicValue, capsuleValue})

		require.NoError(t, err)
		assert.True(t, result.True())

		// Give some time for async processing
		assert.Eventually(t, func() bool {
			return len(subscriber.messages) > 0
		}, timeout, interval)

		// The sendgo should unwrap capsule types
		assert.Equal(t, "test/topic", subscriber.messages[0].topic)
		goValue, ok := subscriber.messages[0].payload.(map[string]any)
		require.True(t, ok, "Expected payload to be a Go map")

		// Verify the capsule was unwrapped to the actual context pointer
		assert.Equal(t, "test with capsule", goValue["message"])
		contextPtr, ok := goValue["context"].(*context.Context)
		require.True(t, ok, "Expected context to be unwrapped to *context.Context")
		assert.NotNil(t, contextPtr, "Context pointer should not be nil")
	})

	t.Run("error handling - invalid context", func(t *testing.T) {
		sendFunc := SendFunction(config)
		invalidCtx := cty.StringVal("invalid")

		_, err := sendFunc.Call([]cty.Value{invalidCtx, busValue, topicValue, testCtyValue})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "context error")
	})

	t.Run("error handling - invalid bus", func(t *testing.T) {
		sendFunc := SendFunction(config)
		invalidBus := cty.StringVal("invalid")

		_, err := sendFunc.Call([]cty.Value{ctxValue, invalidBus, topicValue, testCtyValue})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected Subscriber capsule")
	})

	t.Run("send with fields - object", func(t *testing.T) {
		subscriber.messages = nil // Reset messages

		sendFunc := SendFunction(config)
		fieldsValue := cty.ObjectVal(map[string]cty.Value{
			"userId": cty.StringVal("123"),
			"action": cty.StringVal("login"),
			"count":  cty.NumberIntVal(42),
		})

		result, err := sendFunc.Call([]cty.Value{ctxValue, subscriberValue, topicValue, testCtyValue, fieldsValue})

		require.NoError(t, err)
		assert.Equal(t, cty.True, result)

		// Verify the message was sent with fields directly to subscriber
		require.Len(t, subscriber.messages, 1)
		msg := subscriber.messages[0]
		assert.Equal(t, "test/topic", msg.topic)
		assert.Equal(t, testCtyValue, msg.payload)
		require.NotNil(t, msg.fields)
		assert.Equal(t, "123", msg.fields["userId"])
		assert.Equal(t, "login", msg.fields["action"])
		assert.Equal(t, "42", msg.fields["count"]) // Numbers converted to string
	})

	t.Run("send with fields - map", func(t *testing.T) {
		subscriber.messages = nil // Reset
		sendFunc := SendFunction(config)
		fieldsValue := cty.MapVal(map[string]cty.Value{
			"sessionId": cty.StringVal("abc-123"),
			"timestamp": cty.StringVal("2023-01-01T00:00:00Z"),
		})

		result, err := sendFunc.Call([]cty.Value{ctxValue, subscriberValue, topicValue, testCtyValue, fieldsValue})

		require.NoError(t, err)
		assert.Equal(t, cty.True, result)

		// Verify the message was sent with fields directly to subscriber
		require.Len(t, subscriber.messages, 1)
		msg := subscriber.messages[0]
		assert.Equal(t, "test/topic", msg.topic)
		assert.Equal(t, testCtyValue, msg.payload)
		require.NotNil(t, msg.fields)
		assert.Equal(t, "abc-123", msg.fields["sessionId"])
		assert.Equal(t, "2023-01-01T00:00:00Z", msg.fields["timestamp"])
	})

	t.Run("send without fields - backward compatibility", func(t *testing.T) {
		subscriber.messages = nil // Reset
		sendFunc := SendFunction(config)

		result, err := sendFunc.Call([]cty.Value{ctxValue, subscriberValue, topicValue, testCtyValue})

		require.NoError(t, err)
		assert.Equal(t, cty.True, result)

		// Verify backward compatibility (no fields)
		require.Len(t, subscriber.messages, 1)
		msg := subscriber.messages[0]
		assert.Equal(t, "test/topic", msg.topic)
		assert.Equal(t, testCtyValue, msg.payload)
		assert.Nil(t, msg.fields) // Should be nil when no fields provided
	})

	t.Run("error handling - invalid fields type", func(t *testing.T) {
		sendFunc := SendFunction(config)
		invalidFields := cty.StringVal("not-a-map")

		_, err := sendFunc.Call([]cty.Value{ctxValue, subscriberValue, topicValue, testCtyValue, invalidFields})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "fields must be an object or map")
	})
}
