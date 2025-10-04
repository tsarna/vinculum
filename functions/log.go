package functions

import (
	"fmt"
	"strings"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// GetLogFunctions returns HCL functions for logging with zap logger
func GetLogFunctions(logger *zap.Logger) map[string]function.Function {
	if logger == nil {
		// Return no-op functions if no logger is provided
		return map[string]function.Function{
			"log_debug": makeNoOpLogFunc(),
			"log_info":  makeNoOpLogFunc(),
			"log_warn":  makeNoOpLogFunc(),
			"log_error": makeNoOpLogFunc(),
			"log_level": makeNoOpLogLevelFunc(),
		}
	}

	return map[string]function.Function{
		"log_debug": makeLogFunc(logger, zapcore.DebugLevel),
		"log_info":  makeLogFunc(logger, zapcore.InfoLevel),
		"log_warn":  makeLogFunc(logger, zapcore.WarnLevel),
		"log_error": makeLogFunc(logger, zapcore.ErrorLevel),
		"log_msg":   makeLogLevelFunc(logger),
	}
}

// makeLogFunc creates a logging function for a specific log level
func makeLogFunc(logger *zap.Logger, level zapcore.Level) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name: "message",
				Type: cty.String,
			},
		},
		VarParam: &function.Parameter{
			Name: "fields",
			Type: cty.DynamicPseudoType,
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			message := args[0].AsString()

			// Convert additional arguments to zap fields
			fields := convertArgsToZapFields(args[1:])

			// Log at the specified level using zap's Log method
			logger.Log(level, message, fields...)

			return cty.True, nil
		},
	})
}

// makeLogLevelFunc creates a logging function that takes log level as first parameter
func makeLogLevelFunc(logger *zap.Logger) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name: "level",
				Type: cty.String,
			},
			{
				Name: "message",
				Type: cty.String,
			},
		},
		VarParam: &function.Parameter{
			Name: "fields",
			Type: cty.DynamicPseudoType,
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			levelStr := args[0].AsString()
			message := args[1].AsString()

			// Parse log level from string
			var level zapcore.Level
			switch levelStr {
			case "debug":
				level = zapcore.DebugLevel
			case "info":
				level = zapcore.InfoLevel
			case "warn", "warning":
				level = zapcore.WarnLevel
			case "error":
				level = zapcore.ErrorLevel
			default:
				// Default to info level for unknown levels
				level = zapcore.InfoLevel
			}

			// Convert additional arguments to zap fields
			fields := convertArgsToZapFields(args[2:])

			// Log at the specified level using zap's Log method
			logger.Log(level, message, fields...)

			return cty.True, nil
		},
	})
}

// convertArgsToZapFields converts a slice of cty values to zap fields
func convertArgsToZapFields(args []cty.Value) []zap.Field {
	var fields []zap.Field

	// Special case: if there's exactly one argument and it's a map or object,
	// use the map/object keys as field names
	if len(args) == 1 && !args[0].IsNull() && (args[0].Type().IsMapType() || args[0].Type().IsObjectType()) {
		mapVal := args[0]
		// Check if the object/map has any elements
		hasElements := false
		for it := mapVal.ElementIterator(); it.Next(); {
			hasElements = true
			key, val := it.Element()
			keyStr := key.AsString()
			field := convertCtyValueToZapField(keyStr, val)
			if field != nil {
				fields = append(fields, *field)
			}
		}
		// Only return early if we actually processed elements
		if hasElements {
			return fields
		}
	}

	// Default case: use positional field names
	for i, arg := range args {
		field := convertCtyValueToZapField(fmt.Sprintf("$%d", i+1), arg)
		if field != nil {
			fields = append(fields, *field)
		}
	}
	return fields
}

// convertCtyValueToZapField converts a cty value to a zap field
func convertCtyValueToZapField(key string, val cty.Value) *zap.Field {
	if val.IsNull() {
		field := zap.String(key, "<null>")
		return &field
	}

	switch val.Type() {
	case cty.String:
		field := zap.String(key, val.AsString())
		return &field
	case cty.Number:
		// Try to convert to int first, then float
		if bigFloat := val.AsBigFloat(); bigFloat.IsInt() {
			if intVal, accuracy := bigFloat.Int64(); accuracy == 0 {
				field := zap.Int64(key, intVal)
				return &field
			}
		}
		if floatVal, _ := val.AsBigFloat().Float64(); true {
			field := zap.Float64(key, floatVal)
			return &field
		}
	case cty.Bool:
		field := zap.Bool(key, val.True())
		return &field
	default:
		// For complex types (lists, tuples, objects, etc.), convert to a more readable format
		if val.Type().IsListType() || val.Type().IsTupleType() || val.Type().IsSetType() {
			// Convert collections to a simple string representation
			var elements []string
			for it := val.ElementIterator(); it.Next(); {
				_, elemVal := it.Element()
				if elemVal.Type() == cty.String {
					elements = append(elements, fmt.Sprintf(`"%s"`, elemVal.AsString()))
				} else if elemVal.Type() == cty.Number {
					elements = append(elements, elemVal.AsBigFloat().String())
				} else if elemVal.Type() == cty.Bool {
					elements = append(elements, fmt.Sprintf("%t", elemVal.True()))
				} else {
					elements = append(elements, elemVal.GoString())
				}
			}
			field := zap.String(key, fmt.Sprintf("[%s]", strings.Join(elements, ", ")))
			return &field
		} else {
			// For other complex types, use the string representation
			field := zap.String(key, val.GoString())
			return &field
		}
	}

	return nil
}

// makeNoOpLogFunc creates a no-op logging function when no logger is available
func makeNoOpLogFunc() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name: "message",
				Type: cty.String,
			},
		},
		VarParam: &function.Parameter{
			Name: "fields",
			Type: cty.DynamicPseudoType,
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			return cty.False, nil
		},
	})
}

// makeNoOpLogLevelFunc creates a no-op log_level function when no logger is available
func makeNoOpLogLevelFunc() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name: "level",
				Type: cty.String,
			},
			{
				Name: "message",
				Type: cty.String,
			},
		},
		VarParam: &function.Parameter{
			Name: "fields",
			Type: cty.DynamicPseudoType,
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			// No-op: just return true without logging
			return cty.False, nil
		},
	})
}
