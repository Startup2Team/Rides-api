-- No-op. VALIDATE CONSTRAINT has no meaningful inverse (a constraint cannot be
-- un-validated in place) — rolling back to "not validated" isn't a real state
-- worth reconstructing. 083's down migration drops the constraint entirely,
-- which is the actual reversal; this file exists only so golang-migrate has a
-- down step to run for this version number. Mirrors 081's down.
SELECT 1;
