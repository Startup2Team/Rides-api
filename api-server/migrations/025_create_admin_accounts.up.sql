CREATE TABLE admin_roles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(50) UNIQUE NOT NULL,
    description TEXT,
    permissions JSONB       NOT NULL DEFAULT '[]',
    is_system   BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO admin_roles (name, description, permissions, is_system) VALUES
    ('Super Admin',        'Full platform access',                         '["*"]',                                                                                                                                                                           TRUE),
    ('Operations Manager', 'Manage drivers, rides, and live operations',   '["/admin","/admin/drivers","/admin/customers","/admin/live-rides","/admin/negotiations","/admin/heatmaps","/admin/safety-center"]',                                              FALSE),
    ('Finance Manager',    'Access revenue and analytics data',            '["/admin","/admin/revenue","/admin/analytics","/admin/reports"]',                                                                                                                 FALSE),
    ('Support Staff',      'Handle tickets and inbox',                     '["/admin","/admin/support","/admin/inbox","/admin/safety-center"]',                                                                                                               FALSE),
    ('Analytics Staff',    'View analytics, reports, and heatmaps',        '["/admin","/admin/analytics","/admin/reports","/admin/heatmaps"]',                                                                                                                FALSE);

CREATE TABLE admin_accounts (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           VARCHAR(255) NOT NULL,
    email          VARCHAR(255) UNIQUE NOT NULL,
    password_hash  TEXT,
    role_id        UUID        NOT NULL REFERENCES admin_roles(id),
    status         VARCHAR(20) NOT NULL DEFAULT 'INVITED',
    two_factor     BOOLEAN     NOT NULL DEFAULT FALSE,
    last_active_at TIMESTAMPTZ,
    invited_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_admin_accounts_email  ON admin_accounts(email);
CREATE INDEX idx_admin_accounts_status ON admin_accounts(status);
