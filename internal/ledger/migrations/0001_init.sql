-- AegisSDLC ledger. Every table carries tenant_id and is protected by row-level
-- security (FORCE, so even the table owner's sessions are filtered). The app role
-- is not the owner, has no BYPASSRLS, and cannot UPDATE or DELETE audit events.
-- {{app_role}} is replaced with the quoted role name from config.

CREATE TABLE runs (
    run_id       text PRIMARY KEY,
    tenant_id    text NOT NULL,
    scenario     text NOT NULL DEFAULT '',
    repo         text NOT NULL,
    requirement  text NOT NULL,
    base_sha     text NOT NULL DEFAULT '',
    head_sha     text NOT NULL DEFAULT '',
    branch       text NOT NULL DEFAULT '',
    status       text NOT NULL,
    attempts     integer NOT NULL DEFAULT 0,
    config_hash  text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runs_tenant_status ON runs (tenant_id, status);

CREATE TABLE steps (
    id          bigserial PRIMARY KEY,
    tenant_id   text NOT NULL,
    run_id      text NOT NULL REFERENCES runs (run_id),
    seq         integer NOT NULL,
    node        text NOT NULL,
    attempt     integer NOT NULL DEFAULT 0,
    status      text NOT NULL,
    detail      jsonb NOT NULL DEFAULT '{}',
    commit_sha  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, seq)
);

CREATE TABLE findings (
    id           bigserial PRIMARY KEY,
    tenant_id    text NOT NULL,
    run_id       text NOT NULL REFERENCES runs (run_id),
    attempt      integer NOT NULL,
    gate         text NOT NULL,
    rule_id      text NOT NULL,
    severity     text NOT NULL,
    verdict      text NOT NULL,
    file         text NOT NULL DEFAULT '',
    line         integer NOT NULL DEFAULT 0,
    message      text NOT NULL,
    fingerprint  text NOT NULL,
    commit_sha   text NOT NULL,
    status       text NOT NULL CHECK (status IN ('OPEN', 'RESOLVED', 'STALE', 'PRE_EXISTING')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, gate, fingerprint, commit_sha)
);
CREATE INDEX findings_run ON findings (run_id, status);

CREATE TABLE approvals (
    id             bigserial PRIMARY KEY,
    tenant_id      text NOT NULL,
    run_id         text NOT NULL REFERENCES runs (run_id),
    reviewer       text NOT NULL,
    decision       text NOT NULL CHECK (decision IN ('APPROVE', 'REJECT')),
    head_sha       text NOT NULL,
    evidence_hash  text NOT NULL,
    note           text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, reviewer, head_sha)
);

CREATE TABLE side_effects (
    key         text PRIMARY KEY,
    tenant_id   text NOT NULL,
    run_id      text NOT NULL,
    result      jsonb NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_events (
    tenant_id   text NOT NULL,
    seq         bigint NOT NULL,
    run_id      text NOT NULL DEFAULT '',
    kind        text NOT NULL,
    payload     text NOT NULL,
    prev_hash   text NOT NULL,
    hash        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, seq)
);

CREATE TABLE chain_heads (
    tenant_id  text PRIMARY KEY,
    seq        bigint NOT NULL,
    hash       text NOT NULL
);

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['runs', 'steps', 'findings', 'approvals', 'side_effects', 'audit_events', 'chain_heads'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''aegis.tenant_id'', true)) WITH CHECK (tenant_id = current_setting(''aegis.tenant_id'', true))', t);
    END LOOP;
END $$;

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO {{app_role}};
GRANT SELECT, INSERT, UPDATE ON runs, steps, findings, approvals, chain_heads TO {{app_role}};
-- Append-only: no UPDATE or DELETE on the audit log or recorded side effects.
GRANT SELECT, INSERT ON audit_events, side_effects TO {{app_role}};
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO {{app_role}};
