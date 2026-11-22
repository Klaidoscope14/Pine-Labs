-- Phase 5: WebAuthn passkeys for signed approvals. One row per credential.
-- The credential JSON holds the public key and sign counter (never a secret).
CREATE TABLE passkeys (
    tenant_id      text NOT NULL,
    user_id        text NOT NULL,
    credential_id  text NOT NULL,
    credential     text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_used_at   timestamptz,
    PRIMARY KEY (tenant_id, credential_id)
);
CREATE INDEX passkeys_user ON passkeys (tenant_id, user_id);

ALTER TABLE passkeys ENABLE ROW LEVEL SECURITY;
ALTER TABLE passkeys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON passkeys
    USING (tenant_id = current_setting('aegis.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('aegis.tenant_id', true));
GRANT SELECT, INSERT, UPDATE ON passkeys TO {{app_role}};
