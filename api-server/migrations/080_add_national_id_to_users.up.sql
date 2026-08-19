-- DB-1: capture the national identity NUMBER for a person.
--
-- Until now the national ID existed only as document IMAGES (driver_documents
-- NATIONAL_ID_FRONT / NATIONAL_ID_BACK). The number itself was never stored, so it
-- was unsearchable and could not be enforced as one-ID-one-account (ban evasion,
-- registration-bonus farming).
--
-- The number is an attribute of the PERSON, not of the driver ROLE, so it lives on
-- `users`: there is exactly one users row per human, driver_profiles is 1:1 with
-- users (user_id UNIQUE), and customers are users too. Putting it here makes a
-- future customer-KYC requirement a no-op schema-wise instead of a second column
-- on a second table.
--
-- Additive + reversible: both columns are NULL-able, every existing row stays NULL,
-- and there is NO backfill in this migration. Numbers are captured going forward at
-- onboarding; historical drivers can be back-filled operationally from the ID
-- images already on file, as a separate data step.
--
-- DB-1 round 2 (lock review): this file used to ALSO create the uq_users_national_id
-- UNIQUE INDEX and add both CHECK constraints WITH full validation in one shot —
-- all three take ACCESS EXCLUSIVE on `users` for the whole operation (a full-table
-- scan for the CHECKs, a full index build for the UNIQUE INDEX), which on a table
-- every request touches would have blocked reads AND writes for the duration.
-- Split for a zero/near-zero-downtime rollout:
--   1. THIS file: add the columns, add both CHECK constraints NOT VALID (near-
--      instant — NOT VALID skips the validation scan, so ACCESS EXCLUSIVE is held
--      only long enough to write catalog metadata).
--   2. 081_validate_national_id_checks: VALIDATE CONSTRAINT for both, in ITS OWN
--      migration/transaction — only SHARE UPDATE EXCLUSIVE (does not block reads
--      or writes) while it scans existing rows. Every existing row is NULL here,
--      so this scan is a formality, not a performance concern.
--   3. 082_add_national_id_unique_index_concurrently: CREATE UNIQUE INDEX
--      CONCURRENTLY, alone in its own migration file (see that file for why it
--      MUST be alone).
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS national_id_number  VARCHAR(16),
    -- ISO-3166-1 alpha-2 issuing country (e.g. 'RW', 'UG'). Drives country-aware
    -- validation and scopes uniqueness: the Rwanda 16-digit and Uganda 14-char
    -- namespaces are disjoint, so uniqueness must be per-country, not global.
    ADD COLUMN IF NOT EXISTS national_id_country VARCHAR(2);

-- Defense-in-depth backstop ONLY. The authoritative, country-specific format check
-- runs in the app (RW = ^\d{16}$, UG = ^[A-Z0-9]{14}$), where it can evolve as new
-- countries are added without a migration. This CHECK is deliberately lenient so it
-- never rejects a valid future-country ID: it only asserts the value is normalized
-- (no spaces / punctuation), upper-case alphanumeric, and 5..16 characters.
--
-- NOT VALID: added but not yet checked against existing rows — 081 validates it in
-- a separate, lower-lock-level step. Every existing row is NULL (which the CHECK
-- explicitly permits), so this is a formality, not a real gap in coverage.
ALTER TABLE users
    ADD CONSTRAINT users_national_id_number_chk
    CHECK (
        national_id_number IS NULL
        OR national_id_number ~ '^[A-Z0-9]{5,16}$'
    ) NOT VALID;

-- A number cannot be interpreted or validated without knowing its issuing country,
-- so a country is mandatory whenever a number is present. NOT VALID — see above.
ALTER TABLE users
    ADD CONSTRAINT users_national_id_country_chk
    CHECK (
        national_id_number IS NULL
        OR (national_id_country IS NOT NULL AND national_id_country ~ '^[A-Z]{2}$')
    ) NOT VALID;
