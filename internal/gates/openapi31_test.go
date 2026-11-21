package gates

import (
	"encoding/json"
	"testing"
)

func TestDowngradeOpenAPI31(t *testing.T) {
	in := `{"openapi":"3.1.0","components":{"schemas":{"P":{"type":"object","properties":{
	  "amount":{"type":"integer","exclusiveMinimum":0},
	  "note":{"anyOf":[{"type":"string","maxLength":5},{"type":"null"}]},
	  "tag":{"type":["string","null"]},
	  "kind":{"const":"settlement"}}}}}}`
	out, err := downgradeOpenAPI31([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(out, &doc)
	if doc["openapi"] != "3.0.3" {
		t.Errorf("version not downgraded: %v", doc["openapi"])
	}
	props := doc["components"].(map[string]any)["schemas"].(map[string]any)["P"].(map[string]any)["properties"].(map[string]any)
	amount := props["amount"].(map[string]any)
	if amount["minimum"] != float64(0) || amount["exclusiveMinimum"] != true {
		t.Errorf("amount = %v", amount)
	}
	note := props["note"].(map[string]any)
	if note["type"] != "string" || note["nullable"] != true || note["maxLength"] != float64(5) || note["anyOf"] != nil {
		t.Errorf("note = %v", note)
	}
	tag := props["tag"].(map[string]any)
	if tag["type"] != "string" || tag["nullable"] != true {
		t.Errorf("tag = %v", tag)
	}
	if kind := props["kind"].(map[string]any); kind["const"] != nil || len(kind["enum"].([]any)) != 1 {
		t.Errorf("kind = %v", kind)
	}
}
