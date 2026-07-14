# Double-Entry Ledger — Design

Status: **Draft for review** · Owner: Backend · Admin-web impact: **none** (response shapes preserved)

## 1. Why

The `Accounting` release gate requires "General Ledger, Trial Balance and Balance
Sheet reconcile from ledger entries." Today they do not:

- There is **no persisted ledger**. `finance.GetGeneralLedger` (`internal/finance/service.go:18`)
  *synthesizes* debit/credit pairs in memory at request time from `wallet_transactions`
  and `payments`. Trial Balance and Balance Sheet derive from that same synthesis, so
  they reconcile only with each other (tautologically), against nothing independent.
- Account "codes" are hardcoded English display strings (`service.go:39-133`).
- `CREDIT_GRANT` / `REFUND` wallet types are never posted → derived balances drift.
- **The reports read the wrong tables.** The `payments` table (per-ride fare/commission)
  is **never written by any code** — grep for `INSERT INTO payments` returns nothing —
  so the entire "Commission Revenue" branch reads an empty table. Meanwhile the platform's
  actual revenue lives in `package_purchases`, which the finance module never reads.

## 2. The real money model (verified)

| Event | Table | Real money? | Notes |
|---|---|---|---|
| Driver buys a ride package (MoMo / cash / bank) | `package_purchases` (`price_paid_rwf`, `status=PAID`, `payment_provider`, `paid_at`) | **Yes — this is essentially the platform's only real cash inflow** | Confirmed via `packages.confirm()`; also admin manual settle + admin create-on-behalf |
| Ride credit consumed / refunded | `ride_credit_ledger` | No — **entitlement units** (ride counts), not money | Debited on fare agreement, refunded on blameless cancel |
| Per-ride fare / commission | `payments` | **No — table is unused** | Fares are negotiated cash between customer & driver, off-platform |
| Wallet top-up / withdraw | `wallet_transactions` | No — **disabled** | `TopUp`/`Withdraw` return `PAYMENTS_DISABLED` (`wallet/service.go:51`) |

**Conclusion:** for v1 the ledger only needs to faithfully record **package sales** (and their
refunds). Commission and wallet flows are dormant; the design reserves accounts for them so
they can be switched on later without a schema change.

## 3. Design

### 3.1 Chart of accounts (`ledger_accounts`)

Seeded, stable codes with an accounting type. v1 uses the **cash-basis** accounts (revenue
recognized when a package is paid); the rest are seeded-but-dormant for future flows.

| Code | Name | Type | Normal | v1 |
|---|---|---|---|---|
| `1000` | Cash & Bank — MoMo | ASSET | DEBIT | ✅ |
| `1010` | Cash & Bank — Manual (cash/bank) | ASSET | DEBIT | ✅ |
| `2000` | Driver Wallet Balances | LIABILITY | CREDIT | dormant |
| `2100` | Deferred Revenue — Unused Ride Credits | LIABILITY | CREDIT | dormant (accrual upgrade) |
| `3000` | Retained Earnings | EQUITY | CREDIT | derived |
| `4000` | Package Sales Revenue | REVENUE | CREDIT | ✅ |
| `4100` | Commission Revenue | REVENUE | CREDIT | dormant |
| `5000` | Promotional Credits (contra-revenue) | EXPENSE | DEBIT | dormant |
| `5100` | Payment Processing Fees | EXPENSE | DEBIT | dormant |

### 3.2 Journal schema (migration `061`)

```sql
CREATE TABLE ledger_accounts (
    code         varchar(20)  PRIMARY KEY,
    name         varchar(100) NOT NULL,
    type         varchar(12)  NOT NULL CHECK (type IN ('ASSET','LIABILITY','EQUITY','REVENUE','EXPENSE')),
    normal_side  varchar(6)   NOT NULL CHECK (normal_side IN ('DEBIT','CREDIT')),
    is_active    boolean      NOT NULL DEFAULT true,
    created_at   timestamptz  NOT NULL DEFAULT now()
);

CREATE TABLE journal_entries (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    entry_date      timestamptz NOT NULL,            -- economic date (e.g. paid_at), NOT created_at
    description     text        NOT NULL,
    source_type     varchar(40) NOT NULL,            -- 'package_purchase' | 'purchase_refund' | ...
    source_id       uuid,                            -- originating row (package_purchases.id, ...)
    idempotency_key varchar(120) UNIQUE NOT NULL,    -- one entry per economic event
    created_by      varchar(40) NOT NULL DEFAULT 'system',
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE journal_lines (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    entry_id     uuid        NOT NULL REFERENCES journal_entries(id) ON DELETE RESTRICT,
    account_code varchar(20) NOT NULL REFERENCES ledger_accounts(code),
    debit_rwf    bigint      NOT NULL DEFAULT 0 CHECK (debit_rwf  >= 0),
    credit_rwf   bigint      NOT NULL DEFAULT 0 CHECK (credit_rwf >= 0),
    memo         text,
    -- exactly one side is non-zero per line
    CHECK ((debit_rwf = 0) <> (credit_rwf = 0))
);
CREATE INDEX idx_journal_lines_entry   ON journal_lines(entry_id);
CREATE INDEX idx_journal_lines_account ON journal_lines(account_code);
CREATE INDEX idx_journal_entries_date  ON journal_entries(entry_date);
CREATE INDEX idx_journal_entries_src   ON journal_entries(source_type, source_id);
```

**Invariants**
- *Balanced entry*: `sum(debit_rwf) == sum(credit_rwf)` per entry — enforced in the posting
  service before insert (and optionally a deferred `CONSTRAINT TRIGGER` as a DB backstop).
