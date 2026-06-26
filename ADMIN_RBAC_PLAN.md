# Admin — Two-Role Design (Support + Owner)

Greenfield design. We're not keeping the old `admin_roles`/`admin_accounts`
permission-engine scaffolding — for **two fixed roles** that's over-engineered.
Two roles, one of which is a strict superset of the other, is a **role enum +
two middleware guards**. Simple, fast, impossible to misconfigure.

---

## 1. The two roles

| Role | Mental model | Scope |
|------|--------------|-------|
| **SUPPORT** | The helpdesk + onboarding desk | Operational. Approve/reject riders, fix rider & customer accounts, CRUD the operational records (drivers, customers, tickets, incidents). **No money, no config, no audit.** |
| **OWNER** | The boss | Everything SUPPORT can do **plus** packages & pricing CRUD, admin/user management, payments & "who paid", analytics, and the audit log. |

**OWNER ⊃ SUPPORT.** Anything support can do, owner can do. So enforcement is
trivial: support routes allow `{SUPPORT, OWNER}`; owner routes allow `{OWNER}`.

### Engineering call: enum, not a permission engine
- 2 fixed roles → a `role` enum column + two guards. No JSONB permissions, no
  path-prefix matching, no role-CRUD UI. **YAGNI.**
- If you ever need a 3rd/4th *custom* role, you migrate to a permissions table
  then — not before. The enum is a one-line change away from that if needed.

---

## 2. Capability matrix (grounded in the actual backend)

| Area | Endpoints (existing unless ✦new) | SUPPORT | OWNER |
|------|----------------------------------|:------:|:----:|
| Dashboard (read) | `/admin/dashboard*` | ✅ | ✅ |
| **Driver approval** | `/admin/drivers/{id}/approve` `/reject` `/verify` | ✅ | ✅ |
| Drivers CRUD | `/admin/drivers` (list/get/create/update), suspend/reinstate/force-offline | ✅ | ✅ |
| Customers CRUD | `/admin/customers*`, suspend/reinstate, ban | ✅ | ✅ |
| **Account assist** ✦ | clear OTP lockout, resend OTP, un-suspend, clear fraud/GPS flags | ✅ | ✅ |
| Live rides | `/admin/live-rides*`, intervene | ✅ | ✅ |
| Negotiations (view) | `/admin/negotiations*` | ✅ | ✅ |
| Tickets / Inbox / Incidents CRUD | `/admin/support/*`, `/admin/inbox*`, `/admin/incidents*` | ✅ | ✅ |
| Fraud flags (view) | `/admin/flags/*` | ✅ | ✅ |
| **Packages CRUD + price** | `/admin/packages*` | ❌ | ✅ |
| **Pricing (fares)** | `/admin/pricing*` | ❌ | ✅ |
| **Bonuses CRUD** | `/admin/bonuses*` | ❌ | ✅ |
| **Payments / "who paid"** | `/admin/revenue*`, `/admin/revenue/transactions`, payouts | ❌ | ✅ |
| **Analytics (real)** | `/admin/analytics*`, `/admin/reports*` | ❌ | ✅ |
| **Users & admins CRUD** | `/admin/users*`, `/admin/team*` (create Support staff) | ❌ | ✅ |
| **Settings / config** | `/admin/settings*` | ❌ | ✅ |
| **Audit log (view)** ✦ | `/admin/audit` | ❌ | ✅ |

> Decision to confirm: **hard-deleting** a driver/customer. I'd keep destructive
> *deletes* OWNER-only even though support has the rest of CRUD (support gets
> create/read/update + suspend, which covers real helpdesk work). Say if you want
> support to delete too.

---

## 3. Data model (start from zero)

```sql
-- one table, role is an enum, no separate roles table
CREATE TYPE admin_role AS ENUM ('SUPPORT', 'OWNER');

CREATE TABLE admin_users (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name          VARCHAR(255) NOT NULL,
  email         VARCHAR(255) UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  role          admin_role NOT NULL,
  status        VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',  -- ACTIVE | SUSPENDED
  two_factor    BOOLEAN NOT NULL DEFAULT FALSE,
  last_active_at TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- real audit: every mutating admin action, immutable
CREATE TABLE admin_audit_log (
  id          BIGSERIAL PRIMARY KEY,
  admin_id    UUID NOT NULL REFERENCES admin_users(id),
  admin_role  admin_role NOT NULL,
  action      VARCHAR(64) NOT NULL,        -- 'driver.approve', 'package.update', 'user.suspend'
  target_type VARCHAR(32),                 -- 'driver','customer','package','user'
  target_id   UUID,
  summary     TEXT,                        -- human line: "Approved driver Jean-Paul"
  metadata    JSONB,                       -- before/after, amount, reason
  ip          INET,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_audit_created ON admin_audit_log (created_at DESC);
CREATE INDEX idx_audit_admin   ON admin_audit_log (admin_id, created_at DESC);
CREATE INDEX idx_audit_target  ON admin_audit_log (target_type, target_id);
```

