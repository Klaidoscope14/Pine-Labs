-- Phase 2: model calls, tool calls and the kill switch.
-- No prompt or completion text is ever stored, only counts, hashes and metadata.

CREATE TABLE llm_calls (
    id                  bigserial PRIMARY KEY,
    tenant_id           text NOT NULL,
    run_id              text NOT NULL,
    role                text NOT NULL,
    attempt             integer NOT NULL DEFAULT 0,
    seq                 integer NOT NULL DEFAULT 0,
    provider            text NOT NULL,
    model               text NOT NULL,
    prompt_hash         text NOT NULL DEFAULT '',
    input_tokens        bigint NOT NULL DEFAULT 0,
    output_tokens       bigint NOT NULL DEFAULT 0,
    cache_read_tokens   bigint NOT NULL DEFAULT 0,
    cache_write_tokens  bigint NOT NULL DEFAULT 0,
    latency_ms          bigint NOT NULL DEFAULT 0,
    cost_usd            numeric(12, 6),          -- NULL when the price is unknown
    stop_reason         text NOT NULL DEFAULT '',
    inference_geo       text NOT NULL DEFAULT '',
    redactions          integer NOT NULL DEFAULT 0,
    error               text NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX llm_calls_run ON llm_calls (run_id);
CREATE INDEX llm_calls_tenant_day ON llm_calls (tenant_id, created_at);

CREATE TABLE tool_calls (
    id           bigserial PRIMARY KEY,
    tenant_id    text NOT NULL,
    run_id       text NOT NULL,
    role         text NOT NULL,
    attempt      integer NOT NULL DEFAULT 0,
    tool         text NOT NULL,
    args_hash    text NOT NULL,
    allowed      boolean NOT NULL,
    reason       text NOT NULL DEFAULT '',
    duration_ms  bigint NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tool_calls_run ON tool_calls (run_id);

-- One row per scope: '*' is global, otherwise a tenant id. Only operators
-- (the owner role) can change it; the app role can only read it.
CREATE TABLE kill_switch (
    tenant_id  text PRIMARY KEY,
    active     boolean NOT NULL,
    reason     text NOT NULL DEFAULT '',
    set_by     text NOT NULL DEFAULT '',
    set_at     timestamptz NOT NULL DEFAULT now()
);

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['llm_calls', 'tool_calls'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''aegis.tenant_id'', true)) WITH CHECK (tenant_id = current_setting(''aegis.tenant_id'', true))', t);
    END LOOP;
END $$;
ALTER TABLE kill_switch ENABLE ROW LEVEL SECURITY;
ALTER TABLE kill_switch FORCE ROW LEVEL SECURITY;
-- A tenant sees its own switch and the global one.
CREATE POLICY tenant_or_global ON kill_switch FOR SELECT
    USING (tenant_id = '*' OR tenant_id = current_setting('aegis.tenant_id', true));

REVOKE ALL ON llm_calls, tool_calls, kill_switch FROM PUBLIC;
GRANT SELECT, INSERT ON llm_calls, tool_calls TO {{app_role}};
GRANT SELECT ON kill_switch TO {{app_role}};
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO {{app_role}};
