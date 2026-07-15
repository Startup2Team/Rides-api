-- Multi-device FCM push. Replaces the single users.fcm_token (which a second
-- device login would overwrite) with one row per (user, device token), so a
-- user can receive pushes on every device they're signed in on. The legacy
-- users.fcm_token is kept in sync for the matching engine / expiry notifier.

CREATE TABLE IF NOT EXISTS device_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token      VARCHAR(512) NOT NULL,
    platform   VARCHAR(16) NOT NULL DEFAULT 'unknown', -- 'ios' | 'android' | 'web'
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, token)
);

CREATE INDEX IF NOT EXISTS idx_device_tokens_user_id ON device_tokens(user_id);
-- A physical device belongs to one account at a time; prune stale ownership when
-- the same token re-registers under a new user.
CREATE INDEX IF NOT EXISTS idx_device_tokens_token ON device_tokens(token);
