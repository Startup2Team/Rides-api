-- Ride optimistic concurrency + create idempotency for mobile retries.

ALTER TABLE rides
    ADD COLUMN IF NOT EXISTS ride_version INT NOT NULL DEFAULT 1;

CREATE TABLE IF NOT EXISTS ride_command_idempotency (
    actor_user_id   UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    idempotency_key VARCHAR(128) NOT NULL,
    ride_id         UUID         NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (actor_user_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_ride_idempotency_ride ON ride_command_idempotency(ride_id);
