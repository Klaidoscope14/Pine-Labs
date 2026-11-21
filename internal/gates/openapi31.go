package gates

import "encoding/json"

// downgradeOpenAPI31 rewrites an OpenAPI 3.1 document into the 3.0 shape that
// stable oasdiff understands. Only constructs that change meaning are converted:
//   - exclusiveMinimum/Maximum as numbers  -> minimum/maximum + boolean flag
//   - type: [T, "null"] and anyOf/oneOf with {type: null} -> T + nullable: true
//   - const -> single-value enum
//   - examples (array, 3.1) -> example (first item)
func downgradeOpenAPI31(doc []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, err
	}
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["openapi"].(string); ok && len(s) >= 3 && s[:3] == "3.1" {
			m["openapi"] = "3.0.3"
		}
		delete(m, "jsonSchemaDialect")
		delete(m, "webhooks")
	}
	walk(v)
	return json.Marshal(v)
}

func walk(v any) {
	switch x := v.(type) {
	case map[string]any:
		fixSchema(x)
		for _, child := range x {
			walk(child)
		}
	case []any:
		for _, child := range x {
			walk(child)
		}
	}
}

func isNullSchema(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) == 1 && m["type"] == "null"
}

func fixSchema(m map[string]any) {
	for _, pair := range [][2]string{{"exclusiveMinimum", "minimum"}, {"exclusiveMaximum", "maximum"}} {
		if n, ok := m[pair[0]].(float64); ok {
			m[pair[1]] = n
			m[pair[0]] = true
		}
	}
	if types, ok := m["type"].([]any); ok {
		var rest []any
		for _, t := range types {
			if t == "null" {
				m["nullable"] = true
			} else {
				rest = append(rest, t)
			}
		}
		if len(rest) == 1 {
			m["type"] = rest[0]
		} else {
			delete(m, "type")
		}
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		list, ok := m[key].([]any)
		if !ok {
			continue
		}
		var rest []any
		for _, s := range list {
			if isNullSchema(s) {
				m["nullable"] = true
			} else {
				rest = append(rest, s)
			}
		}
		if len(rest) == 1 && m["nullable"] == true {
			delete(m, key)
			if inner, ok := rest[0].(map[string]any); ok {
				for k, val := range inner {
					if _, exists := m[k]; !exists {
						m[k] = val
					}
				}
			}
		} else {
			m[key] = rest
		}
	}
	if c, ok := m["const"]; ok {
		m["enum"] = []any{c}
		delete(m, "const")
	}
	if ex, ok := m["examples"].([]any); ok && len(ex) > 0 {
		if _, isSchema := m["type"]; isSchema {
			m["example"] = ex[0]
			delete(m, "examples")
		}
	}
}
