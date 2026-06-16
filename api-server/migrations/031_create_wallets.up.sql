-- Wallets are defined HERE authoritatively, in the shape the app actually uses
-- (wallet_transactions needs user_id, phone_number, external_ref).
--
-- Migration 029 (schema_v3_align) ALSO declares wallets/wallet_transactions with
-- a different, older shape. On a clean database 029 runs first and creates the
-- wrong shape, which then breaks this migration (and the wallet code). We drop
-- and recreate here so the schema is correct regardless of 029. This is safe:
-- on a fresh migrate there is no wallet data yet, and on an already-migrated DB
-- this file has already been applied and will not re-run.
DROP TABLE IF EXISTS wallet_transactions CASCADE;
DROP TABLE IF EXISTS wallets CASCADE;

-- One wallet per user (customer or driver — same person when they switch modes).
-- balance_rwf is stored in integer Rwanda Francs (no decimals needed).
CREATE TABLE wallets (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID         NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    balance_rwf  BIGINT       NOT NULL DEFAULT 0 CHECK (balance_rwf >= 0),
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_wallets_user ON wallets (user_id);

-- Audit log for every wallet movement.
CREATE TABLE wallet_transactions (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id       UUID         NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    user_id         UUID         NOT NULL REFERENCES users(id),
    type            VARCHAR(20)  NOT NULL, -- TOP_UP | WITHDRAW | PACKAGE_PURCHASE | CREDIT_GRANT | REFUND
    amount_rwf      BIGINT       NOT NULL, -- always positive; direction implied by type
    balance_after   BIGINT       NOT NULL,
    description     TEXT,
    phone_number    VARCHAR(20),           -- MoMo number used for top-up / withdraw
    external_ref    VARCHAR(100),          -- MoMo transaction ID (filled when gateway integrated)
    status          VARCHAR(20)  NOT NULL DEFAULT 'COMPLETED', -- COMPLETED | PENDING | FAILED
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_wallet_txn_wallet  ON wallet_transactions (wallet_id, created_at DESC);
CREATE INDEX idx_wallet_txn_user    ON wallet_transactions (user_id,   created_at DESC);

-- Auto-create a wallet for every existing user.
INSERT INTO wallets (user_id)
SELECT id FROM users
ON CONFLICT (user_id) DO NOTHING;

-- Trigger: auto-create wallet on new user registration.
CREATE OR REPLACE FUNCTION create_wallet_for_new_user()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO wallets (user_id) VALUES (NEW.id) ON CONFLICT DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_create_wallet ON users;
CREATE TRIGGER trg_create_wallet
    AFTER INSERT ON users
    FOR EACH ROW EXECUTE FUNCTION create_wallet_for_new_user();
