DROP TABLE IF EXISTS ride_command_idempotency;
ALTER TABLE rides DROP COLUMN IF EXISTS ride_version;
