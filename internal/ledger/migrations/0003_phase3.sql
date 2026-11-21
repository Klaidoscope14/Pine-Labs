-- Phase 3: who submitted each run (maker-checker) and scope leases between runs.

ALTER TABLE runs ADD COLUMN submitter text NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN risk_tier text NOT NULL DEFAULT '';

-- A run's advisory claim on the paths its plan will change. Overlapping live
-- leases make the later run wait; expired leases are ignored and swept.
CREATE TABLE scope_leases (
    tenant_id   text NOT NULL,
    repo        text NOT NULL,
    run_id      text NOT NULL,
    path        text NOT NULL,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, repo, run_id, path)
);
CREATE INDEX scope_leases_repo ON scope_leases (tenant_id, repo, expires_at);

ALTER TABLE scope_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE scope_leases FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON scope_leases
    USING (tenant_id = current_setting('aegis.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('aegis.tenant_id', true));
GRANT SELECT, INSERT, UPDATE, DELETE ON scope_leases TO {{app_role}};
