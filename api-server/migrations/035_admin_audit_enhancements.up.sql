-- Extend admin_audit_log with role context and structured metadata.
ALTER TABLE admin_audit_log
    ADD COLUMN IF NOT EXISTS admin_role TEXT,
    ADD COLUMN IF NOT EXISTS metadata   JSONB;

CREATE INDEX IF NOT EXISTS idx_admin_audit_log_role   ON admin_audit_log (admin_role);
CREATE INDEX IF NOT EXISTS idx_admin_audit_log_action ON admin_audit_log (action);
CREATE INDEX IF NOT EXISTS idx_admin_audit_log_target ON admin_audit_log (target_type, target_id);
