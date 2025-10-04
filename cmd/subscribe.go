package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-vws/client"
	"go.uber.org/zap"
)

// subscribeCmd represents the subscribe command
var subscribeCmd = &cobra.Command{
	Use:   "subscribe <websocket-url> [topic-patterns...]",
	Short: "Subscribe to events from a Vinculum WebSocket server",
	Long: `Subscribe to events from a Vinculum WebSocket server and print them to stdout.

The first argument is the WebSocket URL to connect to.
Additional arguments are topic patterns to subscribe to (MQTT-style patterns).
If no topic patterns are provided, subscribes to all events ("#").

Examples:
  vinculum subscribe ws://localhost:8080/ws
  vinculum subscribe ws://localhost:8080/ws "sensors/+/temperature"
  vinculum subscribe ws://localhost:8080/ws "user/#" "system/alerts"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSubscribe,
}

var (
	dialTimeout time.Duration
)

func init() {
	rootCmd.AddCommand(subscribeCmd)

	subscribeCmd.Flags().DurationVar(&dialTimeout, "dial-timeout", 10*time.Second, "WebSocket dial timeout")
}

func runSubscribe(cmd *cobra.Command, args []string) error {
	// Setup logger
	logger, err := setupLogger()
	if err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}
	defer logger.Sync()

	// Create a context that can be cancelled on signal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First arg is the WebSocket URL
	wsURL := args[0]

	// Remaining args are topic patterns, default to "#" if none provided
	topics := args[1:]
	if len(topics) == 0 {
		topics = []string{"#"}
	}

	logger.Info("Starting subscription",
		zap.String("url", wsURL),
		zap.Strings("topics", topics),
		zap.Duration("dial-timeout", dialTimeout),
	)

	// Create printing subscriber
	subscriber := &printingSubscriber{logger: logger}

	// Create AutoReconnector so we can control it
	autoReconnector := bus.NewAutoReconnector().
		WithLogger(logger).
		Build()

	// Create WebSocket client
	wsClient, err := client.NewClient().
		WithURL(wsURL).
		WithLogger(logger).
		WithDialTimeout(dialTimeout).
		WithSubscriber(subscriber).
		WithMonitor(autoReconnector).
		Build()
	if err != nil {
		logger.Fatal("Failed to create WebSocket client", zap.Error(err))
	}

	// Connect to WebSocket server
	if err := wsClient.Connect(ctx); err != nil {
		logger.Fatal("Failed to connect to WebSocket server", zap.Error(err))
	}

	logger.Info("Connected to WebSocket server", zap.String("url", wsURL))

	// Subscribe to all topic patterns
	for _, topic := range topics {
		if err := wsClient.Subscribe(ctx, topic); err != nil {
			logger.Error("Failed to subscribe to topic", zap.String("topic", topic), zap.Error(err))
		} else {
			logger.Info("Subscribed to topic", zap.String("topic", topic))
		}
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	logger.Info("Listening for events... (Press Ctrl+C to exit)")

	// Wait for signal or context cancellation
	select {
	case sig := <-sigChan:
		logger.Debug("Signal received, exiting", zap.String("signal", sig.String()))

		autoReconnector.SetEnabled(false)
		cancel()

		// Graceful shutdown
		if err := wsClient.Disconnect(); err != nil {
			logger.Warn("Error during client disconnect", zap.Error(err))
		}

		logger.Info("Shutdown complete")
		return nil

	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down...")
		autoReconnector.SetEnabled(false)

		// Normal shutdown for context cancellation
		if err := wsClient.Disconnect(); err != nil {
			logger.Warn("Error during client disconnect", zap.Error(err))
		}

		logger.Info("Shutdown complete")
		return nil
	}
}

type printingSubscriber struct {
	bus.BaseSubscriber
	logger *zap.Logger
}

func (s *printingSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	jsonBytes, err := json.Marshal(message)
	if err != nil {
		fmt.Printf("%s\t<error marshaling JSON: %v>\n", topic, err)
		s.logger.Warn("Failed to marshal message to JSON",
			zap.String("topic", topic),
			zap.Error(err),
			zap.Any("message", message))
		return nil
	}
	fmt.Printf("%s\t%s\n", topic, string(jsonBytes))
	return nil
}
