-- Rider gender, captured optionally via PUT /customer/profile (never required
-- at registration). Mirrors migration 055 (driver_profiles.gender) — this is
-- the customer-side counterpart, added straight to `users` rather than a
-- customer_profiles table so it works the same way regardless of role_state.
--
-- Additive + reversible: nullable, no default change for existing rows, no
-- backfill. A lenient CHECK backstops the app-level oneof=male,female,other
-- validation (internal/customer.ProfileUpdate) the same way migration 080/081
-- backstop pkg/nationalid — cheap to satisfy (every existing row is NULL) so
-- this single ALTER TABLE does not need 080's NOT VALID/VALIDATE split.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS gender VARCHAR(20);

ALTER TABLE users
    ADD CONSTRAINT users_gender_chk
    CHECK (gender IS NULL OR gender IN ('male', 'female', 'other'));
