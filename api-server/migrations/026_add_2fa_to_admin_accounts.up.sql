ALTER TABLE admin_accounts
    ADD COLUMN IF NOT EXISTS totp_secret  TEXT,
    ADD COLUMN IF NOT EXISTS backup_codes JSONB NOT NULL DEFAULT '[]';
