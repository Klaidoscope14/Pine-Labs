package ledger

import (
	"context"
	"testing"
	"time"
)

func TestPathsOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"app/webhooks/handler.py", "app/webhooks/handler.py", true},
		{"app/webhooks/**", "app/webhooks/handler.py", true},
		{"app/webhooks/handler.py", "app/webhooks/**", true},
		{"app/**", "app/webhooks/*.py", true},
		{"app/webhooks/handler.py", "app/payments/service.py", false},
		{"app/webhooks/**", "app/payments/**", false},
	}
	for _, c := range cases {
		if got := PathsOverlap(c.a, c.b); got != c.want {
			t.Errorf("PathsOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestScopeLeases(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "run-1")
	newRun(t, s, "t1", "run-2")
	newRun(t, s, "t2", "run-3")
	ttl := 30 * time.Minute

	if c, err := s.AcquireLeases(ctx, "t1", "repo", "run-1", []string{"app/webhooks/handler.py"}, ttl); err != nil || c != nil {
		t.Fatalf("first lease: %v %+v", err, c)
	}
	// Overlapping path in another run: conflict, nothing claimed.
	c, err := s.AcquireLeases(ctx, "t1", "repo", "run-2", []string{"app/payments/service.py", "app/webhooks/**"}, ttl)
	if err != nil || c == nil || c.HolderRun != "run-1" || c.Path != "app/webhooks/**" {
		t.Fatalf("expected conflict with run-1: %v %+v", err, c)
	}
	// Non-overlapping path: granted.
	if c, err := s.AcquireLeases(ctx, "t1", "repo", "run-2", []string{"app/payments/service.py"}, ttl); err != nil || c != nil {
		t.Fatalf("disjoint lease: %v %+v", err, c)
	}
	// Renewing your own lease is fine.
	if c, err := s.AcquireLeases(ctx, "t1", "repo", "run-1", []string{"app/webhooks/handler.py"}, ttl); err != nil || c != nil {
		t.Fatalf("renew: %v %+v", err, c)
	}
	// Another tenant never sees t1's leases (RLS).
	if c, err := s.AcquireLeases(ctx, "t2", "repo", "run-3", []string{"app/webhooks/handler.py"}, ttl); err != nil || c != nil {
		t.Fatalf("cross-tenant lease interference: %v %+v", err, c)
	}
	// Release frees the path for others.
	if err := s.ReleaseLeases(ctx, "t1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if c, err := s.AcquireLeases(ctx, "t1", "repo", "run-2", []string{"app/webhooks/handler.py"}, ttl); err != nil || c != nil {
		t.Fatalf("after release: %v %+v", err, c)
	}
	// An expired lease is ignored.
	newRun(t, s, "t1", "run-4")
	if c, err := s.AcquireLeases(ctx, "t1", "repo", "run-4", []string{"app/ledger/store.py"}, time.Second); err != nil || c != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if c, err := s.AcquireLeases(ctx, "t1", "repo", "run-2", []string{"app/ledger/store.py"}, ttl); err != nil || c != nil {
		t.Fatalf("expired lease still blocks: %v %+v", err, c)
	}
	if _, err := s.Verify(ctx, "t1"); err != nil {
		t.Fatalf("audit chain: %v", err)
	}
}

func TestReviewLoadAndReconcile(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "run-1")
	newRun(t, s, "t1", "run-2")
	for _, id := range []string{"run-1", "run-2"} {
		if err := s.RecordTransition(ctx, Transition{TenantID: "t1", RunID: id, Seq: 1, Node: "await_human", Status: "AWAITING_HUMAN"}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CountAwaitingReview(ctx, "t1", "run-2"); err != nil || n != 1 {
		t.Fatalf("awaiting = %d (%v), want 1", n, err)
	}
	if err := s.ReconcileRun(ctx, "t1", "run-1", "KILLED", "workflow terminated"); err != nil {
		t.Fatal(err)
	}
	sum, err := s.Summary(ctx, "t1", "run-1")
	if err != nil || sum.Run.Status != "KILLED" {
		t.Fatalf("reconciled status %+v (%v)", sum.Run, err)
	}
	if err := s.SetRunMeta(ctx, "t1", "run-2", "requester:bob", "high"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, "t1"); err != nil {
		t.Fatalf("audit chain: %v", err)
	}
}
