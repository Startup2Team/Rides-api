CREATE TABLE IF NOT EXISTS negotiation_rounds (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id           UUID NOT NULL REFERENCES rides(id),
    round_number      INT NOT NULL,
    proposed_by       VARCHAR(20) NOT NULL,
    proposed_amount   DECIMAL(10,2) NOT NULL,
    response          VARCHAR(20),
    call_initiated    BOOLEAN NOT NULL DEFAULT FALSE,
    call_initiated_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_negotiation_rounds_ride_id ON negotiation_rounds(ride_id);
