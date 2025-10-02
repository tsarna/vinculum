package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/itchyny/gojq"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/transform"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// isStruct returns true if the value is a struct or a pointer to a struct
func isStruct(v any) bool {
	if v == nil {
		return false
	}
	t := reflect.TypeOf(v)
	// Check if it's a direct struct
	if t.Kind() == reflect.Struct {
		return true
	}
	// Check if it's a pointer to a struct
	if t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Struct {
		return true
	}
	return false
}

// containsStructs returns true if the value is a slice or array that contains structs
func containsStructs(v any) bool {
	if v == nil {
		return false
	}

	t := reflect.TypeOf(v)

	// Check if it's a slice or array
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		elemType := t.Elem()

		// Check if elements are structs
		if elemType.Kind() == reflect.Struct {
			return true
		}

		// Check if elements are pointers to structs
		if elemType.Kind() == reflect.Ptr && elemType.Elem().Kind() == reflect.Struct {
			return true
		}
	}

	return false
}

// JqTransform creates a MessageTransformFunc that applies a JQ query to message payloads.
//
// The function compiles the provided JQ query string and returns a transform that:
// 1. Applies the JQ query to the message payload
// 2. Returns a new message with the transformed payload
// 3. Preserves the original message context and topic
//
// The JQ query operates on the message payload, which can be any JSON-serializable value.
// If the payload cannot be processed or the JQ query fails, the original message is returned unchanged.
//
// The JQ query has access to the following variables:
//   - $topic: The message topic as a string
//
// Parameters:
//   - jqQuery: A valid JQ query string (e.g., ".field", ".[] | select(.active)", "{data: ., topic: $topic}", etc.)
//   - logger: Optional logger for error reporting. If nil, errors are not logged (but still handled gracefully)
//
// Returns:
//   - A MessageTransformFunc that applies the JQ transformation
//   - An error if the JQ query cannot be compiled
//
// Example usage:
//
//	// Extract a specific field with logging
//	extractName, err := JqTransform(".name", logger)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Filter array elements without logging
//	filterActive, err := JqTransform(".[] | select(.active == true)", nil)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Transform and restructure data with topic information
//	restructure, err := JqTransform("{user: .name, age: .age, status: .active, source_topic: $topic}", logger)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Use topic in conditional logic
//	conditionalTransform, err := JqTransform("if $topic | startswith(\"user/\") then {type: \"user_event\", data: .} else . end", logger)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Use in a transform pipeline
//	transforms := []MessageTransformFunc{
//	    extractName,
//	    AddTopicPrefix("processed/"),
//	}
//
// The transform handles various payload types:
//   - Maps/objects: Used directly (already in primitive form)
//   - Arrays/slices of primitives: Used directly (already in primitive form)
//   - Arrays/slices containing structs: Converted via JSON marshaling/unmarshaling
//   - Primitives: Used directly (strings, numbers, booleans, etc.)
//   - Structs and *structs: Converted via JSON marshaling/unmarshaling to primitive maps
//   - JSON strings: Parsed to primitive types
//   - Byte slices: Parsed as JSON if valid, otherwise treated as strings
//   - cty.Value types: Converted using go2cty2go
//
// If the JQ query produces multiple results, they are collected into an array.
// If the JQ query produces no results, the message is dropped (returns nil).
//
// Error Handling:
// When a logger is provided, all runtime errors during transformation are logged but the transform
// continues gracefully by passing through the original message unchanged.
func JqTransform(jqQuery string, logger *zap.Logger) (transform.MessageTransformFunc, error) {
	// Parse the JQ query
	query, err := gojq.Parse(jqQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JQ query '%s': %w", jqQuery, err)
	}

	// Compile the query with topic variable support
	compiledQuery, err := gojq.Compile(query, gojq.WithVariables([]string{"$topic"}))
	if err != nil {
		return nil, fmt.Errorf("failed to compile JQ query '%s': %w", jqQuery, err)
	}

	// Return the transform function
	return func(msg *bus.EventBusMessage) (*bus.EventBusMessage, bool) {
		// Prepare input for JQ processing
		var jqInput any

		// Convert payload to a format JQ can process
		switch payload := msg.Payload.(type) {
		case string:
			// Try to parse as JSON first
			if err := json.Unmarshal([]byte(payload), &jqInput); err != nil {
				// If it's not valid JSON, treat it as a plain string
				jqInput = payload
			}
		case []byte:
			// Try to parse as JSON
			if err := json.Unmarshal(payload, &jqInput); err != nil {
				// If it's not valid JSON, convert to string
				jqInput = string(payload)
			}
		case cty.Value:
			// Convert cty.Value to Go any
			var convErr error
			jqInput, convErr = go2cty2go.CtyToAny(payload)
			if convErr != nil {
				// If conversion fails, pass through unchanged
				if logger != nil {
					logger.Error("JQ transform: failed to convert cty.Value to Go type",
						zap.String("jq_query", jqQuery),
						zap.String("topic", msg.Topic),
						zap.Error(convErr))
				}
				return msg, true
			}
		default:
			// For other types (maps, slices, primitives, structs), convert via JSON
			// to ensure structs become maps and all types are in primitive form
			if isStruct(payload) || containsStructs(payload) {
				// Convert struct or data containing structs to JSON and back to get primitive types
				jsonBytes, err := json.Marshal(payload)
				if err != nil {
					// If marshaling fails, pass through unchanged
					if logger != nil {
						logger.Error("JQ transform: failed to marshal struct/array to JSON",
							zap.String("jq_query", jqQuery),
							zap.String("topic", msg.Topic),
							zap.String("payload_type", fmt.Sprintf("%T", payload)),
							zap.Error(err))
					}
					return msg, true
				}
				if err := json.Unmarshal(jsonBytes, &jqInput); err != nil {
					// If unmarshaling fails, pass through unchanged
					if logger != nil {
						logger.Error("JQ transform: failed to unmarshal JSON back to Go types",
							zap.String("jq_query", jqQuery),
							zap.String("topic", msg.Topic),
							zap.String("payload_type", fmt.Sprintf("%T", payload)),
							zap.Error(err))
					}
					return msg, true
				}
			} else {
				// For maps, slices, and primitives without structs, use directly
				jqInput = payload
			}
		}

		// Execute the JQ query with topic as $topic variable
		// The topic value is passed as the first variable (matching the order in WithVariables)
		iter := compiledQuery.RunWithContext(context.Background(), jqInput, msg.Topic)

		// Collect all results
		var results []any
		for {
			result, hasResult := iter.Next()
			if !hasResult {
				break
			}

			// Check for execution error
			if execErr, ok := result.(error); ok {
				// On error, pass through unchanged
				if logger != nil {
					logger.Error("JQ transform: JQ execution error",
						zap.String("jq_query", jqQuery),
						zap.String("topic", msg.Topic),
						zap.Error(execErr))
				}
				return msg, true
			}

			results = append(results, result)
		}

		// Handle no results - drop the message
		if len(results) == 0 {
			return nil, false
		}

		// Determine final payload
		var newPayload any
		if len(results) == 1 {
			// Single result: use directly
			newPayload = results[0]
		} else {
			// Multiple results: return as array
			newPayload = results
		}

		// Create new message with transformed payload
		return &bus.EventBusMessage{
			Ctx:     msg.Ctx,
			MsgType: msg.MsgType,
			Topic:   msg.Topic,
			Payload: newPayload,
		}, true
	}, nil
}
