package mcp

import (
	"encoding/json"
	"fmt"
)

// ParamDef describes a single parameter for a tool or prompt.
type ParamDef struct {
	Name        string
	Type        string // "string", "number", "boolean"
	Description string
	Required    bool
	DefaultVal  any
	Enum        []any
}

// jsonSchemaType maps VCL param types to JSON Schema types.
var jsonSchemaType = map[string]string{
	"string":  "string",
	"number":  "number",
	"boolean": "boolean",
}

// BuildToolInputSchema generates a JSON Schema object for the given params,
// suitable for use as Tool.InputSchema in the MCP SDK.
func BuildToolInputSchema(params []ParamDef) (json.RawMessage, error) {
	properties := map[string]any{}
	var required []string

	for _, p := range params {
		jsonType, ok := jsonSchemaType[p.Type]
		if !ok {
			return nil, fmt.Errorf("param %q has unsupported type %q; expected string, number, or boolean", p.Name, p.Type)
		}

		prop := map[string]any{"type": jsonType}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		properties[p.Name] = prop

		if p.Required {
			required = append(required, p.Name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	return json.Marshal(schema)
}
