// Package schemas embeds the JSON Schemas that define AegisSDLC's
// configuration and every agent's input/output contract.
package schemas

import "embed"

// FS holds config.schema.json and agents/*.schema.json.
//
//go:embed config.schema.json agents/*.schema.json
var FS embed.FS

// AgentSchemas maps an agent contract name to its schema file in FS.
var AgentSchemas = map[string]string{
	"requirement_spec":  "agents/requirement_spec.schema.json",
	"architecture_plan": "agents/architecture_plan.schema.json",
	"developer_result":  "agents/developer_result.schema.json",
	"qa_report":         "agents/qa_report.schema.json",
	"security_report":   "agents/security_report.schema.json",
	"release_packet":    "agents/release_packet.schema.json",
}
