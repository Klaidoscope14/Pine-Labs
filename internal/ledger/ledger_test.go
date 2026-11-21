package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"aegis/internal/config"
	"aegis/internal/gates"
	"aegis/internal/llm"
)

// Integration tests against the local Postgres. Each test run uses a fresh,
// throwaway database. Skipped unless the stack's secrets are in the environment.

func setup(t *testing.T) (*Store, string) {
	t.Helper()
	if os.Getenv("POSTGRES_PASSWORD") == "" || os.Getenv("AEGIS_APP_DB_PASSWORD") == "" {
		t.Skip("POSTGRES_PASSWORD / AEGIS_APP_DB_PASSWORD not set")
	}
	cfg, err := config.LoadFile(filepath.Join("..", "..", "config.local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	adminDSN, err := cfg.DSN(cfg.Ledger.AdminEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	appDSN, err := cfg.DSN(cfg.Ledger.AppEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	db := fmt.Sprintf("aegis_test_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Skipf("postgres not reachable: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+db+" WITH (FORCE)")
		admin.Close(context.Background())
	})
	withDB := func(dsn string) string {
		u, _ := url.Parse(dsn)
		u.Path = "/" + db
		return u.String()
	}
	pw, _ := config.ResolveSecret(cfg.Ledger.AppPasswordRef)
	if err := Migrate(ctx, withDB(adminDSN), cfg.Ledger.AppRole, pw); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, withDB(adminDSN), cfg.Ledger.AppRole, pw); err != nil {
		t.Fatalf("migrate is not idempotent: %v", err)
	}
	s, err := Open(ctx, withDB(appDSN))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, withDB(adminDSN)
}

func newRun(t *testing.T, s *Store, tenant, id string) {
	t.Helper()
	if err := s.CreateRun(context.Background(), Run{RunID: id, TenantID: tenant, Repo: "r", Requirement: "q", Status: "QUEUED", ConfigHash: "h"}); err != nil {
		t.Fatal(err)
	}
}

func TestTenantIsolation(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	newRun(t, s, "tenant-a", "run-a")
	newRun(t, s, "tenant-b", "run-b")
	if _, err := s.Summary(ctx, "tenant-b", "run-a"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("tenant-b could read tenant-a's run: %v", err)
	}
	if _, err := s.Summary(ctx, "tenant-a", "run-a"); err != nil {
		t.Fatalf("tenant-a cannot read its own run: %v", err)
	}
	// Writing a row for another tenant must be refused by the RLS WITH CHECK.
	err := s.tx(ctx, "tenant-a", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO runs (run_id, tenant_id, repo, requirement, status, config_hash) VALUES ('x','tenant-b','r','q','QUEUED','h')`)
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant insert succeeded")
	}
	// No tenant set at all: nothing is visible.
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows visible without a tenant: %d (%v)", n, err)
	}
}

func TestAuditLogIsAppendOnlyForApp(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "r1")
	for _, q := range []string{`UPDATE audit_events SET payload = '{}'`, `DELETE FROM audit_events`} {
		err := s.tx(ctx, "t1", func(tx pgx.Tx) error { _, err := tx.Exec(ctx, q); return err })
		if err == nil {
			t.Errorf("app role was allowed: %s", q)
		}
	}
}

func TestChainVerifyDetectsTampering(t *testing.T) {
	s, adminDSN := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "r1")
	for i := 1; i <= 3; i++ {
		if err := s.RecordTransition(ctx, Transition{TenantID: "t1", RunID: "r1", Seq: i, Node: "n", Status: "S"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Verify(ctx, "t1")
	if err != nil || n != 4 {
		t.Fatalf("verify = %d, %v; want 4 events, no error", n, err)
	}
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	// An attacker with owner access edits history...
	if _, err := admin.Exec(ctx, `UPDATE audit_events SET payload = '{"status":"APPROVED"}' WHERE tenant_id = 't1' AND seq = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, "t1"); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("edited row not detected: %v", err)
	}
}

