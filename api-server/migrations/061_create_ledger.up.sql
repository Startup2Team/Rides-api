-- Double-entry general ledger. Replaces the read-time synthesis in
-- internal/finance with a persisted, append-only journal so the GL, Trial
-- Balance and Balance Sheet reconcile from real ledger entries.
-- See docs/backend/LEDGER_DESIGN.md.

CREATE TABLE IF NOT EXISTS ledger_accounts (
    code         varchar(20)  PRIMARY KEY,
    name         varchar(100) NOT NULL,
    type         varchar(12)  NOT NULL CHECK (type IN ('ASSET','LIABILITY','EQUITY','REVENUE','EXPENSE')),
    normal_side  varchar(6)   NOT NULL CHECK (normal_side IN ('DEBIT','CREDIT')),
    is_active    boolean      NOT NULL DEFAULT true,
    created_at   timestamptz  NOT NULL DEFAULT now()
);

-- Seed the chart of accounts. v1 actively uses 1000/1010/4000 (package sales);
-- the rest are seeded-but-dormant for future commission/wallet/accrual flows.
INSERT INTO ledger_accounts (code, name, type, normal_side) VALUES
    ('1000', 'Cash & Bank — MoMo',                    'ASSET',     'DEBIT'),
    ('1010', 'Cash & Bank — Manual',                  'ASSET',     'DEBIT'),
    ('2000', 'Driver Wallet Balances',                'LIABILITY', 'CREDIT'),
    ('2100', 'Deferred Revenue — Unused Ride Credits','LIABILITY', 'CREDIT'),
    ('3000', 'Retained Earnings',                     'EQUITY',    'CREDIT'),
    ('4000', 'Package Sales Revenue',                 'REVENUE',   'CREDIT'),
    ('4100', 'Commission Revenue',                    'REVENUE',   'CREDIT'),
    ('5000', 'Promotional Credits',                   'EXPENSE',   'DEBIT'),
    ('5100', 'Payment Processing Fees',               'EXPENSE',   'DEBIT')
ON CONFLICT (code) DO NOTHING;

CREATE TABLE IF NOT EXISTS journal_entries (
    id              uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    entry_date      timestamptz  NOT NULL,            -- economic date (e.g. paid_at), not created_at
    description     text         NOT NULL,
    source_type     varchar(40)  NOT NULL,            -- 'package_purchase' | 'purchase_refund' | ...
    source_id       uuid,                             -- originating row
    idempotency_key varchar(120) NOT NULL UNIQUE,     -- one entry per economic event
    created_by      varchar(40)  NOT NULL DEFAULT 'system',
    created_at      timestamptz  NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS journal_lines (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    entry_id     uuid        NOT NULL REFERENCES journal_entries(id) ON DELETE RESTRICT,
    account_code varchar(20) NOT NULL REFERENCES ledger_accounts(code),
    debit_rwf    bigint      NOT NULL DEFAULT 0 CHECK (debit_rwf  >= 0),
    credit_rwf   bigint      NOT NULL DEFAULT 0 CHECK (credit_rwf >= 0),
    memo         text,
    -- exactly one side is non-zero per line
    CONSTRAINT journal_lines_one_side CHECK ((debit_rwf = 0) <> (credit_rwf = 0))
);

CREATE INDEX IF NOT EXISTS idx_journal_lines_entry   ON journal_lines(entry_id);
CREATE INDEX IF NOT EXISTS idx_journal_lines_account ON journal_lines(account_code);
CREATE INDEX IF NOT EXISTS idx_journal_entries_date  ON journal_entries(entry_date);
CREATE INDEX IF NOT EXISTS idx_journal_entries_src   ON journal_entries(source_type, source_id);
