package posthogmcpsdk

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithArgumentsExtendsInputSchemas(t *testing.T) {
	arguments := []analyticsArgument{{name: "context", description: "why", required: true}}
	for _, test := range []struct {
		name         string
		schema       any
		wantInjected []string
	}{
		{"typed object", map[string]any{"type": "object"}, []string{"context"}},
		{"properties without a type", map[string]any{"properties": map[string]any{"city": map[string]any{"type": "string"}}}, []string{"context"}},
		{"empty schema", map[string]any{}, []string{"context"}},
		{"not an object", map[string]any{"type": "array"}, nil},
		{"combinator at the root", map[string]any{"type": "object", "anyOf": []any{}}, nil},
		{"properties that are not an object", map[string]any{"type": "object", "properties": "nope"}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, injected := withArguments(test.schema, arguments)
			assert.Equal(t, test.wantInjected, injected)
		})
	}
}

func TestWithInstructionsExtendsOnlySchemasThatAllowIt(t *testing.T) {
	for _, test := range []struct {
		name      string
		schema    any
		wantAdded bool
	}{
		{"typed object", map[string]any{"type": "object"}, true},
		{"properties without a type", map[string]any{"properties": map[string]any{"city": map[string]any{"type": "string"}}}, false},
		{"already declared", map[string]any{"type": "object", "properties": map[string]any{"_mcp_instructions": map[string]any{"type": "string"}}}, false},
		{"maxProperties one", map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "maxProperties": 1}, false},
		{"property names constrained", map[string]any{"type": "object", "propertyNames": map[string]any{"pattern": "^[a-z]+$"}}, false},
		{"additionalProperties false still gets it declared", map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "additionalProperties": false}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, added := withInstructions(test.schema)
			assert.Equal(t, test.wantAdded, added)
		})
	}
}
