# AegisSDLC approval policy (PLAN.md §10.8). Decides each human decision on a run.
#
# input:
#   run:       {risk_tier, submitter}
#   decision:  {reviewer, roles: [..], approve: bool, passkey: bool}
#   approvals: [reviewer, ...]            accepted approvals so far
#   now:       RFC 3339 time
#   freeze_windows: [{start, end, reason}]
#   require_passkey: bool
#   required_approvers: {low: n, high: n}
#
# output: {accept: bool, reason: string, satisfied: bool, needed: n}
package aegis.approval

import rego.v1

default needed := 1

needed := n if {
	n := input.required_approvers[input.run.risk_tier]
	n > 1
}

# ---- refusals (each one is a reason the decision is not counted) ----

refusal contains "only reviewers can approve or reject" if {
	not "reviewer" in input.decision.roles
}

refusal contains "maker-checker: the submitter cannot approve their own change" if {
	input.decision.approve
	input.decision.reviewer == input.run.submitter
}

refusal contains "duplicate approval from the same reviewer" if {
	input.decision.approve
	input.decision.reviewer in input.approvals
}

refusal contains sprintf("change freeze in force: %s", [w.reason]) if {
	input.decision.approve
	some w in input.freeze_windows
	now := time.parse_rfc3339_ns(input.now)
	now >= time.parse_rfc3339_ns(w.start)
	now < time.parse_rfc3339_ns(w.end)
}

refusal contains "a passkey-signed approval is required" if {
	input.decision.approve
	input.require_passkey
	not input.decision.passkey
}

# ---- decision ----

accept if count(refusal) == 0

approvals_after := count(input.approvals) + 1 if {
	accept
	input.decision.approve
} else := count(input.approvals)

satisfied if {
	accept
	input.decision.approve
	approvals_after >= needed
}

reason := concat("; ", sort([r | some r in refusal]))

result := {
	"accept": accept,
	"reason": reason,
	"satisfied": satisfied,
	"needed": needed,
}

default accept := false

default satisfied := false
