DROP INDEX IF EXISTS idx_admin_audit_log_target;
DROP INDEX IF EXISTS idx_admin_audit_log_action;
DROP INDEX IF EXISTS idx_admin_audit_log_role;

ALTER TABLE admin_audit_log
    DROP COLUMN IF EXISTS metadata,
    DROP COLUMN IF EXISTS admin_role;
