CREATE TABLE safety_incidents (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type             VARCHAR(50)  NOT NULL,
    severity         VARCHAR(20)  NOT NULL DEFAULT 'MEDIUM',
    status           VARCHAR(20)  NOT NULL DEFAULT 'OPEN',
    description      TEXT,
    ride_id          UUID REFERENCES rides(id) ON DELETE SET NULL,
    reporter_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    reporter_role    VARCHAR(20),
    location_text    TEXT,
    district         TEXT,
    notes            TEXT,
    reported_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE incident_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id UUID NOT NULL REFERENCES safety_incidents(id) ON DELETE CASCADE,
    event_text  TEXT NOT NULL,
    kind        VARCHAR(20) NOT NULL DEFAULT 'system',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_incidents_status   ON safety_incidents(status);
CREATE INDEX idx_incidents_severity ON safety_incidents(severity);
CREATE INDEX idx_incidents_reported ON safety_incidents(reported_at DESC);
CREATE INDEX idx_incident_events_incident ON incident_events(incident_id);
