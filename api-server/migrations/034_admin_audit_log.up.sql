CREATE TABLE IF NOT EXISTS admin_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    admin_id    UUID        NOT NULL REFERENCES admin_accounts(id) ON DELETE CASCADE,
    action      TEXT        NOT NULL,
    target_type TEXT,
    target_id   TEXT,
    detail      TEXT,
    ip          TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_admin_audit_log_admin_id    ON admin_audit_log(admin_id);
CREATE INDEX IF NOT EXISTS idx_admin_audit_log_occurred_at ON admin_audit_log(occurred_at DESC);