- *Append-only*: no `UPDATE`/`DELETE` of posted lines. Mistakes are fixed with a **reversing
  entry** (source_type `reversal`, negated lines). This is what makes the ledger auditable and
  makes Trial Balance a real check rather than a tautology.
- *Idempotent*: `idempotency_key` UNIQUE → posting the same economic event twice is a no-op.

### 3.3 Posting service (`internal/ledger`)

```go
type Line struct {
    Account string // ledger_accounts.code
    Debit   int64  // RWF, exactly one of Debit/Credit non-zero
    Credit  int64
    Memo    string
}

type Entry struct {
    Date           time.Time
    Description    string
    SourceType     string
    SourceID       *string
    IdempotencyKey string
    CreatedBy      string
    Lines          []Line
}

// Post validates the entry is balanced and writes header+lines. It takes an
// existing pgx.Tx so it composes atomically with the caller's transaction
// (e.g. the package confirm tx). ON CONFLICT (idempotency_key) DO NOTHING makes
// a duplicate post a silent no-op — safe under webhook + reconcile + admin races.
func (s *Service) Post(ctx context.Context, tx pgx.Tx, e Entry) error
```

Post rejects: unbalanced entries, unknown account codes, lines with both/neither side set.

### 3.4 Posting rules

All money-affecting transitions funnel through a small number of choke points, so few hooks
are needed.

| Economic event | Hook point | Journal entry | Idempotency key |
|---|---|---|---|
| Package purchase → PAID (MoMo settle, manual confirm, admin create-on-behalf) | `packages.confirm()` + the two direct `markPaid` sites, via one shared `postPurchasePaid(tx, purchase)` | Dr `1000`/`1010` (by method) `price_paid` · Cr `4000` `price_paid` | `purchase_paid:<purchaseID>` |
| Purchase refunded/reversed | wherever a PAID purchase is reversed | Dr `4000` · Cr `1000`/`1010` | `purchase_refund:<purchaseID>` |
| Free/promo package (price 0) | — | **no entry** (no cash); optionally memo Dr `5000` / Cr `4000` at cost | `purchase_promo:<id>` |
| *(future)* per-ride commission, when `payments` is actually written | payment settle | Dr `1000`/AR · Cr `4100` | `commission:<rideID>` |
| *(future)* wallet top-up / withdraw, when enabled | `wallet` service | Dr/Cr `1000` ↔ `2000` | `wallet_txn:<txnID>` |

`postPurchasePaid` runs **inside the same DB transaction** as `markPaid` + `GrantPurchase`,
so a purchase can never be PAID-and-granted without its ledger entry (and vice versa).

### 3.5 Report refactor (`internal/finance`)

Rewrite the three readers to query the journal — response DTOs unchanged:

- **General Ledger**: `journal_lines ⋈ journal_entries ⋈ ledger_accounts`, filtered by
  `entry_date` range, ordered by date. Now every row is a real persisted posting.
- **Trial Balance**: `GROUP BY account_code` → `sum(debit)`, `sum(credit)`. `Balanced` becomes a
  *genuine* check (a hand-edited DB row would surface as imbalance), not a tautology.
- **Balance Sheet**: classify by `ledger_accounts.type`. Assets = Σ(debit−credit) over ASSET
  accounts; Liabilities/Equity likewise; Retained Earnings = Σ Revenue − Σ Expense. Assets ==
  Liabilities + Equity holds by construction.
- **Revenue reports** (`/admin/revenue*`): read `4000`/`4100` from the ledger so they tie out to
  the GL — closing the "two views disagree" gap.

Delete the `wallet_transactions`+`payments` in-memory synthesis once the ledger is the source.

## 4. Migration & backfill

1. `061_create_ledger.up.sql`: the three tables + seed `ledger_accounts`.
2. **Backfill** `cmd/ledger-backfill` (one-off, idempotent): read every historical
   `package_purchases WHERE status='PAID'` and call `postPurchasePaid` (keyed by purchase id,
   so re-running is safe and it won't double-post live entries). Logs count posted/skipped.
3. `.down.sql` drops the tables (data loss — dev/staging only).

## 5. Phasing (each phase independently shippable)

1. **Schema + service** — migration `061`, `internal/ledger`, seed COA. No behavior change.
2. **Wire postings** — `postPurchasePaid` into `confirm()` + the two `markPaid` paths + refund.
   New paid purchases start posting.
3. **Backfill** — run `cmd/ledger-backfill` so history is represented.
4. **Refactor reports** — GL/TB/BS/revenue read the ledger; drop the old synthesis. Keep JSON
   response shapes identical → **admin-web unchanged**.
5. *(optional, later)* accrual/deferred-revenue recognition on credit consumption; per-ride
   commission; wallet events; real PDF/Excel export (today `reports.Generate` writes a fake path).

## 6. Testing

- Posting service: rejects unbalanced entries, unknown accounts, dual/empty-sided lines;
  idempotency no-op on duplicate key; atomic rollback with caller tx.
- Posting rules: MoMo settle, manual confirm, admin create-on-behalf, refund each produce the
  expected balanced entry exactly once (including under duplicate-callback + reconcile races).
- Reports: seed entries → Trial Balance balanced, Balance Sheet balances, GL totals tie to
  revenue reports (the reconciliation the release gate asks for).

## 7. Open questions

- **Cash-basis vs accrual**: v1 recognizes revenue at sale (`4000` on PAID). If finance wants
  unused prepaid credits shown as a liability, switch to deferred revenue (`2100`) at sale +
  recognize `4000` on each credit consumption — more postings, needs a per-credit monetary value
  (`price_paid / rides`) stored on the grant. Recommend cash-basis for launch.
- **Payment processing fees** (`5100`): capture MTN's cut per settlement? Needs the fee from the
  MoMo response.
- **Multi-currency**: assumed RWF-only (matches the platform). No FX accounts.
