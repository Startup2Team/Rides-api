ALTER TABLE admin_accounts
    DROP COLUMN IF EXISTS totp_secret,
    DROP COLUMN IF EXISTS backup_codes;
