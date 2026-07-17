CREATE TABLE partners (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    logo_url TEXT,
    contact_name VARCHAR(255) NOT NULL,
    contact_email VARCHAR(255) NOT NULL,
    contact_phone VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE adverts (
    id UUID PRIMARY KEY,
    partner_id UUID NOT NULL REFERENCES partners(id) ON DELETE CASCADE,
    image_url TEXT,
    headline VARCHAR(255) NOT NULL,
    cta_label VARCHAR(100) NOT NULL,
    cta_link TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT true,
    start_date TIMESTAMPTZ,
    end_date TIMESTAMPTZ,
    priority INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Indexes for performance
CREATE INDEX idx_adverts_partner_id ON adverts(partner_id);
CREATE INDEX idx_adverts_active_dates ON adverts(active, start_date, end_date);
