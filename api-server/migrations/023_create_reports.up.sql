CREATE TABLE reports (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    template     VARCHAR(100) NOT NULL,
    status       VARCHAR(20)  NOT NULL DEFAULT 'QUEUED',
    format       VARCHAR(10)  NOT NULL DEFAULT 'PDF',
    date_range   VARCHAR(100),
    file_path    TEXT,
    file_size    TEXT,
    generated_at TIMESTAMPTZ,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE scheduled_reports (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    template   VARCHAR(100) NOT NULL,
    format     VARCHAR(10)  NOT NULL DEFAULT 'PDF',
    frequency  VARCHAR(20)  NOT NULL,
    recipients TEXT[]       NOT NULL DEFAULT '{}',
    is_active  BOOLEAN      NOT NULL DEFAULT TRUE,
    next_run   TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_reports_status  ON reports(status);
CREATE INDEX idx_reports_created ON reports(created_at DESC);
