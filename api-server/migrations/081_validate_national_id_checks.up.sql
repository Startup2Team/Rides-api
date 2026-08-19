-- DB-1 round 2 (lock review), step 2 of 3: validate the two CHECK constraints
-- 080 added NOT VALID.
--
-- VALIDATE CONSTRAINT takes only SHARE UPDATE EXCLUSIVE on `users` (allows
-- concurrent reads AND writes) while it scans existing rows — unlike the
-- ACCESS EXCLUSIVE a plain `ADD CONSTRAINT ... CHECK (...)` would hold for the
-- same scan. Running this in a SEPARATE migration/transaction from 080's
-- ADD CONSTRAINT ... NOT VALID is what actually gets the lower lock: if both
-- ran in the same transaction, the ACCESS EXCLUSIVE from ADD CONSTRAINT would
-- still be held (Postgres holds locks until COMMIT, not per-statement) for
-- the whole VALIDATE CONSTRAINT scan too, defeating the point.
--
-- Every existing row is NULL for national_id_number/country (080 added them
-- with no backfill), so this scan is a formality — the split is still the
-- correct pattern for any table this size might grow to before this ships.
ALTER TABLE users VALIDATE CONSTRAINT users_national_id_number_chk;
ALTER TABLE users VALIDATE CONSTRAINT users_national_id_country_chk;
