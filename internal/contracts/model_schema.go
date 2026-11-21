package contracts

import (
	"encoding/json"
	"fmt"

	"aegis/schemas"
)

// unsupported are JSON Schema keywords the model-facing structured-output schema
// omits. The full contract still validates the result afterwards, so nothing is
// lost: these constraints are enforced by Validate, not by the model API.
var unsupported = map[string]bool{
	"$schema": true, "$id": true, "title": true, "pattern": true, "format": true,
	"minLength": true, "maxLength": true, "minimum": true, "maximum": true,
	"exclusiveMinimum": true, "exclusiveMaximum": true, "minItems": true, "maxItems": true,
	"uniqueItems": true, "allOf": true, "if": true, "then": true, "else": true,
}

func simplify(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if unsupported[k] {
				continue
			}
			if k == "properties" {
				props := map[string]any{}
				for name, p := range val.(map[string]any) {
					props[name] = simplify(p)
				}
				out[k] = props
				continue
			}
			out[k] = simplify(val)
		}
		// Structured outputs need every object closed and every property listed.
		if out["type"] == "object" {
			out["additionalProperties"] = false
			if props, ok := out["properties"].(map[string]any); ok {
				req := make([]any, 0, len(props))
				for name := range props {
					req = append(req, name)
				}
				out["required"] = sortedAny(req)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = simplify(x[i])
		}
		return out
	default:
		return v
	}
}

func sortedAny(in []any) []any {
	s := make([]string, len(in))
	for i, v := range in {
		s[i] = v.(string)
	}
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// ModelSchema returns the simplified schema to send as the structured-output
// format for a contract. Optional contract fields become required in the
// model-facing schema (the model returns empty values); the full contract decides.
func ModelSchema(contract string) (map[string]any, error) {
	path, ok := schemas.AgentSchemas[contract]
	if !ok {
		return nil, fmt.Errorf("unknown contract %q", contract)
	}
	raw, err := schemas.FS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return simplify(doc).(map[string]any), nil
}
