-- Reverse 083. Non-lossy for pre-existing data (column was NULL on every row
-- that predates this migration); any gender captured while it was live is
-- dropped, which is the intended meaning of rolling this back.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_gender_chk;
ALTER TABLE users DROP COLUMN IF EXISTS gender;
