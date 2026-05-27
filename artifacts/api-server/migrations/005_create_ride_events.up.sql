-- Append-only event log — records are NEVER updated or deleted.
CREATE TABLE IF NOT EXISTS ride_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id      UUID NOT NULL REFERENCES rides(id),
    event_type   VARCHAR(50) NOT NULL,
    actor_role   VARCHAR(20) NOT NULL,
    actor_id     UUID NOT NULL,
    payload      JSONB,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ride_events_ride_id ON ride_events(ride_id);
CREATE INDEX IF NOT EXISTS idx_ride_events_event_type ON ride_events(event_type);
CREATE INDEX IF NOT EXISTS idx_ride_events_occurred_at ON ride_events(occurred_at DESC);
