-- Rider gender, captured optionally via PUT /customer/profile (never required
-- at registration). Mirrors migration 055 (driver_profiles.gender) — this is
-- the customer-side counterpart, added straight to `users` rather than a
-- customer_profiles table so it works the same way regardless of role_state.
--
-- Additive + reversible: nullable, no default change for existing rows, no
-- backfill. A lenient CHECK backstops the app-level oneof=male,female,other
-- validation (internal/customer.ProfileUpdate) the same way migration 080/081
-- backstop pkg/nationalid.
--
-- Review fix: this used to add the CHECK fully validated in one shot, which
-- takes ACCESS EXCLUSIVE + a full-table scan on `users` for the duration —
-- the exact pattern 080/081 split to avoid, and just as applicable here since
-- `users` is touched by every request. NOT VALID here (near-instant — no scan,
-- ACCESS EXCLUSIVE held only long enough to write catalog metadata);
-- 085_validate_users_gender_check validates it in its own migration/
-- transaction at SHARE UPDATE EXCLUSIVE, which does not block reads or
-- writes. Every existing row is NULL, so that scan is a formality too.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS gender VARCHAR(20);

ALTER TABLE users
    ADD CONSTRAINT users_gender_chk
    CHECK (gender IS NULL OR gender IN ('male', 'female', 'other'))
    NOT VALID;
