package client

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/tsarna/vinculum-bus"
	"go.uber.org/zap"
)

// ExampleClient demonstrates basic usage of the WebSocket client.
func ExampleClient() {
	logger, _ := zap.NewDevelopment()

	// Create a subscriber
	subscriber := &exampleSubscriber{}

	// Create client using fluent builder pattern
	client, err := NewClient().
		WithURL("ws://localhost:8080/ws").
		WithLogger(logger).
		WithDialTimeout(10 * time.Second).
		WithSubscriber(subscriber).
		WithWriteChannelSize(200).                     // Configure write buffer size
		WithAuthorization("Bearer example-token-123"). // Add authorization
		Build()
	if err != nil {
		log.Fatal(err)
	}

	// Connect to the server
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect()

	// Subscribe to topics
	if err := client.Subscribe(ctx, "sensor/+/temperature"); err != nil {
		log.Fatal(err)
	}

	if err := client.Subscribe(ctx, "alerts/#"); err != nil {
		log.Fatal(err)
	}

	// Publish some events
	client.Publish(ctx, "sensor/room1/temperature", 23.5)
	client.Publish(ctx, "sensor/room2/temperature", 24.1)
	client.Publish(ctx, "alerts/high-temperature", "Room 2 temperature is high")

	// The subscriber will receive these events asynchronously
	time.Sleep(100 * time.Millisecond)
}

// ExampleClient_asClient demonstrates using the client as a bus.Client.
func ExampleClient_asClient() {
	logger, _ := zap.NewDevelopment()

	// Create a subscriber
	subscriber := &exampleSubscriber{}

	// Create client
	client, err := NewClient().
		WithURL("ws://localhost:8080/ws").
		WithLogger(logger).
		WithSubscriber(subscriber).
		Build()
	if err != nil {
		log.Fatal(err)
	}

	// Use Client interface methods
	var vinculumClient bus.Client = client

	// Connect to WebSocket
	ctx := context.Background()
	if err := vinculumClient.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer vinculumClient.Disconnect()

	// Now use it like any Client
	vinculumClient.Subscribe(ctx, "notifications/#")
	vinculumClient.Publish(ctx, "notifications/user/login", map[string]string{
		"user_id": "12345",
		"action":  "login",
	})

	time.Sleep(100 * time.Millisecond)
}

// ExampleClient_withDynamicAuth demonstrates using dynamic authorization.
func ExampleClient_withDynamicAuth() {
	logger, _ := zap.NewDevelopment()
	subscriber := &exampleSubscriber{}

	// Create a dynamic authorization provider
	authProvider := func(ctx context.Context) (string, error) {
		// In a real application, this might:
		// - Refresh an expired JWT token
		// - Fetch a new OAuth2 access token
		// - Read credentials from a secure store

		// For this example, we'll simulate getting a fresh token
		token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.example"
		return "Bearer " + token, nil
	}

	// Create client with dynamic authorization
	client, err := NewClient().
		WithURL("ws://localhost:8080/ws").
		WithLogger(logger).
		WithSubscriber(subscriber).
		WithAuthorizationProvider(authProvider). // Dynamic auth
		Build()
	if err != nil {
		log.Fatal(err)
	}

	// Connect - authorization provider will be called during handshake
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect()

	// Use the client normally
	client.Subscribe(ctx, "secure/data/#")
	client.Publish(ctx, "secure/data/update", map[string]string{
		"message": "Authenticated message",
	})

	time.Sleep(100 * time.Millisecond)
}

// ExampleClient_withMonitor demonstrates using a client monitor for lifecycle events.
func ExampleClient_withMonitor() {
	logger, _ := zap.NewDevelopment()
	subscriber := &exampleSubscriber{}

	// Create a monitor to track client lifecycle events
	monitor := &exampleMonitor{logger: logger}

	// Create client with monitor
	client, err := NewClient().
		WithURL("ws://localhost:8080/ws").
		WithLogger(logger).
		WithSubscriber(subscriber).
		WithMonitor(monitor). // Add lifecycle monitoring
		Build()
	if err != nil {
		log.Fatal(err)
	}

	// Connect - monitor will receive OnConnect event
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}

	// Subscribe - monitor will receive OnSubscribe events
	client.Subscribe(ctx, "events/#")
	client.Subscribe(ctx, "alerts/+")

	// Publish some events
	client.Publish(ctx, "events/user/login", "user123")

	// Unsubscribe - monitor will receive OnUnsubscribe event
	client.Unsubscribe(ctx, "alerts/+")

	// Disconnect - monitor will receive OnDisconnect event with nil error (graceful)
	client.Disconnect()

	time.Sleep(100 * time.Millisecond)
}

// exampleMonitor implements the ClientMonitor interface for demonstrations.
type exampleMonitor struct {
	logger *zap.Logger
}

func (m *exampleMonitor) OnConnect(ctx context.Context, client bus.Client) {
	m.logger.Info("🔗 Client connected to WebSocket server")
}

func (m *exampleMonitor) OnDisconnect(ctx context.Context, client bus.Client, err error) {
	if err != nil {
		m.logger.Error("❌ Client disconnected with error", zap.Error(err))
	} else {
		m.logger.Info("✅ Client disconnected gracefully")
	}
}

func (m *exampleMonitor) OnSubscribe(ctx context.Context, client bus.Client, topic string) {
	m.logger.Info("📥 Subscribed to topic", zap.String("topic", topic))
}

func (m *exampleMonitor) OnUnsubscribe(ctx context.Context, client bus.Client, topic string) {
	m.logger.Info("📤 Unsubscribed from topic", zap.String("topic", topic))
}

func (m *exampleMonitor) OnUnsubscribeAll(ctx context.Context, client bus.Client) {
	m.logger.Info("🗑️ Unsubscribed from all topics")
}

// exampleSubscriber implements the Subscriber interface for demonstrations.
type exampleSubscriber struct{}

func (s *exampleSubscriber) OnSubscribe(ctx context.Context, topic string) error {
	fmt.Printf("Subscribed to: %s\n", topic)
	return nil
}

func (s *exampleSubscriber) OnUnsubscribe(ctx context.Context, topic string) error {
	fmt.Printf("Unsubscribed from: %s\n", topic)
	return nil
}

func (s *exampleSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	fmt.Printf("Received event on %s: %v\n", topic, message)
	return nil
}

func (s *exampleSubscriber) PassThrough(msg bus.EventBusMessage) error {
	fmt.Printf("PassThrough: %s -> %v\n", msg.Topic, msg.Payload)
	return nil
}
