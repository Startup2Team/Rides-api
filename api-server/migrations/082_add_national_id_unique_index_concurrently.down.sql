-- DROP INDEX CONCURRENTLY has the SAME "must be the only statement in this
-- file" requirement as CREATE INDEX CONCURRENTLY (see the .up.sql for why) —
-- do not add anything else here.
DROP INDEX CONCURRENTLY IF EXISTS uq_users_national_id;
