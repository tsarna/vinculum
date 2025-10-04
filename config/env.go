package config

import (
	"os"
	"strings"

	"github.com/zclconf/go-cty/cty"
)

// GetEnvObject returns a cty object containing all environment variables
// as attributes, suitable for providing to an HCL evaluation context.
func GetEnvObject() cty.Value {
	envVars := os.Environ()
	envMap := make(map[string]cty.Value)

	for _, envVar := range envVars {
		// Split on the first '=' to separate key and value
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) == 2 {
			key := parts[0]
			value := parts[1]

			// Convert environment variable name to a valid HCL attribute name
			// Replace invalid characters with underscores
			attrName := sanitizeEnvVarName(key)

			envMap[attrName] = cty.StringVal(value)
		}
	}

	// If no environment variables found, return an empty object
	if len(envMap) == 0 {
		return cty.ObjectVal(map[string]cty.Value{})
	}

	return cty.ObjectVal(envMap)
}

// sanitizeEnvVarName converts environment variable names to valid HCL attribute names
// HCL attribute names must start with a letter or underscore and contain only
// letters, digits, underscores, and hyphens
func sanitizeEnvVarName(name string) string {
	if name == "" {
		return "_"
	}

	var result strings.Builder

	// Ensure first character is valid (letter or underscore)
	firstChar := rune(name[0])
	if isValidFirstChar(firstChar) {
		result.WriteRune(firstChar)
	} else {
		result.WriteRune('_')
	}

	// Process remaining characters
	for _, char := range name[1:] {
		if isValidChar(char) {
			result.WriteRune(char)
		} else {
			result.WriteRune('_')
		}
	}

	return result.String()
}

// isValidFirstChar checks if a character is valid as the first character of an HCL attribute name
func isValidFirstChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
}

// isValidChar checks if a character is valid in an HCL attribute name
func isValidChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
}
