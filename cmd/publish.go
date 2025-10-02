package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/pkg/vinculum/vws/client"
	"go.uber.org/zap"
)

// publishCmd represents the publish command
var publishCmd = &cobra.Command{
	Use:   "publish <websocket-url> <topic> <message>",
	Short: "Publish a message to a Vinculum WebSocket server",
	Long: `Publish a message to a specified topic on a Vinculum WebSocket server.

The first argument is the WebSocket URL to connect to.
The second argument is the topic to publish to.
The third argument is the message payload (JSON string or plain text).

Examples:
  vinculum publish ws://localhost:8080/ws "sensors/temperature" "25.5"
  vinculum publish ws://localhost:8080/ws "user/login" '{"user":"alice","timestamp":"2024-01-01T00:00:00Z"}'
  vinculum publish ws://localhost:8080/ws "system/alert" "Server maintenance scheduled"`,
	Args: cobra.ExactArgs(3),
	RunE: runPublish,
}

var (
	publishDialTimeout time.Duration
	publishTimeout     time.Duration
)

func init() {
	rootCmd.AddCommand(publishCmd)

	publishCmd.Flags().DurationVar(&publishDialTimeout, "dial-timeout", 10*time.Second, "WebSocket dial timeout")
	publishCmd.Flags().DurationVar(&publishTimeout, "timeout", 30*time.Second, "Total operation timeout")
}

func runPublish(cmd *cobra.Command, args []string) error {
	// Setup logger
	logger, err := setupLogger()
	if err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}
	defer logger.Sync()

	// Parse arguments
	wsURL := args[0]
	topic := args[1]
	messageStr := args[2]

	logger.Debug("Publishing message",
		zap.String("url", wsURL),
		zap.String("topic", topic),
		zap.Any("message", messageStr),
		zap.Duration("dial-timeout", publishDialTimeout),
		zap.Duration("timeout", publishTimeout),
	)

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()

	// Create WebSocket client (no subscriber needed for publishing)
	wsClient, err := client.NewClient().
		WithURL(wsURL).
		WithLogger(logger).
		WithDialTimeout(publishDialTimeout).
		Build()
	if err != nil {
		logger.Error("failed to create WebSocket client", zap.Error(err))
		return nil
	}

	// Connect to WebSocket server
	if err := wsClient.Connect(ctx); err != nil {
		logger.Error("failed to connect to WebSocket server", zap.Error(err))
		return nil
	}

	defer func() {
		if disconnectErr := wsClient.Disconnect(); disconnectErr != nil {
			logger.Warn("Error during client disconnect", zap.Error(disconnectErr))
		}
	}()

	logger.Debug("Connected to WebSocket server", zap.String("url", wsURL))

	// Publish the message
	if err := wsClient.PublishSync(ctx, topic, messageStr); err != nil {
		logger.Error("failed to publish message", zap.Error(err))
	} else {
		logger.Debug("Message published successfully",
			zap.String("topic", topic),
			zap.Any("message", messageStr),
		)
	}

	return nil
}
