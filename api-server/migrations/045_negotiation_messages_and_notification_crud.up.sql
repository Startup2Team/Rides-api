-- 1. Extend negotiation_rounds with an optional text message per round
ALTER TABLE negotiation_rounds
  ADD COLUMN IF NOT EXISTS message TEXT;

-- 2. Add a dedicated negotiation text-messages table for chat-style messages
--    (separate from price proposals tracked in negotiation_rounds).
CREATE TABLE IF NOT EXISTS negotiation_messages (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id    UUID NOT NULL REFERENCES rides(id),
    sender     VARCHAR(20) NOT NULL,   -- "CUSTOMER" | "DRIVER" | "SYSTEM"
    body       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_neg_messages_ride ON negotiation_messages(ride_id, created_at);

-- 3. notifications table already exists from migration 029; ensure indexes
--    are present for the new user-facing queries.
CREATE INDEX IF NOT EXISTS idx_notif_user_time
  ON notifications(user_id, sent_at DESC);
