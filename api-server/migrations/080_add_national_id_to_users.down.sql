-- Reverse DB-1. Non-lossy for pre-existing data (columns were NULL on every row
-- that predates this migration); any numbers captured while it was live ARE
-- dropped, which is the intended meaning of rolling this back.
--
-- The uq_users_national_id UNIQUE INDEX (082) and the VALIDATE CONSTRAINT step
-- (081) are reversed by THEIR OWN down migrations, run first by golang-migrate
-- (reverse order) — by the time this file runs, the index is already gone and
-- the constraints are just plain CHECK constraints, which DROP CONSTRAINT
-- handles identically either way.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_national_id_country_chk;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_national_id_number_chk;

ALTER TABLE users
    DROP COLUMN IF EXISTS national_id_country,
    DROP COLUMN IF EXISTS national_id_number;
