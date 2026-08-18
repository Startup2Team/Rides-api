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
ALTER TABLE users
    ADD CONSTRAINT users_national_id_number_chk
    CHECK (
        national_id_number IS NULL
        OR national_id_number ~ '^[A-Z0-9]{5,16}$'
    );

-- A number cannot be interpreted or validated without knowing its issuing country,
-- so a country is mandatory whenever a number is present.
ALTER TABLE users
    ADD CONSTRAINT users_national_id_country_chk
    CHECK (
        national_id_number IS NULL
        OR (national_id_country IS NOT NULL AND national_id_country ~ '^[A-Z]{2}$')
    );

-- One national ID = one account, scoped by issuing country. Partial index so the
-- many NULL rows during rollout never collide (Postgres already treats NULLs as
-- distinct in a unique index; the predicate makes the intent explicit and keeps the
-- index small). A 23505 unique-violation on this index is the app's signal for
-- "this national ID is already registered" — the ban-evasion / bonus-farming guard.
-- Legitimate admin corrections are a plain UPDATE, which this index permits.
CREATE UNIQUE INDEX IF NOT EXISTS uq_users_national_id
    ON users (national_id_country, national_id_number)
    WHERE national_id_number IS NOT NULL;
