ALTER TABLE admin_accounts
    DROP COLUMN IF EXISTS phone,
    DROP COLUMN IF EXISTS photo_url;