func TestChainVerifyDetectsDeletedTail(t *testing.T) {
	s, adminDSN := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "r1")
	_ = s.RecordTransition(ctx, Transition{TenantID: "t1", RunID: "r1", Seq: 1, Node: "n", Status: "S"})
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `DELETE FROM audit_events WHERE tenant_id = 't1' AND seq = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, "t1"); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("deleted tail not detected: %v", err)
	}
}

func TestConcurrentAppendsKeepChainIntact(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "r1")
	var wg sync.WaitGroup
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.RecordTransition(ctx, Transition{TenantID: "t1", RunID: "r1", Seq: i, Node: "n", Status: "S"})
		}()
	}
	wg.Wait()
	if n, err := s.Verify(ctx, "t1"); err != nil || n != 21 {
		t.Fatalf("verify after concurrent appends = %d, %v", n, err)
	}
}

func TestTransitionReplayIsIgnored(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "r1")
	tr := Transition{TenantID: "t1", RunID: "r1", Seq: 1, Node: "develop", Status: "DEVELOPING"}
	for range 3 {
		if err := s.RecordTransition(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	sum, _ := s.Summary(ctx, "t1", "r1")
	if len(sum.Steps) != 1 {
		t.Fatalf("replayed transition recorded %d times", len(sum.Steps))
	}
	if n, _ := s.Verify(ctx, "t1"); n != 2 {
		t.Fatalf("audit events = %d, want 2", n)
	}
}

func TestOnceReturnsStoredResult(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "r1")
	calls := 0
	fn := func() (json.RawMessage, error) { calls++; return json.RawMessage(fmt.Sprintf(`{"n":%d}`, calls)), nil }
	a, err := s.Once(ctx, "t1", "r1", "commit:1", fn)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Once(ctx, "t1", "r1", "commit:1", fn)
	if calls != 1 || string(a) != string(b) {
		t.Fatalf("calls=%d a=%s b=%s", calls, a, b)
	}
}

func TestFindingsResolveAcrossCommits(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "r1")
	float := gates.Finding{RuleID: "FIN-001", Severity: "HIGH", Verdict: gates.RemediableFail, Message: "float", Fingerprint: "fp1"}
	pre := gates.Finding{RuleID: "FIN-004", Severity: "MEDIUM", Verdict: gates.Flag, Message: "idem", Fingerprint: "fp2", PreExisting: true}
	if err := s.RecordFindings(ctx, "t1", "r1", 1, "c1", []gates.Result{{Gate: "semgrep", Findings: []gates.Finding{float, pre}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFindings(ctx, "t1", "r1", 2, "c2", []gates.Result{{Gate: "semgrep"}}); err != nil {
		t.Fatal(err)
	}
	sum, _ := s.Summary(ctx, "t1", "r1")
	got := map[string]string{}
	for _, f := range sum.Findings {
		got[f["rule_id"].(string)] = f["status"].(string)
	}
	if got["FIN-001"] != "RESOLVED" || got["FIN-004"] != "PRE_EXISTING" {
		t.Fatalf("statuses = %v", got)
	}
}

func TestKillSwitchScopesAndAppCannotChangeIt(t *testing.T) {
	s, adminDSN := setup(t)
	ctx := context.Background()
	if on, _, _ := s.KillActive(ctx, "t1"); on {
		t.Fatal("kill switch on by default")
	}
	if err := SetKill(ctx, adminDSN, "t2", true, "incident", "ops"); err != nil {
		t.Fatal(err)
	}
	if on, _, _ := s.KillActive(ctx, "t1"); on {
		t.Error("t2's switch stopped t1")
	}
	if on, reason, _ := s.KillActive(ctx, "t2"); !on || reason != "incident" {
		t.Errorf("t2 switch = %v %q", on, reason)
	}
	if err := SetKill(ctx, adminDSN, "*", true, "global stop", "ops"); err != nil {
		t.Fatal(err)
	}
	if on, _, _ := s.KillActive(ctx, "t1"); !on {
		t.Error("global switch did not stop t1")
	}
	err := s.tx(ctx, "t1", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE kill_switch SET active = false`)
		return err
	})
	if err == nil {
		t.Error("app role turned the kill switch off")
	}
}

func TestRunSpendAndUsage(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	newRun(t, s, "t1", "r1")
	c := 0.5
	for i := 0; i < 2; i++ {
		if err := s.RecordLLMCall(ctx, llm.CallRecord{TenantID: "t1", RunID: "r1", Role: "developer", Provider: "p",
			Model: "m", InputTokens: 100, OutputTokens: 10, CostUSD: &c, InferenceGeo: "in"}); err != nil {
			t.Fatal(err)
		}
	}
	tokens, usd, err := s.RunSpend(ctx, "t1", "r1")
	if err != nil || tokens != 220 || usd != 1.0 {
		t.Fatalf("spend = %d %v %v", tokens, usd, err)
	}
	if other, _, _ := s.RunSpend(ctx, "t2", "r1"); other != 0 {
		t.Error("another tenant can see spend")
	}
	_ = s.RecordToolCall(ctx, ToolCall{TenantID: "t1", RunID: "r1", Role: "developer", Tool: "edit_file", ArgsHash: "h", Allowed: false, Reason: "protected"})
	u, err := s.RunUsage(ctx, "t1", "r1")
	if err != nil || u.Calls != 2 || u.DeniedToolCalls != 1 || u.CostUSD == nil || *u.CostUSD != 1.0 || len(u.Regions) != 1 {
		t.Fatalf("usage = %+v %v", u, err)
	}
}
