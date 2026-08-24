DROP INDEX IF EXISTS waitlist_signups_role_email_key;

-- Forward-mostly: this fails with a NOT NULL violation if any row inserted
-- while phone was optional actually has phone IS NULL. That's intentional —
-- silently deleting or backfilling those rows to make the down migration
-- succeed would destroy real waitlist signups. If you need to roll back
-- after phone-optional signups exist, decide what to do with those rows by
-- hand first (backfill a placeholder or delete them), then re-run this down
-- migration.
ALTER TABLE waitlist_signups ALTER COLUMN phone SET NOT NULL;
