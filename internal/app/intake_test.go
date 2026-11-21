package app

import "testing"

func TestIntakeKeyNormalises(t *testing.T) {
	a := IntakeKey("payments-team", "svc", "Fix the  settlement\nwebhook")
	if a != IntakeKey("payments-team", "svc", "fix the settlement webhook") {
		t.Fatal("spacing and case must not create a second run")
	}
	if a == IntakeKey("settlements-team", "svc", "fix the settlement webhook") || a == IntakeKey("payments-team", "other", "fix the settlement webhook") {
		t.Fatal("different tenant or repo must be a different run")
	}
}
