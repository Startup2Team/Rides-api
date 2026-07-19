CREATE TABLE IF NOT EXISTS admin_notifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       VARCHAR(100) NOT NULL,
    body        TEXT NOT NULL,
    audience    VARCHAR(20) NOT NULL, -- 'ALL', 'DRIVERS', 'CUSTOMERS'
    status      VARCHAR(20) NOT NULL DEFAULT 'SENT', -- 'SENT', 'SCHEDULED', 'DRAFT'
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by  VARCHAR(100),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_admin_notifications_created ON admin_notifications(created_at DESC);
