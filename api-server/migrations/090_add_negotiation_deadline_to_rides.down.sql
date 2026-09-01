-- Reverse 090. Non-lossy: every row's value here is a recomputable timer
-- deadline, never durable business data, so dropping it loses nothing that
-- matters once rolled back (the in-memory timer alone reverts to being the
-- only negotiation-timeout mechanism, same as before this migration).
ALTER TABLE rides DROP COLUMN IF EXISTS negotiation_deadline_at;
