-- Reverse DB-1. Non-lossy for pre-existing data (columns were NULL on every row
-- that predates this migration); any numbers captured while it was live ARE
-- dropped, which is the intended meaning of rolling this back.
DROP INDEX IF EXISTS uq_users_national_id;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_national_id_country_chk;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_national_id_number_chk;

ALTER TABLE users
    DROP COLUMN IF EXISTS national_id_country,
    DROP COLUMN IF EXISTS national_id_number;
