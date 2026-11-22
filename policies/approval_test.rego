package aegis.approval_test

import data.aegis.approval
import rego.v1

base := {
	"run": {"risk_tier": "high", "submitter": "priya"},
	"decision": {"reviewer": "arjun", "roles": ["reviewer"], "approve": true, "passkey": true},
	"approvals": [],
	"now": "2026-11-30T10:00:00Z",
	"freeze_windows": [],
	"require_passkey": false,
	"required_approvers": {"low": 1, "high": 2},
}

test_first_of_two_high_risk_approvals_is_accepted_not_satisfied if {
	r := approval.result with input as base
	r.accept
	not r.satisfied
	r.needed == 2
}

test_second_distinct_approval_satisfies if {
	r := approval.result with input as object.union(base, {"approvals": ["kavya"]})
	r.accept
	r.satisfied
}

test_low_risk_needs_one if {
	r := approval.result with input as object.union(base, {"run": {"risk_tier": "low", "submitter": "priya"}})
	r.satisfied
}

test_submitter_cannot_approve if {
	r := approval.result with input as object.union(base, {"decision": {"reviewer": "priya", "roles": ["reviewer"], "approve": true, "passkey": true}})
	not r.accept
	contains(r.reason, "maker-checker")
}

test_submitter_may_withdraw if {
	r := approval.result with input as object.union(base, {"decision": {"reviewer": "priya", "roles": ["reviewer"], "approve": false, "passkey": false}})
	r.accept
}

test_duplicate_vote_refused if {
	r := approval.result with input as object.union(base, {"approvals": ["arjun"]})
	not r.accept
	contains(r.reason, "duplicate")
}

test_non_reviewer_refused if {
	r := approval.result with input as object.union(base, {"decision": {"reviewer": "rohan", "roles": ["operator"], "approve": true, "passkey": true}})
	not r.accept
}

test_freeze_refuses_approval if {
	r := approval.result with input as object.union(base, {"freeze_windows": [{"start": "2026-11-30T00:00:00Z", "end": "2026-12-01T00:00:00Z", "reason": "month-end settlement"}]})
	not r.accept
	contains(r.reason, "month-end")
}

test_freeze_allows_rejection if {
	r := approval.result with input as object.union(base, {
		"decision": {"reviewer": "arjun", "roles": ["reviewer"], "approve": false, "passkey": false},
		"freeze_windows": [{"start": "2026-11-30T00:00:00Z", "end": "2026-12-01T00:00:00Z", "reason": "x"}],
	})
	r.accept
}

test_passkey_required if {
	r := approval.result with input as object.union(base, {
		"require_passkey": true,
		"decision": {"reviewer": "arjun", "roles": ["reviewer"], "approve": true, "passkey": false},
	})
	not r.accept
	contains(r.reason, "passkey")
}