**Bootstrap the first OWNER** via a one-time seed migration reading
`ADMIN_BOOTSTRAP_EMAIL` / `ADMIN_BOOTSTRAP_PASSWORD` (or a small CLI). After that,
the owner creates support staff in-app.

---

## 4. Enforcement (zero per-request DB hit)

- Admin login embeds `admin_role` in the JWT (alongside `role_state:"ADMIN"`).
- `middleware.Claims` gets an `AdminRole string` field.
- Two guards:
  ```go
  RequireAdmin()  // role_state == ADMIN  (SUPPORT or OWNER) — operational routes
  RequireOwner()  // AdminRole == "OWNER" — business/config/audit routes
  ```
- Route groups:
  ```go
  r.Route("/admin", func(r) {
    r.Group(public admin auth: login, 2fa)                 // no guard
    r.Group(RequireAdmin)  { drivers, customers, live-rides, tickets, inbox,
                             incidents, flags, account-assist, dashboard }
    r.Group(RequireOwner)  { packages, pricing, bonuses, revenue, analytics,
                             reports, settings, users, team, audit }
  })
  ```
- Enforcement is one enum compare. Role changes take effect within the 15-min
  access-token TTL; instant kick = existing session-revoke.

---

## 5. Audit — make it real, not decorative

- One helper: `audit.Record(ctx, adminID, role, action, targetType, targetID, summary, meta)`.
- Call it at the **end of every mutating owner/support handler** (approve, suspend,
  price change, package CRUD, role change, refund…). One cheap insert.
- `GET /admin/audit?actor=&action=&target=&from=&to=&limit=` — owner-only, paginated.
- Never deletable from the API (append-only).

---

## 6. Tasks (in order)

### Epic 1 — Model & auth
- [ ] **1.1** Migration: `admin_role` enum, `admin_users`, `admin_audit_log`
  (drop/replace old `admin_roles`/`admin_accounts`).
- [ ] **1.2** Bootstrap-owner seed from env (`ADMIN_BOOTSTRAP_EMAIL/PASSWORD`).
- [ ] **1.3** Admin login issues JWT with `admin_role`; rework `team` →
  `adminuser` service (login, 2FA, CRUD admin users — owner-only).
- [ ] **1.4** Add `AdminRole` to `middleware.Claims`.

### Epic 2 — Enforcement
- [ ] **2.1** `RequireAdmin()` + `RequireOwner()` middleware.
- [ ] **2.2** Re-group all `/admin` routes into the two buckets in §4.
- [ ] **2.3** Table-driven tests: SUPPORT gets 403 on every owner route; OWNER
  gets 200 everywhere.

### Epic 3 — Audit
- [ ] **3.1** `admin_audit_log` + `audit.Record` helper.
- [ ] **3.2** Wire it into every mutating admin handler.
- [ ] **3.3** `GET /admin/audit` (owner) with filters + pagination.

### Epic 4 — Support capabilities
- [ ] **4.1** Confirm driver approve/reject/verify in the SUPPORT group.
- [ ] **4.2** Account-assist endpoints: clear OTP lockout, resend OTP, un-suspend,
  clear GPS-anomaly / device-collision flags (wrap existing Redis/DB primitives,
  all audited).
- [ ] **4.3** Read-only account timeline (rides, sessions, flags, suspensions).

### Epic 5 — Owner: payments & real insights
- [ ] **5.1** "Who paid": wallet-transaction + revenue views are real
  (`/admin/revenue/transactions`), filterable by user/date.
- [ ] **5.2** Replace placeholder analytics with real aggregates (rides, GMV,
  take-rate, active drivers, cancel rate); cache heavy ones in a daily rollup so
  the dashboard never strains prod.

### Epic 6 — Web admin
- [ ] **6.1** After login, read `admin_role`; render **Support console** vs
  **Owner console**; gate nav off the role (single source of truth = backend).
- [ ] **6.2** Owner: admin-user management screen (create/suspend Support staff).
- [ ] **6.3** Owner: audit-log viewer.

**Backbone = Epics 1–3** (one migration, two middlewares, an audit helper).
Everything else hangs off that.
